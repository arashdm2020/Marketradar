package profile

import (
	"database/sql"
	"errors"
	"fmt"
	_ "github.com/mattn/go-sqlite3"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"tradebot/internal/custody"
)

type User struct {
	ID                            int64
	State                         string
	Markets                       string
	MinProfit, MaxProfit, MaxRisk float64
	Completed                     bool
	Timezone                      string
}
type Store struct{ DB *sql.DB }
type Alert struct {
	ID, UserID              int64
	Symbol, Timeframe, Kind string
	Price                   float64
	After                   time.Time
	Entry                   float64
	Stage                   int
	Expires                 time.Time
}
type TelegramInvoice struct {
	ID         string    `json:"id"`
	TelegramID int64     `json:"-"`
	Address    string    `json:"address"`
	PriceWei   string    `json:"price_wei"`
	ChainID    uint64    `json:"chain_id"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	StartBlock uint64    `json:"start_block"`
	Status     string    `json:"status"`
	TxHash     string    `json:"tx_hash,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func Open(path string) (*Store, error) {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return nil, e
	}
	db, e := sql.Open("sqlite3", path+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000")
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	schema := `CREATE TABLE IF NOT EXISTS schema_version(version INTEGER PRIMARY KEY); INSERT OR IGNORE INTO schema_version VALUES(1); CREATE TABLE IF NOT EXISTS users(id INTEGER PRIMARY KEY,state TEXT NOT NULL DEFAULT 'markets',markets TEXT NOT NULL DEFAULT '',min_profit REAL NOT NULL DEFAULT 0,max_profit REAL NOT NULL DEFAULT 0,max_risk REAL NOT NULL DEFAULT 0,completed INTEGER NOT NULL DEFAULT 0,timezone TEXT NOT NULL DEFAULT 'Asia/Tehran',updated_at TEXT NOT NULL); CREATE TABLE IF NOT EXISTS analyses(id INTEGER PRIMARY KEY AUTOINCREMENT,user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,symbol TEXT NOT NULL,timeframe TEXT NOT NULL,style TEXT NOT NULL,created_at TEXT NOT NULL,result_json TEXT NOT NULL); CREATE TABLE IF NOT EXISTS alerts(id INTEGER PRIMARY KEY AUTOINCREMENT,user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,symbol TEXT NOT NULL,timeframe TEXT NOT NULL,kind TEXT NOT NULL,price REAL NOT NULL,after_utc TEXT NOT NULL,active INTEGER NOT NULL DEFAULT 1); CREATE TABLE IF NOT EXISTS request_states(id TEXT PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,chat_id INTEGER NOT NULL,symbol TEXT NOT NULL,timeframe TEXT NOT NULL,style TEXT NOT NULL,status TEXT NOT NULL DEFAULT 'queued',created_at TEXT NOT NULL,updated_at TEXT NOT NULL,message_id INTEGER,result_json TEXT,error TEXT); CREATE TABLE IF NOT EXISTS telegram_wallets(telegram_id INTEGER PRIMARY KEY,derivation_index INTEGER NOT NULL,address TEXT NOT NULL UNIQUE,created_at TEXT NOT NULL); CREATE TABLE IF NOT EXISTS telegram_invoices(id TEXT PRIMARY KEY,telegram_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,address TEXT NOT NULL,price_wei TEXT NOT NULL,chain_id INTEGER NOT NULL,created_at TEXT NOT NULL,expires_at TEXT NOT NULL,status TEXT NOT NULL DEFAULT 'pending',tx_hash TEXT NOT NULL DEFAULT '');CREATE INDEX IF NOT EXISTS telegram_invoices_user ON telegram_invoices(telegram_id,status); CREATE TABLE IF NOT EXISTS telegram_ui_state(telegram_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,message_id INTEGER NOT NULL,updated_at TEXT NOT NULL);`
	if _, e = db.Exec(schema); e != nil {
		db.Close()
		return nil, e
	}
	for _, stmt := range []string{"ALTER TABLE alerts ADD COLUMN entry REAL NOT NULL DEFAULT 0", "ALTER TABLE alerts ADD COLUMN stage INTEGER NOT NULL DEFAULT 0", "ALTER TABLE alerts ADD COLUMN expires_utc TEXT NOT NULL DEFAULT ''"} {
		_, _ = db.Exec(stmt)
	}
	for _, stmt := range []string{"ALTER TABLE telegram_wallets ADD COLUMN private_key_hex TEXT NOT NULL DEFAULT ''", "ALTER TABLE telegram_wallets ADD COLUMN signable INTEGER NOT NULL DEFAULT 0", "ALTER TABLE telegram_wallets ADD COLUMN private_key_cipher BLOB NOT NULL DEFAULT X''", "ALTER TABLE telegram_wallets ADD COLUMN key_encryption_version INTEGER NOT NULL DEFAULT 0"} {
		_, _ = db.Exec(stmt)
	}
	if e := (&Store{db}).MigrateAccess(); e != nil {
		db.Close()
		return nil, e
	}
	return &Store{db}, nil
}
func (s *Store) Get(id int64) (User, error) {
	var u User
	var done int
	e := s.DB.QueryRow(`SELECT id,state,markets,min_profit,max_profit,max_risk,completed,timezone FROM users WHERE id=?`, id).Scan(&u.ID, &u.State, &u.Markets, &u.MinProfit, &u.MaxProfit, &u.MaxRisk, &done, &u.Timezone)
	u.Completed = done == 1
	return u, e
}
func (s *Store) Ensure(id int64) (User, error) {
	_, e := s.DB.Exec(`INSERT OR IGNORE INTO users(id,updated_at) VALUES(?,?)`, id, time.Now().UTC().Format(time.RFC3339))
	if e != nil {
		return User{}, e
	}
	return s.Get(id)
}

