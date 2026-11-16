package custody

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
)

type Store struct {
	DB  *sql.DB
	key []byte
}

func New(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("database unavailable")
	}
	if _, e := db.Exec(`CREATE TABLE IF NOT EXISTS schema_version(version INTEGER PRIMARY KEY)`); e != nil {
		return nil, e
	}
	for _, q := range []string{`ALTER TABLE telegram_wallets ADD COLUMN private_key_hex TEXT NOT NULL DEFAULT ''`, `ALTER TABLE telegram_wallets ADD COLUMN signable INTEGER NOT NULL DEFAULT 0`, `ALTER TABLE telegram_wallets ADD COLUMN private_key_cipher BLOB NOT NULL DEFAULT X''`, `ALTER TABLE telegram_wallets ADD COLUMN key_encryption_version INTEGER NOT NULL DEFAULT 0`} {
		if _, e := db.Exec(q); e != nil && !strings.Contains(strings.ToLower(e.Error()), "duplicate column") {
			return nil, e
		}
	}
	raw := strings.TrimSpace(os.Getenv("WALLET_KEY_ENCRYPTION_KEY"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("AI_CONFIG_MASTER_KEY"))
	}
	s := &Store{DB: db}
	if raw != "" {
		key, e := hex.DecodeString(raw)
		if e != nil || len(key) != 32 {
			return nil, errors.New("wallet encryption key must be 32-byte hex")
		}
		s.key = key
		if _, _, e = s.MigratePlaintext(); e != nil {
			return nil, e
		}
	}
	return s, nil
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
func (s *Store) ready() error {
	if len(s.key) != 32 {
		return errors.New("wallet encryption key unavailable")
	}
	return nil
}
func (s *Store) seal(address, clear string) ([]byte, error) {
	if e := s.ready(); e != nil {
		return nil, e
	}
	b, e := aes.NewCipher(s.key)
	if e != nil {
		return nil, e
	}
	g, e := cipher.NewGCM(b)
	if e != nil {
		return nil, e
	}
	nonce := make([]byte, g.NonceSize())
	if _, e = io.ReadFull(rand.Reader, nonce); e != nil {
		return nil, e
	}
	return g.Seal(nonce, nonce, []byte(clear), []byte(strings.ToLower(address))), nil
}
func (s *Store) open(address string, sealed []byte) (string, error) {
	if e := s.ready(); e != nil {
		return "", e
	}
	b, e := aes.NewCipher(s.key)
	if e != nil {
		return "", e
	}
	g, e := cipher.NewGCM(b)
	if e != nil {
		return "", e
	}
	if len(sealed) < g.NonceSize() {
		return "", errors.New("invalid encrypted wallet key")
	}
	raw, e := g.Open(nil, sealed[:g.NonceSize()], sealed[g.NonceSize():], []byte(strings.ToLower(address)))
	if e != nil {
		return "", errors.New("wallet key decryption failed")
	}
	return string(raw), nil
}
func (s *Store) StoreKey(address, privateKey string) error {
	normalized, derived, e := normalizeKey(privateKey)
	if e != nil {
		return e
	}
	if !strings.EqualFold(address, derived) {
		return errors.New("private key does not match wallet address")
	}
	sealed, e := s.seal(derived, normalized)
	if e != nil {
		return e
	}
	r, e := s.DB.Exec(`UPDATE telegram_wallets SET private_key_cipher=?,key_encryption_version=1,private_key_hex='',signable=1 WHERE lower(address)=lower(?) AND length(private_key_cipher)=0`, sealed, address)
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
	var sealed []byte
	var version, signable int
	var plain string
	e := s.DB.QueryRow(`SELECT private_key_cipher,key_encryption_version,signable,private_key_hex FROM telegram_wallets WHERE lower(address)=lower(?)`, address).Scan(&sealed, &version, &signable, &plain)
	if e != nil {
		return "", e
	}
	if signable != 1 {
		return "", errors.New("wallet is non-signable")
	}
	if version != 1 || len(sealed) == 0 {
		if plain != "" {
			return "", errors.New("plaintext wallet key requires migration")
		}
		return "", errors.New("wallet is non-signable")
	}
	raw, e := s.open(address, sealed)
	if e != nil {
		return "", e
	}
	normalized, derived, e := normalizeKey(raw)
	if e != nil || !strings.EqualFold(derived, address) {
		return "", errors.New("wallet key/address mismatch")
	}
	return normalized, nil
}
func (s *Store) HasKey(address string) (bool, error) {
	_, e := s.RetrieveKey(address)
	if e == nil {
		return true, nil
	}
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	return false, nil
}
func (s *Store) EnsureWallet(telegramID int64) (string, error) {
	if e := s.ready(); e != nil {
		return "", e
	}
	var address string
	if e := s.DB.QueryRow(`SELECT address FROM telegram_wallets WHERE telegram_id=?`, telegramID).Scan(&address); e == nil {
		return address, nil
	} else if e != sql.ErrNoRows {
		return "", e
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return "", e
	}
	defer tx.Rollback()
	var verified int
	if e = tx.QueryRow(`SELECT count(*) FROM users WHERE id=?`, telegramID).Scan(&verified); e != nil || verified != 1 {
		return "", errors.New("verified Telegram user required")
	}
	if e = tx.QueryRow(`SELECT address FROM telegram_wallets WHERE telegram_id=?`, telegramID).Scan(&address); e == nil {
		return address, tx.Commit()
	} else if e != sql.ErrNoRows {
		return "", e
	}
	var next int64
	if e = tx.QueryRow(`SELECT next_index FROM telegram_wallet_sequence WHERE id=1`).Scan(&next); e != nil {
		return "", e
	}
	key, e := crypto.GenerateKey()
	if e != nil {
		return "", e
	}
	plain := hex.EncodeToString(crypto.FromECDSA(key))
	address = strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())
	sealed, e := s.seal(address, plain)
	if e != nil {
		return "", e
	}
	if _, e = tx.Exec(`INSERT INTO telegram_wallets(telegram_id,derivation_index,address,created_at,private_key_hex,private_key_cipher,key_encryption_version,signable) VALUES(?,?,?,?, '',?,1,1)`, telegramID, next, address, time.Now().UTC().Format(time.RFC3339), sealed); e != nil {
		return "", e
	}
	if _, e = tx.Exec(`UPDATE telegram_wallet_sequence SET next_index=? WHERE id=1 AND next_index=?`, next+1, next); e != nil {
		return "", e
	}
	if e = tx.Commit(); e != nil {
		return "", e
	}
	return address, nil
}
func (s *Store) MigratePlaintext() (migrated, unresolved int, err error) {
	if e := s.ready(); e != nil {
		return 0, 0, e
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return 0, 0, e
	}
	defer tx.Rollback()
	rows, e := tx.Query(`SELECT address,private_key_hex FROM telegram_wallets WHERE private_key_hex!='' AND length(private_key_cipher)=0`)
	if e != nil {
		return 0, 0, e
	}
	type item struct{ a, k string }
	var items []item
	for rows.Next() {
		var x item
		if e = rows.Scan(&x.a, &x.k); e != nil {
			rows.Close()
			return 0, 0, e
		}
		items = append(items, x)
	}
	if e = rows.Close(); e != nil {
		return 0, 0, e
	}
	for _, x := range items {
		normalized, derived, ne := normalizeKey(x.k)
		if ne != nil || !strings.EqualFold(derived, x.a) {
			unresolved++
			continue
		}
		sealed, se := s.seal(x.a, normalized)
		if se != nil {
			return migrated, unresolved, se
		}
		clear, oe := s.open(x.a, sealed)
		if oe != nil || clear != normalized {
			return migrated, unresolved, errors.New("wallet encryption verification failed")
		}
		res, ue := tx.Exec(`UPDATE telegram_wallets SET private_key_cipher=?,key_encryption_version=1,private_key_hex='',signable=1 WHERE lower(address)=lower(?) AND private_key_hex=? AND length(private_key_cipher)=0`, sealed, x.a, x.k)
		if ue != nil {
			return migrated, unresolved, ue
		}
		n, _ := res.RowsAffected()
		if n == 1 {
			migrated++
		}
	}
	if _, e = tx.Exec(`INSERT OR IGNORE INTO schema_version(version) VALUES(9)`); e != nil {
		return migrated, unresolved, e
	}
	return migrated, unresolved, tx.Commit()
}
func (s *Store) MigrateLegacy(hexKey string) (migrated, unresolved int, err error) {
	key, e := hex.DecodeString(strings.TrimSpace(hexKey))
	if e != nil || len(key) != 32 {
		return 0, 0, errors.New("legacy custody key invalid")
	}
	rows, e := s.DB.Query(`SELECT w.address,k.private_key_cipher FROM telegram_wallets w JOIN custody_keys k ON lower(k.address)=lower(w.address) WHERE length(w.private_key_cipher)=0`)
	if e != nil {
		if strings.Contains(strings.ToLower(e.Error()), "no such table") {
			return 0, 0, nil
		}
		return 0, 0, e
	}
	type item struct {
		a string
		c []byte
	}
	var items []item
	for rows.Next() {
		var x item
		if e = rows.Scan(&x.a, &x.c); e != nil {
			rows.Close()
			return migrated, unresolved, e
		}
		items = append(items, x)
	}
	rows.Close()
	b, _ := aes.NewCipher(key)
	g, _ := cipher.NewGCM(b)
	for _, x := range items {
		if len(x.c) < g.NonceSize() {
			unresolved++
			continue
		}
		raw, de := g.Open(nil, x.c[:g.NonceSize()], x.c[g.NonceSize():], nil)
		if de != nil || s.StoreKey(x.a, string(raw)) != nil {
			unresolved++
			continue
		}
		migrated++
	}
	return migrated, unresolved, nil
}
