package profile

import (
	"encoding/hex"
	"sync"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"tradebot/internal/custody"
)

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
	s, e := Open(t.TempDir() + "/key_consistency_test.db")
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()

	cs, e := custody.New(s.DB)
	if e != nil {
		t.Fatal(e)
	}

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