// EnsureWallet provisions exactly one persistent BSC deposit wallet per Telegram user.
// The derive callback produces the public address for a given derivation index (xpub-derived;
// it never exposes private keys). Concurrent calls resolve to the same wallet.
func (s *Store) EnsureWallet(telegramID int64, derive func(uint32) (string, error)) (string, error) {
	var address string
	var done int
	e := s.DB.QueryRow(`SELECT address,1 FROM telegram_wallets WHERE telegram_id=?`, telegramID).Scan(&address, &done)
	if e == nil {
		return address, nil
	}
	if e != sql.ErrNoRows {
		return "", e
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return "", e
	}
	defer tx.Rollback()
	var verified int
	if e = tx.QueryRow(`SELECT count(*) FROM users WHERE id=?`, telegramID).Scan(&verified); e != nil {
		return "", e
	}
	if verified != 1 {
		return "", errors.New("verified Telegram user required")
	}
	var existing string
	if e = tx.QueryRow(`SELECT address FROM telegram_wallets WHERE telegram_id=?`, telegramID).Scan(&existing); e == nil {
		return existing, tx.Commit()
	}
	if e != sql.ErrNoRows {
		return "", e
	}
	var next int64
	if e = tx.QueryRow(`SELECT next_index FROM telegram_wallet_sequence WHERE id=1`).Scan(&next); e != nil {
		return "", e
	}
	if next < 0 || next >= 1<<31 {
		return "", errors.New("wallet index exhausted")
	}
	addr, e := derive(uint32(next))
	if e != nil {
		return "", e
	}
	if _, e = tx.Exec(`INSERT INTO telegram_wallets(telegram_id,derivation_index,address,created_at) VALUES(?,?,?,?)`, telegramID, next, addr, time.Now().UTC().Format(time.RFC3339)); e != nil {
		return "", e
	}
	if _, e = tx.Exec(`UPDATE telegram_wallet_sequence SET next_index=? WHERE id=1 AND next_index=?`, next+1, next); e != nil {
		return "", e
	}
	if e = tx.Commit(); e != nil {
		return "", e
	}
	return addr, nil
}

