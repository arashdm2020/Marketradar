package profile

import (
	"fmt"
	"testing"
	"time"
)

func loginWithProfile(t *testing.T, s *Store, id int64, first, last, username, photo string) {
	t.Helper()
	now := time.Now().UTC()
	nonce, err := s.IssueTelegramLoginNonce(now)
	if err != nil {
		t.Fatal(err)
	}
	assertion := fmt.Sprintf("%064x", time.Now().UnixNano()+id)
	if _, err = s.TelegramLoginOnce(nonce, assertion, id, first, last, username, photo, now); err != nil {
		t.Fatal(err)
	}
}

func TestTelegramProfilePartialUpdatesPreserveExistingValuesAndIdentity(t *testing.T) {
	s := telegramLoginFixture(t)
	const id int64 = 743211
	if _, err := s.Ensure(id); err != nil {
		t.Fatal(err)
	}
	const address = "0x1111111111111111111111111111111111111111"
	if _, err := s.DB.Exec(`INSERT INTO telegram_wallets(telegram_id,derivation_index,address,created_at) VALUES(?,0,?,?)`, id, address, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	loginWithProfile(t, s, id, "Sara", "Khan", "@sara", "https://t.me/i/userpic/1.jpg")
	loginWithProfile(t, s, id, "Sara", "", "", "not-a-secure-url")
	p, err := s.TelegramProfile(id)
	if err != nil {
		t.Fatal(err)
	}
	if p.TelegramID != id || p.FirstName != "Sara" || p.LastName != "Khan" || p.Username != "sara" || p.PhotoURL != "https://t.me/i/userpic/1.jpg" {
		t.Fatalf("partial login erased valid profile: %+v", p)
	}
	var gotAddress string
	if err = s.DB.QueryRow(`SELECT address FROM telegram_wallets WHERE telegram_id=?`, id).Scan(&gotAddress); err != nil || gotAddress != address {
		t.Fatalf("wallet changed: %q %v", gotAddress, err)
	}
}

func TestUserPhoneRequiresConsentAndRemainsUnverified(t *testing.T) {
	s := telegramLoginFixture(t)
	const id int64 = 743212
	if _, err := s.Ensure(id); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserProvidedPhone(id, "+1 415-555-2671", false); err == nil {
		t.Fatal("phone saved without consent")
	}
	if err := s.SetUserProvidedDisplayName(id, "Sara", false); err == nil {
		t.Fatal("display name saved without consent")
	}
	if err := s.SetUserProvidedPhone(id, "14155552671", true); err == nil {
		t.Fatal("phone without country prefix accepted")
	}
	if err := s.SetUserProvidedPhone(id, "+1 (415) 555-2671", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserProvidedDisplayName(id, "Sara K.", true); err != nil {
		t.Fatal(err)
	}
	p, err := s.TelegramProfile(id)
	if err != nil {
		t.Fatal(err)
	}
	if p.PhoneNumber != "+14155552671" || p.PhoneVerified || p.ProvidedName != "Sara K." || p.DisplayName() != "Sara K." {
		t.Fatalf("manual phone status=%+v", p)
	}
	if err = s.ClearUserProvidedPhone(id); err != nil {
		t.Fatal(err)
	}
	if err = s.ClearUserProvidedDisplayName(id); err != nil {
		t.Fatal(err)
	}
	p, err = s.TelegramProfile(id)
	if err != nil || p.PhoneNumber != "" || p.PhoneVerified || p.ProvidedName != "" {
		t.Fatalf("phone not cleared: %+v err=%v", p, err)
	}
}

func TestProfileMigrationPreservesExistingUserWalletAndSubscriptions(t *testing.T) {
	path := t.TempDir() + "/profile.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	const id int64 = 743213
	if _, err = s.Ensure(id); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`INSERT INTO telegram_wallets(telegram_id,derivation_index,address,created_at) VALUES(?,3,?,?)`, id, "0x2222222222222222222222222222222222222222", time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`INSERT INTO entitlements(user_id,subscription_until,plan_id) VALUES(?,?,'standard')`, id, "2030-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err = s.DB.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	var address, until string
	if err = s.DB.QueryRow(`SELECT w.address,e.subscription_until FROM telegram_wallets w JOIN entitlements e ON e.user_id=w.telegram_id WHERE w.telegram_id=?`, id).Scan(&address, &until); err != nil {
		t.Fatal(err)
	}
	if address != "0x2222222222222222222222222222222222222222" || until != "2030-01-01T00:00:00Z" {
		t.Fatalf("existing records changed: address=%s until=%s", address, until)
	}
}
