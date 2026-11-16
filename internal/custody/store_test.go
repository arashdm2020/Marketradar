package custody

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"github.com/ethereum/go-ethereum/crypto"
	_ "github.com/mattn/go-sqlite3"
	"io"
	"strings"
	"testing"
)

func legacySeal(t *testing.T, key []byte, clear string) []byte {
	t.Helper()
	b, e := aes.NewCipher(key)
	if e != nil {
		t.Fatal(e)
	}
	g, e := cipher.NewGCM(b)
	if e != nil {
		t.Fatal(e)
	}
	nonce := make([]byte, g.NonceSize())
	if _, e = io.ReadFull(rand.Reader, nonce); e != nil {
		t.Fatal(e)
	}
	return g.Seal(nonce, nonce, []byte(clear), nil)
}

func TestLegacyMigrationOnlyAcceptsMatchingKey(t *testing.T) {
	t.Setenv("WALLET_KEY_ENCRYPTION_KEY", strings.Repeat("20", 32))
	db, e := sql.Open("sqlite3", t.TempDir()+"/migrate.db")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	_, e = db.Exec(`CREATE TABLE telegram_wallets(telegram_id INTEGER PRIMARY KEY,derivation_index INTEGER NOT NULL,address TEXT NOT NULL UNIQUE,created_at TEXT NOT NULL);CREATE TABLE custody_keys(address TEXT PRIMARY KEY,private_key_cipher BLOB NOT NULL,created_at TEXT NOT NULL)`)
	if e != nil {
		t.Fatal(e)
	}
	s, e := New(db)
	if e != nil {
		t.Fatal(e)
	}
	key := bytes32(7)
	good, _ := crypto.GenerateKey()
	goodHex := hex.EncodeToString(crypto.FromECDSA(good))
	goodAddr := crypto.PubkeyToAddress(good.PublicKey).Hex()
	bad, _ := crypto.GenerateKey()
	badHex := hex.EncodeToString(crypto.FromECDSA(bad))
	badAddr := "0x" + strings.Repeat("1", 40)
	_, _ = db.Exec(`INSERT INTO telegram_wallets(telegram_id,derivation_index,address,created_at,private_key_hex,signable) VALUES(1,0,?,datetime('now'),'',0),(2,1,?,datetime('now'),'',0)`, goodAddr, badAddr)
	_, _ = db.Exec(`INSERT INTO custody_keys VALUES(?,?,datetime('now')),(?,?,datetime('now'))`, strings.ToLower(goodAddr), legacySeal(t, key, goodHex), strings.ToLower(badAddr), legacySeal(t, key, badHex))
	migrated, unresolved, e := s.MigrateLegacy(hex.EncodeToString(key))
	if e != nil || migrated != 1 || unresolved != 1 {
		t.Fatalf("migrated=%d unresolved=%d err=%v", migrated, unresolved, e)
	}
	if ok, _ := s.HasKey(goodAddr); !ok {
		t.Fatal("matching key not migrated")
	}
	if ok, _ := s.HasKey(badAddr); ok {
		t.Fatal("mismatched wallet made signable")
	}
	var address string
	_ = db.QueryRow(`SELECT address FROM telegram_wallets WHERE telegram_id=2`).Scan(&address)
	if !strings.EqualFold(address, badAddr) {
		t.Fatal("legacy address changed")
	}
}

func TestPlaintextMigrationPreservesAddressAndSigningCompatibility(t *testing.T) {
	t.Setenv("WALLET_KEY_ENCRYPTION_KEY", strings.Repeat("31", 32))
	db, e := sql.Open("sqlite3", t.TempDir()+"/plain.db")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	_, e = db.Exec(`CREATE TABLE telegram_wallets(telegram_id INTEGER PRIMARY KEY,derivation_index INTEGER NOT NULL,address TEXT NOT NULL UNIQUE,created_at TEXT NOT NULL,private_key_hex TEXT NOT NULL DEFAULT '',signable INTEGER NOT NULL DEFAULT 0)`)
	if e != nil {
		t.Fatal(e)
	}
	key, _ := crypto.GenerateKey()
	plain := hex.EncodeToString(crypto.FromECDSA(key))
	address := strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())
	_, _ = db.Exec(`INSERT INTO telegram_wallets VALUES(1,7,?,datetime('now'),?,1)`, address, plain)
	s, e := New(db)
	if e != nil {
		t.Fatal(e)
	}
	got, e := s.RetrieveKey(address)
	if e != nil || got != plain {
		t.Fatalf("retrieved key mismatch: %v", e)
	}
	var stored string
	var version int
	var cipher []byte
	_ = db.QueryRow(`SELECT address,key_encryption_version,private_key_cipher FROM telegram_wallets`).Scan(&stored, &version, &cipher)
	var migrationResult struct{}
	_ = migrationResult
	if stored != address || version != 1 || len(cipher) == 0 {
		t.Fatalf("migration state addr=%s version=%d cipher=%d", stored, version, len(cipher))
	}
	var clear string
	_ = db.QueryRow(`SELECT private_key_hex FROM telegram_wallets`).Scan(&clear)
	if clear != "" {
		t.Fatal("plaintext key was not cleared")
	}
}

func bytes32(v byte) []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = v
	}
	return b
}
