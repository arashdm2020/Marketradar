package profile

import (
	"database/sql"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"tradebot/internal/custody"
)

func TestLinkedWebIdentityReusesTelegramWalletAndLedger(t *testing.T) {
	t.Setenv("WALLET_KEY_ENCRYPTION_KEY", strings.Repeat("11", 32))
	s, e := Open(t.TempDir() + "/linked.db")
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	_, _ = s.Ensure(5150)
	address, e := s.EnsureCustodialWallet(5150)
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.DB.Exec(`CREATE TABLE web_users(id INTEGER PRIMARY KEY);CREATE TABLE web_telegram_links(telegram_id INTEGER PRIMARY KEY,web_user_id INTEGER UNIQUE);INSERT INTO web_users(id) VALUES(9);INSERT INTO web_telegram_links VALUES(5150,9)`)
	if e != nil {
		t.Fatal(e)
	}
	w, e := s.WalletRecordForWeb(9)
	if e != nil || w.Address != address || w.TelegramID != 5150 {
		t.Fatalf("wallet=%+v err=%v", w, e)
	}
	inv, e := s.CreateTelegramInvoice(5150, address, "100", 56, time.Now().Add(time.Hour), 10)
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.DB.Exec(`CREATE TABLE IF NOT EXISTS web_payment_events(tx_hash TEXT PRIMARY KEY,invoice_id TEXT,address TEXT,amount_wei TEXT,block_time TEXT,classification TEXT,confirmation_count INTEGER);CREATE TABLE IF NOT EXISTS treasury_jobs(payment_hash TEXT PRIMARY KEY,status TEXT);INSERT INTO web_payment_events VALUES('0x1',?,?,'100','2026-01-01T00:00:00Z','paid_telegram',12);UPDATE telegram_invoices SET status='paid',tx_hash='0x1' WHERE id=?;INSERT INTO treasury_jobs VALUES('0x1','queued')`, inv.ID, address, inv.ID)
	if e != nil {
		t.Fatal(e)
	}
	items, total, e := s.WalletTransactions(5150, 10, 0)
	if e != nil || total != 1 || len(items) != 1 || items[0].ReconciliationStatus != "paid_telegram" || items[0].SweepStatus != "queued" {
		t.Fatalf("items=%+v total=%d err=%v", items, total, e)
	}
	_, e = s.WalletRecordForWeb(10)
	if e != sql.ErrNoRows {
		t.Fatalf("unlinked web identity err=%v", e)
	}
}

func TestEnsureWalletIdempotent(t *testing.T) {
	s, e := Open(t.TempDir() + "/wallet_test.db")
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()

	_, e = s.Ensure(42)
	if e != nil {
		t.Fatal(e)
	}

	derive := func(idx uint32) (string, error) {
		return "0x" + hex.EncodeToString([]byte{byte(idx), byte(idx >> 8), byte(idx >> 16), byte(idx >> 24)}), nil
	}

	addr1, e := s.EnsureWallet(42, derive)
	if e != nil {
		t.Fatal(e)
	}
	addr2, e := s.EnsureWallet(42, derive)
	if e != nil {
		t.Fatal(e)
	}
	if addr1 != addr2 {
		t.Fatalf("wallet changed: %s vs %s", addr1, addr2)
	}

	// Different user gets different wallet
	_, _ = s.Ensure(43)
	addr3, e := s.EnsureWallet(43, derive)
	if e != nil {
		t.Fatal(e)
	}
	if addr3 == addr1 {
		t.Fatal("different users share a wallet")
	}

	// Query by address
	wallet, e := s.Wallet(42)
	if e != nil {
		t.Fatal(e)
	}
	if wallet != addr1 {
		t.Fatalf("Wallet() returned %s want %s", wallet, addr1)
	}

	id, e := s.WalletByAddress(addr1)
	if e != nil {
		t.Fatal(e)
	}
	if id != 42 {
		t.Fatalf("WalletByAddress returned %d want 42", id)
	}
}

func TestConcurrentEnsureWallet(t *testing.T) {
	s, e := Open(t.TempDir() + "/concurrent_wallet_test.db")
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()

	_, e = s.Ensure(7)
	if e != nil {
		t.Fatal(e)
	}

	calls := 0
	derive := func(idx uint32) (string, error) {
		calls++
		return "0x" + hex.EncodeToString([]byte{byte(idx), byte(idx >> 8), byte(idx >> 16), byte(idx >> 24)}), nil
	}

	var wg sync.WaitGroup
	results := make(chan string, 50)
	errs := make(chan error, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			addr, err := s.EnsureWallet(7, derive)
			if err != nil {
				errs <- err
				return
			}
			results <- addr
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	var first string
	for addr := range results {
		if first == "" {
			first = addr
		}
		if addr != first {
			t.Fatalf("concurrent provisioning produced different addresses: %s vs %s", first, addr)
		}
	}
	if len(errs) > 0 {
		t.Fatalf("concurrent errors: %v", <-errs)
	}
	if calls > 1 {
		t.Fatalf("derive called %d times, expected 1", calls)
	}
}

func TestConcurrentEnsureCustodialWallet(t *testing.T) {
	t.Setenv("WALLET_KEY_ENCRYPTION_KEY", strings.Repeat("12", 32))
	s, e := Open(t.TempDir() + "/custodial_concurrent.db")
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	_, _ = s.Ensure(8080)
	var wg sync.WaitGroup
	addresses := make(chan string, 30)
	errs := make(chan error, 30)
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, err := s.EnsureCustodialWallet(8080)
			if err != nil {
				errs <- err
				return
			}
			addresses <- a
		}()
	}
	wg.Wait()
	close(addresses)
	close(errs)
	if len(errs) > 0 {
		t.Fatal(<-errs)
	}
	first := ""
	for a := range addresses {
		if first == "" {
			first = a
		}
		if a != first {
			t.Fatal("wallet changed under concurrency")
		}
	}
	var count, signable int
	if e = s.DB.QueryRow(`SELECT count(*),sum(signable) FROM telegram_wallets WHERE telegram_id=8080`).Scan(&count, &signable); e != nil || count != 1 || signable != 1 {
		t.Fatalf("count=%d signable=%d err=%v", count, signable, e)
	}
}

func TestAddressKeyConsistency(t *testing.T) {
	t.Setenv("WALLET_KEY_ENCRYPTION_KEY", strings.Repeat("13", 32))
	s, e := Open(t.TempDir() + "/key_consistency_test.db")
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()

	cs, e := custody.New(s.DB)
	if e != nil {
		t.Fatal(e)
	}
	_, _ = s.Ensure(99)

	addr, e := s.EnsureCustodialWallet(99)
	if e != nil {
		t.Fatal(e)
	}

	retrieved, e := cs.RetrieveKey(addr)
	if e != nil {
		t.Fatal(e)
	}
	if len(retrieved) != 64 {
		t.Fatal("normalized key missing")
	}

	hasKey, e := cs.HasKey(addr)
	if e != nil {
		t.Fatal(e)
	}
	if !hasKey {
		t.Fatal("key not found")
	}

	hasKey, e = cs.HasKey("0x" + hex.EncodeToString(make([]byte, 20)))
	if e != nil {
		t.Fatal(e)
	}
	if hasKey {
		t.Fatal("unexpected key found")
	}
}
