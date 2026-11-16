package bnb

import (
	"context"
	"database/sql"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
	_ "github.com/mattn/go-sqlite3"
	"tradebot/internal/custody"
)

func newTestCustodyStore(t *testing.T) *custody.Store {
	t.Setenv("WALLET_KEY_ENCRYPTION_KEY", strings.Repeat("19", 32))
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`CREATE TABLE telegram_wallets(telegram_id INTEGER PRIMARY KEY,derivation_index INTEGER NOT NULL,address TEXT NOT NULL UNIQUE,created_at TEXT NOT NULL);CREATE TABLE custody_keys(address TEXT PRIMARY KEY,private_key_cipher BLOB NOT NULL,created_at TEXT NOT NULL)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	store, err := custody.New(db)
	if err != nil {
		t.Fatalf("new custody: %v", err)
	}
	return store
}

func TestCustodialSigner_SignAndRecover(t *testing.T) {
	store := newTestCustodyStore(t)
	privKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := crypto.PubkeyToAddress(privKey.PublicKey)
	_, _ = store.DB.Exec(`INSERT INTO telegram_wallets(telegram_id,derivation_index,address,created_at) VALUES(1,0,?,datetime('now'))`, addr.Hex())
	err = store.StoreKey(addr.Hex(), hex.EncodeToString(crypto.FromECDSA(privKey)))
	if err != nil {
		t.Fatalf("store key: %v", err)
	}

	signer := NewCustodialSigner(store)
	signed, err := signer.Sign(context.Background(), SignRequest{
		PaymentHash:        "test-payment",
		Source:             addr.Hex(),
		Destination:        "0x0F65096f6C02aA6DC0d0b8c99dDeBbc3CD90B3F5",
		DestinationVersion: 1,
		Nonce:              0,
		ChainID:            56,
		AmountWei:          "1000000000000000000",
		GasPriceWei:        "20000000000",
		GasLimit:           21000,
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if signed.Raw == "" {
		t.Fatal("empty raw")
	}
	if signed.Hash == "" {
		t.Fatal("empty hash")
	}
}

func TestCustodialSigner_InvalidKey(t *testing.T) {
	store := newTestCustodyStore(t)
	signer := NewCustodialSigner(store)
	_, err := signer.Sign(context.Background(), SignRequest{
		Source:             "0x0F65096f6C02aA6DC0d0b8c99dDeBbc3CD90B3F5",
		Destination:        "0x0F65096f6C02aA6DC0d0b8c99dDeBbc3CD90B3F5",
		DestinationVersion: 1,
		Nonce:              0,
		ChainID:            56,
		AmountWei:          "1000000000000000000",
		GasPriceWei:        "20000000000",
		GasLimit:           21000,
	})
	if err == nil {
		t.Fatal("expected error for missing key")
	}
}

func TestCustodialSignerRejectsUnrelatedSource(t *testing.T) {
	store := newTestCustodyStore(t)
	key, e := crypto.GenerateKey()
	if e != nil {
		t.Fatal(e)
	}
	addr := crypto.PubkeyToAddress(key.PublicKey)
	_, _ = store.DB.Exec(`INSERT INTO telegram_wallets(telegram_id,derivation_index,address,created_at) VALUES(1,0,?,datetime('now'))`, addr.Hex())
	if e = store.StoreKey(addr.Hex(), hex.EncodeToString(crypto.FromECDSA(key))); e != nil {
		t.Fatal(e)
	}
	_, e = NewCustodialSigner(store).Sign(context.Background(), SignRequest{Source: "0x" + strings.Repeat("2", 40), Destination: "0x0F65096f6C02aA6DC0d0b8c99dDeBbc3CD90B3F5", ChainID: 56, AmountWei: "1", GasPriceWei: "1", GasLimit: 21000})
	if e == nil {
		t.Fatal("unrelated source accepted")
	}
}

func TestCustodialSigner_WrongChainID(t *testing.T) {
	store := newTestCustodyStore(t)
	privKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := crypto.PubkeyToAddress(privKey.PublicKey)
	_, _ = store.DB.Exec(`INSERT INTO telegram_wallets(telegram_id,derivation_index,address,created_at) VALUES(1,0,?,datetime('now'))`, addr.Hex())
	err = store.StoreKey(addr.Hex(), hex.EncodeToString(crypto.FromECDSA(privKey)))
	if err != nil {
		t.Fatalf("store key: %v", err)
	}

	signer := NewCustodialSigner(store)
	signed, err := signer.Sign(context.Background(), SignRequest{
		Source:             addr.Hex(),
		Destination:        "0x0F65096f6C02aA6DC0d0b8c99dDeBbc3CD90B3F5",
		DestinationVersion: 1,
		Nonce:              0,
		ChainID:            1,
		AmountWei:          "1000000000000000000",
		GasPriceWei:        "20000000000",
		GasLimit:           21000,
	})
	if err == nil || signed.Raw != "" {
		t.Fatal("wrong chain ID accepted")
	}
}

func TestAddressKeyConsistency(t *testing.T) {
	store := newTestCustodyStore(t)
	privKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := crypto.PubkeyToAddress(privKey.PublicKey)
	_, _ = store.DB.Exec(`INSERT INTO telegram_wallets(telegram_id,derivation_index,address,created_at) VALUES(1,0,?,datetime('now'))`, addr.Hex())
	privKeyHex := hex.EncodeToString(crypto.FromECDSA(privKey))
	err = store.StoreKey(addr.Hex(), privKeyHex)
	if err != nil {
		t.Fatalf("store key: %v", err)
	}

	retrieved, err := store.RetrieveKey(addr.Hex())
	if err != nil {
		t.Fatalf("retrieve key: %v", err)
	}
	if retrieved != privKeyHex {
		t.Fatalf("key mismatch: got %s want %s", retrieved, privKeyHex)
	}

	hasKey, err := store.HasKey(addr.Hex())
	if err != nil {
		t.Fatalf("has key: %v", err)
	}
	if !hasKey {
		t.Fatal("expected key to exist")
	}
}
