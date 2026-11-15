package profile

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestDailyQuotaAndExemption(t *testing.T) {
	s, e := Open(t.TempDir() + "/test.db")
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	if _, e = s.Ensure(11); e != nil {
		t.Fatal(e)
	}
	now := time.Date(2026, 11, 14, 20, 20, 0, 0, time.UTC)
	if e = s.Reserve("a", 11, now); e != nil {
		t.Fatal(e)
	}
	if e = s.SetUse("a", "consumed"); e != nil {
		t.Fatal(e)
	}
	if e = s.Reserve("b", 11, now); !errors.Is(e, ErrQuota) {
		t.Fatalf("want quota, got %v", e)
	}
	if e = s.SetExempt(AdminID, 11, true); e != nil {
		t.Fatal(e)
	}
	if e = s.Reserve("c", 11, now); e != nil {
		t.Fatal(e)
	}
	if e = s.SetExempt(AdminID, 11, false); e != nil {
		t.Fatal(e)
	}
}

func TestAdminAlwaysBypassesDailyQuota(t *testing.T) {
	s, e := Open(t.TempDir() + "/admin.db")
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	if _, e = s.Ensure(AdminID); e != nil {
		t.Fatal(e)
	}
	now := time.Date(2026, 11, 15, 8, 0, 0, 0, time.UTC)
	for _, id := range []string{"admin-first", "admin-second", "admin-third"} {
		if e = s.Reserve(id, AdminID, now); e != nil {
			t.Fatalf("admin reservation %q was limited: %v", id, e)
		}
		if e = s.SetUse(id, "consumed"); e != nil {
			t.Fatal(e)
		}
	}
	users, e := s.AdminUsers(AdminID)
	if e != nil || len(users) != 1 || !users[0].Exempt {
		t.Fatalf("admin exemption not reported: %+v, %v", users, e)
	}
	if e = s.SetExempt(AdminID, AdminID, false); e == nil {
		t.Fatal("permanent admin exemption was revoked")
	}
}

func TestExemptionPersistsAndUnknownUserIsRejected(t *testing.T) {
	path := t.TempDir() + "/persist.db"
	s, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Ensure(42); e != nil {
		t.Fatal(e)
	}
	if e = s.SetExempt(AdminID, 42, true); e != nil {
		t.Fatal(e)
	}
	if e = s.SetExempt(AdminID, 404, true); e == nil {
		t.Fatal("unknown user accepted")
	}
	_ = s.DB.Close()
	s, e = Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	users, e := s.AdminUsers(AdminID)
	if e != nil || len(users) != 1 || !users[0].Exempt {
		t.Fatalf("persisted exemption missing: %+v, %v", users, e)
	}
	if e = s.Reserve("exempt-after-restart", 42, time.Now()); e != nil {
		t.Fatalf("persisted exemption did not grant access: %v", e)
	}
}

func TestConcurrentReserve(t *testing.T) {
	s, e := Open(t.TempDir() + "/test.db")
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	_, _ = s.Ensure(12)
	var wg sync.WaitGroup
	ok := make(chan bool, 2)
	for _, id := range []string{"one", "two"} {
		wg.Add(1)
		go func(id string) { defer wg.Done(); ok <- s.Reserve(id, 12, time.Now()) == nil }(id)
	}
	wg.Wait()
	close(ok)
	n := 0
	for x := range ok {
		if x {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("reserved %d of 2", n)
	}
}

func TestPaymentIdempotencyAndAnalysisLookup(t *testing.T) {
	s, e := Open(t.TempDir() + "/test.db")
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	_, _ = s.Ensure(13)
	now := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		if e = s.ActivateOnce(13, "charge-1", "telegram_stars", now); e != nil {
			t.Fatal(e)
		}
	}
	users, e := s.AdminUsers(AdminID)
	if e != nil {
		t.Fatal(e)
	}
	if len(users) != 1 || users[0].SubscriptionUntil != now.AddDate(0, 1, 0).Format(time.RFC3339) {
		t.Fatalf("unexpected subscription: %+v", users)
	}
	if e = s.CreateRequestState("analysis-1", "13", "13", "XAU/USD", "5min", "scalp"); e != nil {
		t.Fatal(e)
	}
	a, e := s.FindAnalysis(AdminID, "analysis-1")
	if e != nil || a.ID != "analysis-1" || a.UserID != 13 {
		t.Fatalf("lookup: %+v %v", a, e)
	}
}
func TestLegacyTelegramSubscriptionDoesNotBypassFreeQuota(t *testing.T) {
	s, e := Open(t.TempDir() + "/legacy.db")
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	_, _ = s.Ensure(77)
	now := time.Now()
	if e = s.ActivateOnce(77, "legacy-charge", "telegram_stars", now); e != nil {
		t.Fatal(e)
	}
	if e = s.Reserve("first", 77, now); e != nil {
		t.Fatal(e)
	}
	_ = s.SetUse("first", "consumed")
	if e = s.Reserve("second", 77, now); !errors.Is(e, ErrQuota) {
		t.Fatalf("legacy subscription bypassed free quota: %v", e)
	}
}
func TestConfigSwitchAndAudit(t *testing.T) {
	s, e := Open(t.TempDir() + "/a.db")
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	if e = s.SetConfig(AdminID, 2); e != nil {
		t.Fatal(e)
	}
	c, e := s.ActiveConfig()
	if e != nil || c.Version != 2 || c.EngineCandles != 500 || c.ModelCandles != 50 {
		t.Fatalf("config %+v %v", c, e)
	}
	if e = s.SetConfig(AdminID, 1); e != nil {
		t.Fatal(e)
	}
	c, _ = s.ActiveConfig()
	if c.Version != 1 || c.ModelCandles != 500 {
		t.Fatal("rollback failed")
	}
	var n int
	_ = s.DB.QueryRow(`SELECT count(*) FROM admin_audit WHERE action='analysis_config'`).Scan(&n)
	if n != 2 {
		t.Fatalf("audit records=%d", n)
	}
	if e = s.SetConfig(1, 2); e == nil {
		t.Fatal("unauthorized config change")
	}
}
func TestAdminCodeIsOneTime(t *testing.T) {
	s, e := Open(t.TempDir() + "/c.db")
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	if _, e = s.IssueAdminCode(123); e == nil {
		t.Fatal("non-admin issued code")
	}
	code, e := s.IssueAdminCode(AdminID)
	if e != nil {
		t.Fatal(e)
	}
	ok, e := s.ConsumeAdminCode(code)
	if e != nil || !ok {
		t.Fatalf("first consume: %v %v", ok, e)
	}
	ok, e = s.ConsumeAdminCode(code)
	if e != nil || ok {
		t.Fatalf("code reused: %v %v", ok, e)
	}
}
