package custody

import (
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
)

// Store reads wallet-specific keys from telegram_wallets. It retains no
// application-level encryption key and never returns keys through list APIs.
type Store struct{ DB *sql.DB }

func New(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("database unavailable")
	}
	for _, q := range []string{`ALTER TABLE telegram_wallets ADD COLUMN private_key_hex TEXT NOT NULL DEFAULT ''`, `ALTER TABLE telegram_wallets ADD COLUMN signable INTEGER NOT NULL DEFAULT 0`} {
		if _, e := db.Exec(q); e != nil && !strings.Contains(strings.ToLower(e.Error()), "duplicate column") {
			return nil, e
		}
	}
	return &Store{DB: db}, nil
}

func normalizeKey(raw string) (string, string, error) {
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "0x")
	if len(raw) != 64 {
		return "", "", errors.New("invalid private key")
	}
	key, e := crypto.HexToECDSA(raw)
	if e != nil {
		return "", "", errors.New("invalid private key")
	}
	return hex.EncodeToString(crypto.FromECDSA(key)), strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex()), nil
}

func (s *Store) StoreKey(address, privateKey string) error {
	normalized, derived, e := normalizeKey(privateKey)
	if e != nil {
		return e
	}
	if !strings.EqualFold(strings.TrimSpace(address), derived) {
		return errors.New("private key does not match wallet address")
	}
	r, e := s.DB.Exec(`UPDATE telegram_wallets SET private_key_hex=?,signable=1 WHERE lower(address)=lower(?) AND (private_key_hex='' OR lower(private_key_hex)=lower(?))`, normalized, address, normalized)
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return errors.New("wallet unavailable or key conflict")
	}
	return nil
}

func (s *Store) RetrieveKey(address string) (string, error) {
	var raw string
	var signable int
	e := s.DB.QueryRow(`SELECT private_key_hex,signable FROM telegram_wallets WHERE lower(address)=lower(?)`, address).Scan(&raw, &signable)
	if e != nil {
		return "", e
	}
	if signable != 1 || raw == "" {
		return "", errors.New("wallet is non-signable")
	}
	normalized, derived, e := normalizeKey(raw)
	if e != nil || !strings.EqualFold(derived, address) {
		return "", errors.New("wallet key/address mismatch")
	}
	return normalized, nil
}

func (s *Store) HasKey(address string) (bool, error) {
	var n int
	e := s.DB.QueryRow(`SELECT count(*) FROM telegram_wallets WHERE lower(address)=lower(?) AND private_key_hex!='' AND signable=1`, address).Scan(&n)
	return n == 1, e
}

func GeneratePrivateKey() (string, error) {
	key, e := crypto.GenerateKey()
	if e != nil {
		return "", e
	}
	return hex.EncodeToString(crypto.FromECDSA(key)), nil
}

// MigrateLegacy decrypts only matching legacy custody records. Mismatches and
// undecryptable records remain untouched and their wallets stay non-signable.
func (s *Store) MigrateLegacy(hexKey string) (migrated, unresolved int, err error) {
	key, e := hex.DecodeString(strings.TrimSpace(hexKey))
	if e != nil || len(key) != 32 {
		return 0, 0, errors.New("legacy custody key invalid")
	}
	rows, e := s.DB.Query(`SELECT w.address,k.private_key_cipher FROM telegram_wallets w JOIN custody_keys k ON lower(k.address)=lower(w.address) WHERE w.private_key_hex=''`)
	if e != nil {
		if strings.Contains(strings.ToLower(e.Error()), "no such table") {
			return 0, 0, nil
		}
		return 0, 0, e
	}
	type item struct {
		address string
		cipher  []byte
	}
	var items []item
	for rows.Next() {
		var x item
		if e = rows.Scan(&x.address, &x.cipher); e != nil {
			rows.Close()
			return migrated, unresolved, e
		}
		items = append(items, x)
	}
	if e = rows.Err(); e != nil {
		rows.Close()
		return migrated, unresolved, e
	}
	rows.Close()
	block, e := aes.NewCipher(key)
	if e != nil {
		return 0, 0, e
	}
	gcm, e := cipher.NewGCM(block)
	if e != nil {
		return 0, 0, e
	}
	for _, x := range items {
		if len(x.cipher) < gcm.NonceSize() {
			unresolved++
			continue
		}
		raw, de := gcm.Open(nil, x.cipher[:gcm.NonceSize()], x.cipher[gcm.NonceSize():], nil)
		if de != nil {
			unresolved++
			continue
		}
		if se := s.StoreKey(x.address, string(raw)); se != nil {
			unresolved++
			continue
		}
		migrated++
	}
	return migrated, unresolved, nil
}
