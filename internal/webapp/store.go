package webapp

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"golang.org/x/crypto/bcrypt"
	"math/big"
	"strings"
	"time"
	"tradebot/internal/profile"
)

type Store struct{ DB *sql.DB }

func NewStore(db *sql.DB) (*Store, error) {
	s := &Store{DB: db}
	_, e := db.Exec(`CREATE TABLE IF NOT EXISTS web_users(id INTEGER PRIMARY KEY,email TEXT NOT NULL UNIQUE,password_hash BLOB NOT NULL,subscription_until TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL);CREATE TABLE IF NOT EXISTS web_sessions(token_hash TEXT PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES web_users(id),expires_at TEXT NOT NULL);CREATE TABLE IF NOT EXISTS wallet_sequence(id INTEGER PRIMARY KEY CHECK(id=1),next_index INTEGER NOT NULL);INSERT OR IGNORE INTO wallet_sequence VALUES(1,0);CREATE TABLE IF NOT EXISTS web_wallets(user_id INTEGER PRIMARY KEY REFERENCES web_users(id),derivation_index INTEGER NOT NULL UNIQUE,address TEXT NOT NULL UNIQUE);CREATE TABLE IF NOT EXISTS web_plan(id INTEGER PRIMARY KEY CHECK(id=1),price_wei TEXT NOT NULL DEFAULT '');INSERT OR IGNORE INTO web_plan VALUES(1,'');CREATE TABLE IF NOT EXISTS web_invoices(id TEXT PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES web_users(id),address TEXT NOT NULL,price_wei TEXT NOT NULL,chain_id INTEGER NOT NULL,created_at TEXT NOT NULL,expires_at TEXT NOT NULL,start_block INTEGER NOT NULL,status TEXT NOT NULL,tx_hash TEXT NOT NULL DEFAULT '');CREATE INDEX IF NOT EXISTS web_invoices_user ON web_invoices(user_id,status);CREATE UNIQUE INDEX IF NOT EXISTS web_one_pending ON web_invoices(user_id) WHERE status='pending';CREATE TABLE IF NOT EXISTS web_payment_events(tx_hash TEXT PRIMARY KEY,invoice_id TEXT NOT NULL DEFAULT '',address TEXT NOT NULL,amount_wei TEXT NOT NULL,block_number INTEGER NOT NULL,block_time TEXT NOT NULL,classification TEXT NOT NULL,seen_at TEXT NOT NULL);CREATE TABLE IF NOT EXISTS web_chain_scan(id INTEGER PRIMARY KEY CHECK(id=1),cursor INTEGER NOT NULL);CREATE TABLE IF NOT EXISTS web_analyses(id TEXT PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES web_users(id),symbol TEXT NOT NULL,timeframe TEXT NOT NULL,style TEXT NOT NULL,status TEXT NOT NULL,config_version INTEGER NOT NULL,engine_candles INTEGER NOT NULL DEFAULT 0,model_candles INTEGER NOT NULL DEFAULT 0,model TEXT NOT NULL DEFAULT '',prompt_version TEXT NOT NULL DEFAULT '',result_json TEXT NOT NULL DEFAULT '',error TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,updated_at TEXT NOT NULL);CREATE UNIQUE INDEX IF NOT EXISTS web_one_running ON web_analyses(user_id) WHERE status IN ('queued','running');`)
	if e == nil {
		if _, alterErr := db.Exec(`ALTER TABLE web_payment_events ADD COLUMN duplicate_count INTEGER NOT NULL DEFAULT 0`); alterErr != nil && !strings.Contains(alterErr.Error(), "duplicate column name") {
			e = alterErr
		}
	}
	if e == nil {
		_, e = db.Exec(`CREATE TABLE IF NOT EXISTS web_wallet_root(id INTEGER PRIMARY KEY CHECK(id=1),fingerprint TEXT NOT NULL)`)
	}
	if e == nil {
		_, e = db.Exec(`CREATE TABLE IF NOT EXISTS web_link_tokens(token_hash TEXT PRIMARY KEY,telegram_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,expires_at TEXT NOT NULL,used INTEGER NOT NULL DEFAULT 0);CREATE TABLE IF NOT EXISTS web_telegram_links(telegram_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,web_user_id INTEGER NOT NULL UNIQUE REFERENCES web_users(id),linked_at TEXT NOT NULL)`)
	}
	if e == nil {
		if _, alterErr := db.Exec(`ALTER TABLE web_analyses ADD COLUMN horizon_candles INTEGER NOT NULL DEFAULT 12`); alterErr != nil && !strings.Contains(alterErr.Error(), "duplicate column name") {
			e = alterErr
		}
	}
	if e == nil {
		_, e = db.Exec(`CREATE TABLE IF NOT EXISTS treasury_destinations(version INTEGER PRIMARY KEY AUTOINCREMENT,address TEXT NOT NULL,active INTEGER NOT NULL DEFAULT 0,admin_id INTEGER NOT NULL,at_utc TEXT NOT NULL);CREATE UNIQUE INDEX IF NOT EXISTS treasury_one_active ON treasury_destinations(active) WHERE active=1;CREATE TABLE IF NOT EXISTS treasury_jobs(payment_hash TEXT PRIMARY KEY REFERENCES web_payment_events(tx_hash),source TEXT NOT NULL,amount_wei TEXT NOT NULL,destination_version INTEGER NOT NULL DEFAULT 0,status TEXT NOT NULL,nonce INTEGER,tx_hash TEXT NOT NULL DEFAULT '',raw_tx TEXT NOT NULL DEFAULT '',fee_wei TEXT NOT NULL DEFAULT '',updated_at TEXT NOT NULL);CREATE UNIQUE INDEX IF NOT EXISTS treasury_source_nonce ON treasury_jobs(source,nonce) WHERE nonce IS NOT NULL`)
	}
	return s, e
}
func (s *Store) PinWalletRoot(xpub string) error {
	digest := sha256.Sum256([]byte(strings.TrimSpace(xpub)))
	fingerprint := hex.EncodeToString(digest[:])
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var saved string
	e = tx.QueryRow(`SELECT fingerprint FROM web_wallet_root WHERE id=1`).Scan(&saved)
	if e == sql.ErrNoRows {
		var wallets int
		if e = tx.QueryRow(`SELECT count(*) FROM web_wallets`).Scan(&wallets); e != nil {
			return e
		}
		if wallets > 0 {
			return errors.New("wallet root unpinned with existing addresses")
		}
		if _, e = tx.Exec(`INSERT INTO web_wallet_root(id,fingerprint) VALUES(1,?)`, fingerprint); e != nil {
			return e
		}
	} else if e != nil {
		return e
	} else if saved != fingerprint {
		return errors.New("wallet root changed")
	}
	return tx.Commit()
}
func normalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }
func (s *Store) Register(email, password string) (int64, error) {
	email = normalizeEmail(email)
	if len(email) < 5 || len(email) > 254 || !strings.Contains(email, "@") || len(password) < 12 || len(password) > 128 {
		return 0, errors.New("invalid registration")
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if e != nil {
		return 0, e
	}
	r, e := s.DB.Exec(`INSERT INTO web_users(email,password_hash,created_at) VALUES(?,?,?)`, email, hash, time.Now().UTC().Format(time.RFC3339))
	if e != nil {
		return 0, errors.New("registration unavailable")
	}
	return r.LastInsertId()
}
func (s *Store) Login(email, password string) (string, error) {
	var id int64
	var hash []byte
	e := s.DB.QueryRow(`SELECT id,password_hash FROM web_users WHERE email=?`, normalizeEmail(email)).Scan(&id, &hash)
	if e != nil || bcrypt.CompareHashAndPassword(hash, []byte(password)) != nil {
		return "", errors.New("invalid credentials")
	}
	b := make([]byte, 32)
	if _, e = rand.Read(b); e != nil {
		return "", e
	}
	token := hex.EncodeToString(b)
	digest := sha256.Sum256([]byte(token))
	_, e = s.DB.Exec(`INSERT INTO web_sessions(token_hash,user_id,expires_at) VALUES(?,?,?)`, hex.EncodeToString(digest[:]), id, time.Now().UTC().Add(12*time.Hour).Format(time.RFC3339))
	return token, e
}
func (s *Store) Session(token string) (int64, error) {
	digest := sha256.Sum256([]byte(token))
	var id int64
	e := s.DB.QueryRow(`SELECT user_id FROM web_sessions WHERE token_hash=? AND expires_at>?`, hex.EncodeToString(digest[:]), time.Now().UTC().Format(time.RFC3339)).Scan(&id)
	return id, e
}
func (s *Store) Logout(token string) error {
	digest := sha256.Sum256([]byte(token))
	_, e := s.DB.Exec(`DELETE FROM web_sessions WHERE token_hash=?`, hex.EncodeToString(digest[:]))
	return e
}
func (s *Store) Subscription(user int64) (time.Time, error) {
	var raw string
	e := s.DB.QueryRow(`SELECT subscription_until FROM web_users WHERE id=?`, user).Scan(&raw)
	if e != nil {
		return time.Time{}, e
	}
	v, _ := time.Parse(time.RFC3339, raw)
	return v, nil
}
func (s *Store) Price() (*big.Int, error) {
	var raw string
	e := s.DB.QueryRow(`SELECT price_wei FROM web_plan WHERE id=1`).Scan(&raw)
	if e != nil {
		return nil, e
	}
	if raw == "" {
		return nil, errors.New("plan price not configured")
	}
	n, ok := new(big.Int).SetString(raw, 10)
	if !ok || n.Sign() <= 0 {
		return nil, errors.New("invalid plan price")
	}
	return n, nil
}
func ParseBNB(v string) (*big.Int, error) {
	v = strings.TrimSpace(v)
	p := strings.Split(v, ".")
	if len(p) > 2 || len(p[0]) == 0 {
		return nil, errors.New("invalid BNB amount")
	}
	whole, ok := new(big.Int).SetString(p[0], 10)
	if !ok || whole.Sign() < 0 {
		return nil, errors.New("invalid BNB amount")
	}
	frac := ""
	if len(p) == 2 {
		frac = p[1]
		if len(frac) > 18 {
			return nil, errors.New("too many decimals")
		}
		for _, c := range frac {
			if c < '0' || c > '9' {
				return nil, errors.New("invalid BNB amount")
			}
		}
	}
	whole.Mul(whole, new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	if frac != "" {
		fraction, _ := new(big.Int).SetString(frac+strings.Repeat("0", 18-len(frac)), 10)
		whole.Add(whole, fraction)
	}
	if whole.Sign() <= 0 || whole.BitLen() > 128 {
		return nil, errors.New("amount out of range")
	}
	return whole, nil
}
func (s *Store) SetPrice(admin int64, bnb string) error {
	if admin != profile.AdminID {
		return errors.New("forbidden")
	}
	price, e := ParseBNB(bnb)
	if e != nil {
		return e
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.Exec(`UPDATE web_plan SET price_wei=? WHERE id=1`, price.String()); e != nil {
		return e
	}
	if _, e = tx.Exec(`INSERT INTO admin_audit(admin_id,action,detail,at_utc) VALUES(?,'web_plan_price',?,?)`, admin, price.String(), time.Now().UTC().Format(time.RFC3339)); e != nil {
		return e
	}
	return tx.Commit()
}

type AdminWebUser struct {
	ID                int64  `json:"id"`
	Email             string `json:"email"`
	SubscriptionUntil string `json:"subscription_until"`
	Address           string `json:"address"`
}

func (s *Store) AdminUsers() ([]AdminWebUser, error) {
	rows, e := s.DB.Query(`SELECT u.id,u.email,u.subscription_until,COALESCE(w.address,'') FROM web_users u LEFT JOIN web_wallets w ON w.user_id=u.id ORDER BY u.id DESC LIMIT 200`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []AdminWebUser
	for rows.Next() {
		var u AdminWebUser
		if e = rows.Scan(&u.ID, &u.Email, &u.SubscriptionUntil, &u.Address); e != nil {
			return nil, e
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
