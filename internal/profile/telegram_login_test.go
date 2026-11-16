package profile

import (
	"sync"
	"testing"
	"time"
)

func telegramLoginFixture(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/login.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.DB.Close() })
	if _, err = s.DB.Exec(`CREATE TABLE web_users(id INTEGER PRIMARY KEY,email TEXT NOT NULL UNIQUE,password_hash BLOB NOT NULL,created_at TEXT NOT NULL,telegram_id INTEGER UNIQUE,first_name TEXT NOT NULL DEFAULT '',username TEXT NOT NULL DEFAULT '');CREATE TABLE web_telegram_links(telegram_id INTEGER PRIMARY KEY,web_user_id INTEGER NOT NULL UNIQUE,linked_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTelegramLoginMapsOnlyExistingIdentityOnce(t *testing.T) {
	s := telegramLoginFixture(t)
	const tg int64 = 554433
	if _, err := s.Ensure(tg); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	assertion := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	nonce, err := s.IssueTelegramLoginNonce(now)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.TelegramLoginOnce(nonce, assertion, tg, "Test", "", "tester", "", now)
	if err != nil {
		t.Fatal(err)
	}
	var linked int64
	if err = s.DB.QueryRow(`SELECT web_user_id FROM web_telegram_links WHERE telegram_id=?`, tg).Scan(&linked); err != nil || linked != id {
		t.Fatalf("linked=%d id=%d err=%v", linked, id, err)
	}
	if _, err = s.TelegramLoginOnce(nonce, assertion, tg, "Test", "", "tester", "", now); err == nil {
		t.Fatal("replay accepted")
	}
	secondNonce, err := s.IssueTelegramLoginNonce(now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.TelegramLoginOnce(secondNonce, assertion, tg, "Test", "", "tester", "", now); err == nil {
		t.Fatal("same signed Telegram assertion replayed with fresh state")
	}
	var n int
	if err = s.DB.QueryRow(`SELECT count(*) FROM web_users WHERE telegram_id=?`, tg).Scan(&n); err != nil || n != 1 {
		t.Fatalf("web identities=%d err=%v", n, err)
	}
	secondState, err := s.IssueTelegramLoginNonce(now)
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := s.TelegramLoginOnce(secondState, "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789", tg, "Test", "", "tester", "", now)
	if err != nil || secondID != id {
		t.Fatalf("Telegram identity was not reused: id=%d want=%d err=%v", secondID, id, err)
	}
}

func TestTelegramLoginRejectsUnknownTelegramIdentity(t *testing.T) {
	s := telegramLoginFixture(t)
	nonce, err := s.IssueTelegramLoginNonce(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.TelegramLoginOnce(nonce, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", 998877, "Unknown", "", "", "", time.Now()); err == nil {
		t.Fatal("unknown Telegram identity accepted")
	}
}

func TestTelegramLoginStateExpiresAndDoesNotConsumeQuotaOrWallet(t *testing.T) {
	s := telegramLoginFixture(t)
	const tg int64 = 554435
	if _, err := s.Ensure(tg); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	nonce, err := s.IssueTelegramLoginNonce(now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.TelegramLoginOnce(nonce, "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789", tg, "Test", "", "", "", now.Add(6*time.Minute)); err == nil {
		t.Fatal("expired login state accepted")
	}
	var uses, wallets, webUsers int
	_ = s.DB.QueryRow(`SELECT count(*) FROM daily_uses WHERE user_id=?`, tg).Scan(&uses)
	_ = s.DB.QueryRow(`SELECT count(*) FROM telegram_wallets WHERE telegram_id=?`, tg).Scan(&wallets)
	_ = s.DB.QueryRow(`SELECT count(*) FROM web_users WHERE telegram_id=?`, tg).Scan(&webUsers)
	if uses != 0 || wallets != 0 || webUsers != 0 {
		t.Fatalf("login side effect: uses=%d wallets=%d web_users=%d", uses, wallets, webUsers)
	}
}

func TestTelegramLoginConcurrentReplayHasOneWinner(t *testing.T) {
	s := telegramLoginFixture(t)
	const tg int64 = 554434
	if _, err := s.Ensure(tg); err != nil {
		t.Fatal(err)
	}
	nonce, err := s.IssueTelegramLoginNonce(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	const assertion = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.TelegramLoginOnce(nonce, assertion, tg, "Test", "", "", "", time.Now()); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("successful concurrent logins=%d, want 1", wins)
	}
}