// EnsureCustodialWallet atomically provisions one wallet whose address is
// derived from the private key stored in that same row. Existing wallets are
// never replaced; legacy rows without a matching key remain non-signable.
func (s *Store) EnsureCustodialWallet(telegramID int64) (string, error) {
	var address string
	if e := s.DB.QueryRow(`SELECT address FROM telegram_wallets WHERE telegram_id=?`, telegramID).Scan(&address); e == nil {
		return address, nil
	} else if e != sql.ErrNoRows {
		return "", e
	}
	secure, e := custody.New(s.DB)
	if e != nil {
		return "", e
	}
	return secure.EnsureWallet(telegramID)
}
func (s *Store) Save(u User) error {
	if u.MinProfit < 0 || u.MaxProfit < 0 || u.MaxRisk < 0 || u.MaxRisk > 100 {
		return fmt.Errorf("invalid preference")
	}
	if u.MaxProfit > 0 && u.MinProfit > u.MaxProfit {
		return fmt.Errorf("minimum exceeds maximum")
	}
	done := 0
	if u.Completed {
		done = 1
	}
	_, e := s.DB.Exec(`UPDATE users SET state=?,markets=?,min_profit=?,max_profit=?,max_risk=?,completed=?,timezone=?,updated_at=? WHERE id=?`, u.State, u.Markets, u.MinProfit, u.MaxProfit, u.MaxRisk, done, u.Timezone, time.Now().UTC().Format(time.RFC3339), u.ID)
	return e
}
func (s *Store) Delete(id int64) error {
	_, e := s.DB.Exec(`DELETE FROM users WHERE id=?`, id)
	return e
}
func (s *Store) Record(id int64, symbol, frame, style, result string) error {
	_, e := s.DB.Exec(`INSERT INTO analyses(user_id,symbol,timeframe,style,created_at,result_json) VALUES(?,?,?,?,?,?)`, id, symbol, frame, style, time.Now().UTC().Format(time.RFC3339), result)
	return e
}
func (s *Store) History(id int64) ([]string, error) {
	rows, e := s.DB.Query(`SELECT created_at||' | '||symbol||' | '||timeframe||' | '||style FROM analyses WHERE user_id=? ORDER BY id DESC LIMIT 10`, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var x string
		if e = rows.Scan(&x); e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func (s *Store) AddAlert(a Alert) error {
	_, e := s.DB.Exec(`INSERT INTO alerts(user_id,symbol,timeframe,kind,price,after_utc,entry,stage,expires_utc) VALUES(?,?,?,?,?,?,?,?,?)`, a.UserID, a.Symbol, a.Timeframe, a.Kind, a.Price, a.After.UTC().Format(time.RFC3339), a.Entry, a.Stage, a.Expires.UTC().Format(time.RFC3339))
	return e
}
func (s *Store) ActiveAlerts() ([]Alert, error) {
	rows, e := s.DB.Query(`SELECT id,user_id,symbol,timeframe,kind,price,after_utc,entry,stage,expires_utc FROM alerts WHERE active=1`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []Alert
	for rows.Next() {
		var a Alert
		var ts, exp string
		if e = rows.Scan(&a.ID, &a.UserID, &a.Symbol, &a.Timeframe, &a.Kind, &a.Price, &ts, &a.Entry, &a.Stage, &exp); e != nil {
			return nil, e
		}
		a.After, _ = time.Parse(time.RFC3339, ts)
		a.Expires, _ = time.Parse(time.RFC3339, exp)
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *Store) AdvanceAlert(id int64, stage int, after time.Time) error {
	_, e := s.DB.Exec(`UPDATE alerts SET stage=?,after_utc=? WHERE id=? AND active=1`, stage, after.UTC().Format(time.RFC3339), id)
	return e
}
func (s *Store) CloseAlert(id int64) error {
	_, e := s.DB.Exec(`UPDATE alerts SET active=0 WHERE id=?`, id)
	return e
}
func (s *Store) Latest(id int64) (string, error) {
	var raw string
	e := s.DB.QueryRow(`SELECT result_json FROM analyses WHERE user_id=? ORDER BY id DESC LIMIT 1`, id).Scan(&raw)
	return raw, e
}
func (s *Store) Wallet(telegramID int64) (string, error) {
	var address string
	e := s.DB.QueryRow(`SELECT address FROM telegram_wallets WHERE telegram_id=?`, telegramID).Scan(&address)
	return address, e
}
func (s *Store) DashboardMessage(telegramID int64) (int64, error) {
	var messageID int64
	e := s.DB.QueryRow(`SELECT message_id FROM telegram_ui_state WHERE telegram_id=?`, telegramID).Scan(&messageID)
	return messageID, e
}
func (s *Store) SetDashboardMessage(telegramID, messageID int64) error {
	_, e := s.DB.Exec(`INSERT INTO telegram_ui_state(telegram_id,message_id,updated_at) VALUES(?,?,?) ON CONFLICT(telegram_id) DO UPDATE SET message_id=excluded.message_id,updated_at=excluded.updated_at`, telegramID, messageID, time.Now().UTC().Format(time.RFC3339))
	return e
}
func (s *Store) WalletByAddress(address string) (int64, error) {
	var id int64
	e := s.DB.QueryRow(`SELECT telegram_id FROM telegram_wallets WHERE lower(address)=lower(?)`, address).Scan(&id)
	return id, e
}
func (s *Store) CreateTelegramInvoice(telegramID int64, address, priceWei string, chainID uint64, expiresAt time.Time, startBlocks ...uint64) (TelegramInvoice, error) {
	amount, ok := new(big.Int).SetString(priceWei, 10)
	if !ok || amount.Sign() <= 0 {
		return TelegramInvoice{}, errors.New("invalid invoice amount")
	}
	if chainID != 56 {
		return TelegramInvoice{}, errors.New("unsupported invoice network")
	}
	id := NewID()
	if id == "" {
		return TelegramInvoice{}, errors.New("random unavailable")
	}
	now := time.Now().UTC()
	startBlock := uint64(0)
	if len(startBlocks) > 0 {
		startBlock = startBlocks[0]
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return TelegramInvoice{}, e
	}
	defer tx.Rollback()
	var owned string
	if e = tx.QueryRow(`SELECT address FROM telegram_wallets WHERE telegram_id=?`, telegramID).Scan(&owned); e != nil {
		return TelegramInvoice{}, errors.New("wallet unavailable")
	}
	if !strings.EqualFold(owned, address) {
		return TelegramInvoice{}, errors.New("invoice wallet ownership mismatch")
	}
	if _, e = tx.Exec(`UPDATE telegram_invoices SET status='expired',updated_at=? WHERE telegram_id=? AND status='pending' AND expires_at<=?`, now.Format(time.RFC3339), telegramID, now.Format(time.RFC3339)); e != nil {
		return TelegramInvoice{}, e
	}
	var existing TelegramInvoice
	var created, expires, updated string
	e = tx.QueryRow(`SELECT id,telegram_id,address,price_wei,chain_id,created_at,expires_at,start_block,status,tx_hash,updated_at FROM telegram_invoices WHERE telegram_id=? AND status='pending'`, telegramID).Scan(&existing.ID, &existing.TelegramID, &existing.Address, &existing.PriceWei, &existing.ChainID, &created, &expires, &existing.StartBlock, &existing.Status, &existing.TxHash, &updated)
	if e == nil {
		existing.CreatedAt, _ = time.Parse(time.RFC3339, created)
		existing.ExpiresAt, _ = time.Parse(time.RFC3339, expires)
		existing.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
		return existing, tx.Commit()
	}
	if e != sql.ErrNoRows {
		return TelegramInvoice{}, e
	}
	_, e = tx.Exec(`INSERT INTO telegram_invoices(id,telegram_id,address,price_wei,chain_id,created_at,expires_at,start_block,status,updated_at) VALUES(?,?,?,?,?,?,?,?,'pending',?)`, id, telegramID, address, priceWei, chainID, now.Format(time.RFC3339), expiresAt.UTC().Format(time.RFC3339), startBlock, now.Format(time.RFC3339))
	if e != nil {
		return TelegramInvoice{}, e
	}
	if e = tx.Commit(); e != nil {
		return TelegramInvoice{}, e
	}
	return TelegramInvoice{ID: id, TelegramID: telegramID, Address: address, PriceWei: priceWei, ChainID: chainID, CreatedAt: now, ExpiresAt: expiresAt.UTC(), StartBlock: startBlock, Status: "pending", UpdatedAt: now}, nil
}
func (s *Store) TelegramInvoice(telegramID int64, id string) (TelegramInvoice, error) {
	var inv TelegramInvoice
	var created, expires, updated string
	e := s.DB.QueryRow(`SELECT id,telegram_id,address,price_wei,chain_id,created_at,expires_at,start_block,status,tx_hash,updated_at FROM telegram_invoices WHERE id=? AND telegram_id=?`, id, telegramID).Scan(&inv.ID, &inv.TelegramID, &inv.Address, &inv.PriceWei, &inv.ChainID, &created, &expires, &inv.StartBlock, &inv.Status, &inv.TxHash, &updated)
	if e != nil {
		return inv, e
	}
	inv.CreatedAt, _ = time.Parse(time.RFC3339, created)
	inv.ExpiresAt, _ = time.Parse(time.RFC3339, expires)
	inv.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	if inv.Status == "pending" && time.Now().After(inv.ExpiresAt) {
		_, _ = s.DB.Exec(`UPDATE telegram_invoices SET status='expired',updated_at=? WHERE id=? AND status='pending'`, time.Now().UTC().Format(time.RFC3339), id)
		inv.Status = "expired"
	}
	return inv, nil
}
func (s *Store) ListTelegramInvoices(telegramID int64) ([]TelegramInvoice, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	_, _ = s.DB.Exec(`UPDATE telegram_invoices SET status='expired',updated_at=? WHERE telegram_id=? AND status='pending' AND expires_at<=?`, now, telegramID, now)
	rows, e := s.DB.Query(`SELECT id,telegram_id,address,price_wei,chain_id,created_at,expires_at,start_block,status,tx_hash,updated_at FROM telegram_invoices WHERE telegram_id=? ORDER BY created_at DESC LIMIT 100`, telegramID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []TelegramInvoice
	for rows.Next() {
		var inv TelegramInvoice
		var created, expires, updated string
		if e = rows.Scan(&inv.ID, &inv.TelegramID, &inv.Address, &inv.PriceWei, &inv.ChainID, &created, &expires, &inv.StartBlock, &inv.Status, &inv.TxHash, &updated); e != nil {
			return nil, e
		}
		inv.CreatedAt, _ = time.Parse(time.RFC3339, created)
		inv.ExpiresAt, _ = time.Parse(time.RFC3339, expires)
		inv.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
		out = append(out, inv)
	}
	return out, rows.Err()
}
func (s *Store) PageTelegramInvoices(telegramID int64, limit, offset int) ([]TelegramInvoice, int, error) {
	if limit < 1 || limit > 20 || offset < 0 {
		return nil, 0, errors.New("invalid pagination")
	}
	var total int
	if e := s.DB.QueryRow(`SELECT count(*) FROM telegram_invoices WHERE telegram_id=?`, telegramID).Scan(&total); e != nil {
		return nil, 0, e
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, _ = s.DB.Exec(`UPDATE telegram_invoices SET status='expired',updated_at=? WHERE telegram_id=? AND status='pending' AND expires_at<=?`, now, telegramID, now)
	rows, e := s.DB.Query(`SELECT id,telegram_id,address,price_wei,chain_id,created_at,expires_at,start_block,status,tx_hash,updated_at FROM telegram_invoices WHERE telegram_id=? ORDER BY created_at DESC LIMIT ? OFFSET ?`, telegramID, limit, offset)
	if e != nil {
		return nil, 0, e
	}
	defer rows.Close()
	out := make([]TelegramInvoice, 0, limit)
	for rows.Next() {
		var inv TelegramInvoice
		var created, expires, updated string
		if e = rows.Scan(&inv.ID, &inv.TelegramID, &inv.Address, &inv.PriceWei, &inv.ChainID, &created, &expires, &inv.StartBlock, &inv.Status, &inv.TxHash, &updated); e != nil {
			return nil, 0, e
		}
		inv.CreatedAt, _ = time.Parse(time.RFC3339, created)
		inv.ExpiresAt, _ = time.Parse(time.RFC3339, expires)
		inv.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
		out = append(out, inv)
	}
	return out, total, rows.Err()
}
func (s *Store) MarkTelegramInvoicePaid(id, txHash string) error {
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	res, e := tx.Exec(`UPDATE telegram_invoices SET status='paid',tx_hash=?,updated_at=? WHERE id=? AND status='pending'`, txHash, time.Now().UTC().Format(time.RFC3339), id)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return errors.New("invoice changed during settlement")
	}
	return tx.Commit()
}

type RequestState struct {
	ID, UserID, ChatID, Symbol, Timeframe, Style, Status string
	CreatedAt, UpdatedAt                                 time.Time
	MessageID                                            *int64
	ResultJSON, Error                                    *string
}

func (s *Store) CreateRequestState(id, userID, chatID, symbol, timeframe, style string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, e := s.DB.Exec(`INSERT INTO request_states(id,user_id,chat_id,symbol,timeframe,style,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		id, userID, chatID, symbol, timeframe, style, "queued", now, now)
	return e
}

func (s *Store) UpdateRequestState(id, status, resultJSON, err string, messageID *int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, e := s.DB.Exec(`UPDATE request_states SET status=?,updated_at=?,message_id=?,result_json=?,error=? WHERE id=?`,
		status, now, messageID, resultJSON, err, id)
	return e
}

func (s *Store) GetRequestState(id string) (RequestState, error) {
	var r RequestState
	var msgID, result, err sql.NullString
	var createdAt, updatedAt string
	e := s.DB.QueryRow(`SELECT id,user_id,chat_id,symbol,timeframe,style,status,created_at,updated_at,message_id,result_json,error FROM request_states WHERE id=?`, id).
		Scan(&r.ID, &r.UserID, &r.ChatID, &r.Symbol, &r.Timeframe, &r.Style, &r.Status, &createdAt, &updatedAt, &msgID, &result, &err)
	if e != nil {
		return r, e
	}
	r.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	r.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	if msgID.Valid {
		v, _ := strconv.ParseInt(msgID.String, 10, 64)
		r.MessageID = &v
	}
	if result.Valid {
		r.ResultJSON = &result.String
	}
	if err.Valid {
		r.Error = &err.String
	}
	return r, nil
}

func (s *Store) InFlightRequestForUser(userID int64) (RequestState, bool) {
	var r RequestState
	var msgID, result, err sql.NullString
	e := s.DB.QueryRow(`SELECT id,user_id,chat_id,symbol,timeframe,style,status,created_at,updated_at,message_id,result_json,error FROM request_states WHERE user_id=? AND status IN ('queued','running') ORDER BY created_at DESC LIMIT 1`, userID).
		Scan(&r.ID, &r.UserID, &r.ChatID, &r.Symbol, &r.Timeframe, &r.Style, &r.Status, &r.CreatedAt, &r.UpdatedAt, &msgID, &result, &err)
	if e != nil {
		return r, false
	}
	return r, true
}
