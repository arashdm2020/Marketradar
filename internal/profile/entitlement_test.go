package profile

import (
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"tradebot/internal/entitlement"
)

func grantPlan(t *testing.T, s *Store, id int64, plan entitlement.PlanID, until time.Time) {
	t.Helper()
	if _, err := s.Ensure(id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO entitlements(user_id,subscription_until,plan_id,daily_allowance_override) VALUES(?,?,?,0) ON CONFLICT(user_id) DO UPDATE SET subscription_until=excluded.subscription_until,plan_id=excluded.plan_id,daily_allowance_override=0`, id, until.UTC().Format(time.RFC3339), string(plan)); err != nil {
		t.Fatal(err)
	}
}

func consumeN(t *testing.T, s *Store, user int64, now time.Time, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		id := NewID()
		if err := s.Reserve(id, user, now); err != nil {
			t.Fatalf("reservation %d: %v", i, err)
		}
		if err := s.SetUse(id, "consumed"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPlanQuotasAndSuccessfulConsumption(t *testing.T) {
	now := time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)
	s, err := Open(filepath.Join(t.TempDir(), "plans.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	for _, tc := range []struct {
		id    int64
		plan  entitlement.PlanID
		limit int
	}{{11, entitlement.Visitor, 1}, {12, entitlement.Standard, 5}, {13, entitlement.Premium, 30}} {
		if tc.plan == entitlement.Visitor {
			if _, err = s.Ensure(tc.id); err != nil {
				t.Fatal(err)
			}
		} else {
			grantPlan(t, s, tc.id, tc.plan, now.Add(24*time.Hour))
		}
		consumeN(t, s, tc.id, now, tc.limit)
		if err = s.Reserve(NewID(), tc.id, now); !errors.Is(err, ErrQuota) {
			t.Fatalf("%s quota: got %v", tc.plan, err)
		}
		a, err := s.Access(tc.id, now)
		if err != nil || a.Remaining != 0 || a.Policy.ID != tc.plan {
			t.Fatalf("%s access=%+v err=%v", tc.plan, a, err)
		}
	}
}

func TestReleaseDuplicateAndTehranReset(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "release.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	if _, err = s.Ensure(21); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 16, 19, 0, 0, 0, time.UTC) // 22:30 Tehran
	if err = s.Reserve("same", 21, now); err != nil {
		t.Fatal(err)
	}
	if err = s.Reserve("same", 21, now); !errors.Is(err, ErrDuplicateAnalysis) {
		t.Fatalf("duplicate=%v", err)
	}
	if err = s.SetUse("same", "released"); err != nil {
		t.Fatal(err)
	}
	if err = s.Reserve("replacement", 21, now); err != nil {
		t.Fatalf("released reservation still counted: %v", err)
	}
	if err = s.SetUse("replacement", "consumed"); err != nil {
		t.Fatal(err)
	}
	if err = s.Reserve("next-day", 21, now.Add(2*time.Hour)); err != nil {
		t.Fatalf("Tehran day did not reset: %v", err)
	}
}

func TestAdminAndUserExemptionsDoNotGrantPremiumFeatures(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "exempt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	if _, err = s.Ensure(31); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Ensure(AdminID); err != nil {
		t.Fatal(err)
	}
	if err = s.SetExempt(AdminID, 31, true); err != nil {
		t.Fatal(err)
	}
	a, err := s.Access(31, time.Now())
	if err != nil || !a.Exempt || a.Allowance != -1 || a.Policy.ID != entitlement.Visitor || a.Policy.Features.MultiTimeframeEligible {
		t.Fatalf("exemption leaked premium capability: %+v err=%v", a, err)
	}
	consumeN(t, s, 31, time.Now(), 3)
	if err = s.SetExempt(AdminID, 31, false); err != nil {
		t.Fatal(err)
	}
	a, _ = s.Access(31, time.Now())
	if a.Exempt || a.Allowance != 1 {
		t.Fatalf("revocation failed: %+v", a)
	}
	for i := 0; i < 40; i++ {
		if err = s.Reserve(NewID(), AdminID, time.Now()); err != nil {
			t.Fatalf("admin reservation %d: %v", i, err)
		}
	}
	if err = s.SetExempt(AdminID, AdminID, false); err == nil {
		t.Fatal("admin exemption was revocable")
	}
}

func TestConcurrentStandardReservationsCannotExceedAllowance(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "concurrent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	grantPlan(t, s, 41, entitlement.Standard, time.Now().Add(time.Hour))
	var wg sync.WaitGroup
	results := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- s.Reserve(NewID(), 41, time.Now()) }()
	}
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else if !errors.Is(err, ErrQuota) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if accepted != entitlement.Defaults[entitlement.Standard].DailyAllowance {
		t.Fatalf("accepted=%d", accepted)
	}
}

func TestLegacyPaidMigrationIsIdempotentAndPreservesRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Ensure(51); err != nil {
		t.Fatal(err)
	}
	until := time.Now().UTC().Add(30 * 24 * time.Hour).Format(time.RFC3339)
	if _, err = s.DB.Exec(`INSERT INTO telegram_wallets(telegram_id,derivation_index,address,created_at) VALUES(51,7,'0xlegacy','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`INSERT INTO telegram_invoices(id,telegram_id,address,price_wei,chain_id,created_at,expires_at,status) VALUES('legacy-invoice',51,'0xlegacy','123',56,'2026-01-01T00:00:00Z','2026-01-01T00:15:00Z','paid')`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`INSERT INTO daily_uses(analysis_id,user_id,day_tehran,status,access_kind) VALUES('historical-use',51,'2026-01-01','consumed','free')`); err != nil {
		t.Fatal(err)
	}
	s.DB.Close()
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(`DELETE FROM schema_version WHERE version=3; DROP TABLE entitlements; CREATE TABLE entitlements(user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,exempt INTEGER NOT NULL DEFAULT 0,subscription_until TEXT NOT NULL DEFAULT ''); INSERT INTO entitlements(user_id,exempt,subscription_until) VALUES(51,1,?)`, until); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	raw.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	if err = s.MigrateAccess(); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	var plan, gotUntil string
	var override, exempt int
	if err = s.DB.QueryRow(`SELECT plan_id,daily_allowance_override,subscription_until,exempt FROM entitlements WHERE user_id=51`).Scan(&plan, &override, &gotUntil, &exempt); err != nil {
		t.Fatal(err)
	}
	if plan != "standard" || override != -1 || gotUntil != until || exempt != 1 {
		t.Fatalf("legacy mapping plan=%s override=%d until=%s exempt=%d", plan, override, gotUntil, exempt)
	}
	var wallet, price, status string
	if err = s.DB.QueryRow(`SELECT w.address,i.price_wei,i.status FROM telegram_wallets w JOIN telegram_invoices i ON i.telegram_id=w.telegram_id WHERE w.telegram_id=51`).Scan(&wallet, &price, &status); err != nil {
		t.Fatal(err)
	}
	if wallet != "0xlegacy" || price != "123" || status != "paid" {
		t.Fatalf("historical data changed: %s %s %s", wallet, price, status)
	}
	var historical int
	if err = s.DB.QueryRow(`SELECT count(*) FROM daily_uses WHERE analysis_id='historical-use' AND status='consumed'`).Scan(&historical); err != nil || historical != 1 {
		t.Fatalf("daily usage changed: %d %v", historical, err)
	}
	if _, err = s.DB.Exec(`UPDATE entitlements SET plan_id='Public',daily_allowance_override=0; DELETE FROM schema_version WHERE version=3`); err != nil {
		t.Fatal(err)
	}
	if err = s.MigrateAccess(); err != nil {
		t.Fatal(err)
	}
	if err = s.DB.QueryRow(`SELECT plan_id,daily_allowance_override FROM entitlements WHERE user_id=51`).Scan(&plan, &override); err != nil || plan != "standard" || override != -1 {
		t.Fatalf("Public compatibility plan=%s override=%d err=%v", plan, override, err)
	}
}

func TestActivationRenewalIsIdempotentAndStandard(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "renew.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	if _, err = s.Ensure(61); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err = s.ActivateOnce(61, "charge-1", "bnb", now); err != nil {
		t.Fatal(err)
	}
	_, first, err := s.Subscription(61)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ActivateOnce(61, "charge-1", "bnb", now); err != nil {
		t.Fatal(err)
	}
	_, duplicate, _ := s.Subscription(61)
	if !duplicate.Equal(first) {
		t.Fatal("duplicate payment extended subscription")
	}
	if err = s.ActivateOnce(61, "charge-2", "bnb", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	_, renewed, _ := s.Subscription(61)
	if !renewed.After(first) {
		t.Fatal("distinct payment did not renew")
	}
	a, _ := s.Access(61, now)
	if a.Policy.ID != entitlement.Standard || a.Allowance != 5 {
		t.Fatalf("new paid policy=%+v", a)
	}
	expired, err := s.Access(61, renewed.Add(time.Second))
	if err != nil || expired.SubscriptionActive || expired.Policy.ID != entitlement.Visitor || expired.Allowance != 1 {
		t.Fatalf("expired access=%+v err=%v", expired, err)
	}
}
