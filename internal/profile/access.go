package profile

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"tradebot/internal/entitlement"
)

var (
	ErrQuota             = errors.New("daily analysis allowance exhausted")
	ErrDuplicateAnalysis = errors.New("analysis already reserved")
)

func NewID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return ""
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	v := hex.EncodeToString(b[:])
	return v[:8] + "-" + v[8:12] + "-" + v[12:16] + "-" + v[16:20] + "-" + v[20:]
}
func (s *Store) MigrateAccess() error {
	_, e := s.DB.Exec(`CREATE TABLE IF NOT EXISTS entitlements(user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,exempt INTEGER NOT NULL DEFAULT 0,subscription_until TEXT NOT NULL DEFAULT '');CREATE TABLE IF NOT EXISTS daily_uses(analysis_id TEXT PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,day_tehran TEXT NOT NULL,status TEXT NOT NULL);CREATE INDEX IF NOT EXISTS daily_uses_day ON daily_uses(user_id,day_tehran,status);CREATE TABLE IF NOT EXISTS payment_events(charge_id TEXT PRIMARY KEY,user_id INTEGER NOT NULL REFERENCES users(id),kind TEXT NOT NULL,created_at TEXT NOT NULL);CREATE TABLE IF NOT EXISTS admin_audit(id INTEGER PRIMARY KEY,admin_id INTEGER NOT NULL,target_id INTEGER,action TEXT NOT NULL,detail TEXT NOT NULL,at_utc TEXT NOT NULL);CREATE TABLE IF NOT EXISTS analysis_config(version INTEGER PRIMARY KEY,engine_candles INTEGER NOT NULL,model_candles INTEGER NOT NULL,active INTEGER NOT NULL DEFAULT 0);INSERT OR IGNORE INTO analysis_config(version,engine_candles,model_candles,active) VALUES(1,500,500,1);INSERT OR IGNORE INTO analysis_config(version,engine_candles,model_candles,active) VALUES(2,500,50,0);`)
	if e != nil {
		return e
	}
	if e = s.migrateEntitlementPlans(); e != nil {
		return e
	}
	if _, alterErr := s.DB.Exec(`ALTER TABLE daily_uses ADD COLUMN access_kind TEXT NOT NULL DEFAULT 'free'`); alterErr != nil && !strings.Contains(alterErr.Error(), "duplicate column name") {
		return alterErr
	}
	if _, alterErr := s.DB.Exec(`ALTER TABLE analysis_config ADD COLUMN horizon_candles INTEGER NOT NULL DEFAULT 12`); alterErr != nil && !strings.Contains(alterErr.Error(), "duplicate column name") {
		return alterErr
	}
	for _, q := range []string{`ALTER TABLE analysis_config ADD COLUMN max_duration_seconds INTEGER NOT NULL DEFAULT 180`, `ALTER TABLE analysis_config ADD COLUMN response_token_budget INTEGER NOT NULL DEFAULT 4096`} {
		if _, alterErr := s.DB.Exec(q); alterErr != nil && !strings.Contains(alterErr.Error(), "duplicate column name") {
			return alterErr
		}
	}
	for _, q := range []string{`ALTER TABLE request_states ADD COLUMN config_version INTEGER NOT NULL DEFAULT 1`, `ALTER TABLE request_states ADD COLUMN engine_candles INTEGER NOT NULL DEFAULT 500`, `ALTER TABLE request_states ADD COLUMN model_candles INTEGER NOT NULL DEFAULT 500`, `ALTER TABLE request_states ADD COLUMN horizon_candles INTEGER NOT NULL DEFAULT 12`, `ALTER TABLE request_states ADD COLUMN model TEXT NOT NULL DEFAULT ''`, `ALTER TABLE request_states ADD COLUMN prompt_version TEXT NOT NULL DEFAULT ''`} {
		_, _ = s.DB.Exec(q)
	}
	_, e = s.DB.Exec(`CREATE TABLE IF NOT EXISTS analysis_attempts(attempt_id TEXT PRIMARY KEY,analysis_id TEXT NOT NULL REFERENCES request_states(id),started_at TEXT NOT NULL,finished_at TEXT NOT NULL DEFAULT '',status TEXT NOT NULL)`)
	if e != nil {
		return e
	}
	_, e = s.DB.Exec(`CREATE TABLE IF NOT EXISTS admin_login_codes(code_hash TEXT PRIMARY KEY,admin_id INTEGER NOT NULL,expires_at TEXT NOT NULL,used INTEGER NOT NULL DEFAULT 0)`)
	if e != nil {
		return e
	}
	_, e = s.DB.Exec(`CREATE TABLE IF NOT EXISTS ai_api_attempts(id INTEGER PRIMARY KEY AUTOINCREMENT,analysis_id TEXT NOT NULL,channel TEXT NOT NULL,provider TEXT NOT NULL,model TEXT NOT NULL,status TEXT NOT NULL,finish_reason TEXT NOT NULL,http_status INTEGER NOT NULL,started_at TEXT NOT NULL,latency_ms INTEGER NOT NULL,prompt_tokens INTEGER NOT NULL,completion_tokens INTEGER NOT NULL);CREATE INDEX IF NOT EXISTS ai_api_attempts_date ON ai_api_attempts(started_at,model,channel)`)
	if e != nil {
		return e
	}
	for _, q := range []string{
		`ALTER TABLE ai_api_attempts ADD COLUMN attempt_count INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE ai_api_attempts ADD COLUMN total_latency_ms INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE ai_api_attempts ADD COLUMN breaker_state TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE ai_api_attempts ADD COLUMN failure_class TEXT NOT NULL DEFAULT ''`,
	} {
		if _, alterErr := s.DB.Exec(q); alterErr != nil && !strings.Contains(alterErr.Error(), "duplicate column name") {
			return alterErr
		}
	}
	return s.migrateFinancialCore()
}

func (s *Store) migrateFinancialCore() error {
	var done int
	if e := s.DB.QueryRow(`SELECT count(*) FROM schema_version WHERE version=5`).Scan(&done); e != nil {
		return e
	}
	if done == 1 {
		return nil
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for _, q := range []string{
		`ALTER TABLE telegram_invoices ADD COLUMN start_block INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE telegram_invoices ADD COLUMN updated_at TEXT NOT NULL DEFAULT ''`,
	} {
		if _, e = tx.Exec(q); e != nil && !strings.Contains(strings.ToLower(e.Error()), "duplicate column name") {
			return e
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, e = tx.Exec(`UPDATE telegram_invoices SET status='expired',updated_at=? WHERE status='pending' AND expires_at<=?`, now, now); e != nil {
		return e
	}
	if _, e = tx.Exec(`UPDATE telegram_invoices SET status='superseded',updated_at=? WHERE status='pending' AND rowid NOT IN (SELECT max(rowid) FROM telegram_invoices WHERE status='pending' GROUP BY telegram_id)`, now); e != nil {
		return e
	}
	if _, e = tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS telegram_one_pending ON telegram_invoices(telegram_id) WHERE status='pending';CREATE TABLE IF NOT EXISTS payment_notification_outbox(id INTEGER PRIMARY KEY AUTOINCREMENT,event_key TEXT NOT NULL UNIQUE,telegram_id INTEGER NOT NULL,invoice_id TEXT NOT NULL,tx_hash TEXT NOT NULL,kind TEXT NOT NULL,amount_wei TEXT NOT NULL,plan_id TEXT NOT NULL DEFAULT 'standard',subscription_until TEXT NOT NULL DEFAULT '',status TEXT NOT NULL DEFAULT 'pending',attempts INTEGER NOT NULL DEFAULT 0,next_attempt_at TEXT NOT NULL,locked_until TEXT NOT NULL DEFAULT '',last_error TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,delivered_at TEXT NOT NULL DEFAULT '');CREATE INDEX IF NOT EXISTS payment_notification_due ON payment_notification_outbox(status,next_attempt_at)`); e != nil {
		return e
	}
	if _, e = tx.Exec(`INSERT OR IGNORE INTO schema_version(version) VALUES(5)`); e != nil {
		return e
	}
	return tx.Commit()
}

// migrateEntitlementPlans is additive and versioned. Existing paid accounts
// were contractually unlimited before plans existed, so they become Standard
// with an unlimited override rather than being silently reduced to 5/day.
func (s *Store) migrateEntitlementPlans() error {
	var done int
	if e := s.DB.QueryRow(`SELECT count(*) FROM schema_version WHERE version=3`).Scan(&done); e != nil {
		return e
	}
	if done == 1 {
		return nil
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for _, q := range []string{
		`ALTER TABLE entitlements ADD COLUMN plan_id TEXT NOT NULL DEFAULT 'visitor'`,
		`ALTER TABLE entitlements ADD COLUMN daily_allowance_override INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE telegram_invoices ADD COLUMN plan_id TEXT NOT NULL DEFAULT 'standard'`,
		`ALTER TABLE request_states ADD COLUMN plan_id TEXT NOT NULL DEFAULT 'visitor'`,
	} {
		if _, e = tx.Exec(q); e != nil && !strings.Contains(strings.ToLower(e.Error()), "duplicate column name") {
			return e
		}
	}
	if _, e = tx.Exec(`UPDATE entitlements SET plan_id='standard',daily_allowance_override=-1 WHERE subscription_until!='' AND (plan_id='' OR plan_id='visitor' OR lower(plan_id)='public')`); e != nil {
		return e
	}
	if _, e = tx.Exec(`UPDATE entitlements SET plan_id='standard' WHERE lower(plan_id)='public'`); e != nil {
		return e
	}
	if _, e = tx.Exec(`INSERT OR IGNORE INTO schema_version(version) VALUES(3)`); e != nil {
		return e
	}
	return tx.Commit()
}

type accessQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

func accessFor(q accessQuerier, user int64, now time.Time) (entitlement.Access, error) {
	plan := entitlement.Visitor
	untilRaw := ""
	override := 0
	exempt := user == AdminID
	var nativePlan string
	var nativeExempt int
	e := q.QueryRow(`SELECT subscription_until,plan_id,daily_allowance_override,exempt FROM entitlements WHERE user_id=?`, user).Scan(&untilRaw, &nativePlan, &override, &nativeExempt)
	if e != nil && e != sql.ErrNoRows {
		return entitlement.Access{}, e
	}
	if nativeExempt == 1 {
		exempt = true
	}
	until, _ := time.Parse(time.RFC3339, untilRaw)
	active := until.After(now)
	if active {
		plan = entitlement.Normalize(nativePlan)
		if plan == entitlement.Visitor { // corrupted/legacy paid row fails safely to Standard
			plan = entitlement.Standard
		}
	}

	// A linked web subscription participates in the same effective policy. Old
	// web schemas are interpreted as legacy Standard until web migration runs.
	var webReady, webPlanReady int
	if e = q.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='web_users'`).Scan(&webReady); e != nil {
		return entitlement.Access{}, e
	}
	if webReady != 0 {
		_ = q.QueryRow(`SELECT count(*) FROM pragma_table_info('web_users') WHERE name='plan_id'`).Scan(&webPlanReady)
		var webUntilRaw, webPlan string
		webOverride := 0
		if webPlanReady != 0 {
			e = q.QueryRow(`SELECT w.subscription_until,w.plan_id,w.daily_allowance_override FROM web_telegram_links l JOIN web_users w ON w.id=l.web_user_id WHERE l.telegram_id=?`, user).Scan(&webUntilRaw, &webPlan, &webOverride)
		} else {
			e = q.QueryRow(`SELECT w.subscription_until FROM web_telegram_links l JOIN web_users w ON w.id=l.web_user_id WHERE l.telegram_id=?`, user).Scan(&webUntilRaw)
			webPlan, webOverride = "standard", -1
		}
		if e != nil && e != sql.ErrNoRows {
			return entitlement.Access{}, e
		}
		webUntil, _ := time.Parse(time.RFC3339, webUntilRaw)
		webID := entitlement.Normalize(webPlan)
		webPriority, currentPriority := entitlement.For(string(webID)).Priority, entitlement.For(string(plan)).Priority
		preferWeb := !active || webPriority > currentPriority || webPriority == currentPriority && (webOverride < 0 && override >= 0 || webOverride == override && webUntil.After(until))
		if webUntil.After(now) && preferWeb {
			active, plan, until, untilRaw, override = true, webID, webUntil, webUntilRaw, webOverride
		}
	}

	policy := entitlement.For(string(plan))
	allowance := policy.DailyAllowance
	legacyUnlimited := false
	if active && override != 0 {
		allowance = override
		legacyUnlimited = override < 0
	}
	if exempt {
		allowance = -1
	}
	tz, _ := time.LoadLocation("Asia/Tehran")
	day := now.In(tz).Format("2006-01-02")
	var used int
	if e = q.QueryRow(`SELECT count(*) FROM daily_uses WHERE user_id=? AND day_tehran=? AND status IN ('reserved','consumed','uncertain')`, user, day).Scan(&used); e != nil {
		return entitlement.Access{}, e
	}
	remaining := -1
	if allowance >= 0 {
		remaining = allowance - used
		if remaining < 0 {
			remaining = 0
		}
	}
	return entitlement.Access{Policy: policy, SubscriptionActive: active, SubscriptionUntil: untilRaw, Exempt: exempt, Allowance: allowance, Used: used, Remaining: remaining, LegacyUnlimited: legacyUnlimited}, nil
}

func (s *Store) Access(user int64, now time.Time) (entitlement.Access, error) {
	return accessFor(s.DB, user, now)
}

func (s *Store) IssueWebLink(user int64, now time.Time) (string, error) {
	token := NewID()
	if token == "" {
		return "", errors.New("random unavailable")
	}
	h := sha256.Sum256([]byte(token))
	_, e := s.DB.Exec(`INSERT INTO web_link_tokens(token_hash,telegram_id,expires_at) VALUES(?,?,?)`, hex.EncodeToString(h[:]), user, now.UTC().Add(10*time.Minute).Format(time.RFC3339Nano))
	return token, e
}

func (s *Store) ConsumeWebLink(token string, webUser int64, now time.Time) error {
	if len(token) != 36 {
		return errors.New("invalid link")
	}
	h := sha256.Sum256([]byte(token))
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var telegramID int64
	e = tx.QueryRow(`SELECT telegram_id FROM web_link_tokens WHERE token_hash=? AND used=0 AND expires_at>?`, hex.EncodeToString(h[:]), now.UTC().Format(time.RFC3339Nano)).Scan(&telegramID)
	if e != nil {
		return errors.New("link expired or used")
	}
	var existing int64
	e = tx.QueryRow(`SELECT web_user_id FROM web_telegram_links WHERE telegram_id=?`, telegramID).Scan(&existing)
	if e == nil && existing != webUser {
		return errors.New("Telegram account already linked")
	}
	if e != nil && e != sql.ErrNoRows {
		return e
	}
	e = tx.QueryRow(`SELECT telegram_id FROM web_telegram_links WHERE web_user_id=?`, webUser).Scan(&existing)
	if e == nil && existing != telegramID {
		return errors.New("web account already linked")
	}
	if e != nil && e != sql.ErrNoRows {
		return e
	}
	if _, e = tx.Exec(`INSERT OR IGNORE INTO web_telegram_links(telegram_id,web_user_id,linked_at) VALUES(?,?,?)`, telegramID, webUser, now.UTC().Format(time.RFC3339)); e != nil {
		return e
	}
	res, e := tx.Exec(`UPDATE web_link_tokens SET used=1 WHERE token_hash=? AND used=0`, hex.EncodeToString(h[:]))
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil || n != 1 {
		return errors.New("link expired or used")
	}
	return tx.Commit()
}

func (s *Store) WebSubscription(user int64) (bool, time.Time, error) {
	var raw string
	e := s.DB.QueryRow(`SELECT w.subscription_until FROM web_telegram_links l JOIN web_users w ON w.id=l.web_user_id WHERE l.telegram_id=?`, user).Scan(&raw)
	if e == sql.ErrNoRows {
		return false, time.Time{}, nil
	}
	if e != nil {
		return false, time.Time{}, e
	}
	until, _ := time.Parse(time.RFC3339, raw)
	return true, until, nil
}

// Subscription reports whether the Telegram user has an active subscription
// (telegram-native via entitlements or web-linked) and until when.
func (s *Store) Subscription(id int64) (bool, time.Time, error) {
	a, e := s.Access(id, time.Now())
	if e != nil {
		return false, time.Time{}, e
	}
	until, _ := time.Parse(time.RFC3339, a.SubscriptionUntil)
	return a.SubscriptionActive, until, nil
}
func (s *Store) IssueAdminCode(id int64) (string, error) {
	if id != AdminID {
		return "", errors.New("forbidden")
	}
	code := NewID()
	if code == "" {
		return "", errors.New("random unavailable")
	}
	hash := sha256.Sum256([]byte(code))
	_, e := s.DB.Exec(`INSERT INTO admin_login_codes(code_hash,admin_id,expires_at) VALUES(?,?,?)`, hex.EncodeToString(hash[:]), id, time.Now().UTC().Add(5*time.Minute).Format(time.RFC3339))
	return code, e
}
func (s *Store) ConsumeAdminCode(code string) (bool, error) {
	hash := sha256.Sum256([]byte(code))
	res, e := s.DB.Exec(`UPDATE admin_login_codes SET used=1 WHERE code_hash=? AND admin_id=? AND used=0 AND expires_at>?`, hex.EncodeToString(hash[:]), AdminID, time.Now().UTC().Format(time.RFC3339))
	if e != nil {
		return false, e
	}
	n, e := res.RowsAffected()
	return n == 1, e
}
func (s *Store) Reserve(id string, user int64, now time.Time) error {
	tz, _ := time.LoadLocation("Asia/Tehran")
	day := now.In(tz).Format("2006-01-02")
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var existingUser int64
	if e = tx.QueryRow(`SELECT user_id FROM daily_uses WHERE analysis_id=?`, id).Scan(&existingUser); e == nil {
		return ErrDuplicateAnalysis
	} else if e != sql.ErrNoRows {
		return e
	}
	access, e := accessFor(tx, user, now)
	if e != nil {
		return e
	}
	if access.Allowance >= 0 && access.Used >= access.Allowance {
		return ErrQuota
	}
	accessKind := string(access.Policy.ID)
	if access.Exempt {
		accessKind = "exempt"
	}
	_, e = tx.Exec(`INSERT INTO daily_uses(analysis_id,user_id,day_tehran,status,access_kind) VALUES(?,?,?,'reserved',?)`, id, user, day, accessKind)
	if e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) SetUse(id, status string) error {
	_, e := s.DB.Exec(`UPDATE daily_uses SET status=? WHERE analysis_id=?`, status, id)
	return e
}
func (s *Store) SetExempt(admin, target int64, on bool) error {
	if admin != AdminID {
		return errors.New("forbidden")
	}
	if target == AdminID && !on {
		return errors.New("admin exemption cannot be revoked")
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	v := 0
	if on {
		v = 1
	}
	var exists int
	if e = tx.QueryRow(`SELECT count(*) FROM users WHERE id=?`, target).Scan(&exists); e != nil {
		return e
	}
	if exists != 1 {
		return errors.New("user not found")
	}
	if _, e = tx.Exec(`INSERT INTO entitlements(user_id,exempt) VALUES(?,?) ON CONFLICT(user_id) DO UPDATE SET exempt=excluded.exempt`, target, v); e != nil {
		return e
	}
	if _, e = tx.Exec(`INSERT INTO admin_audit(admin_id,target_id,action,detail,at_utc) VALUES(?,?, 'exemption',?,?)`, admin, target, map[bool]string{true: "on", false: "off"}[on], time.Now().UTC().Format(time.RFC3339)); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) ActivateOnce(user int64, charge, kind string, at time.Time) error {
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	res, e := tx.Exec(`INSERT OR IGNORE INTO payment_events VALUES(?,?,?,?)`, charge, user, kind, at.UTC().Format(time.RFC3339))
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return tx.Commit()
	}
	var prev string
	_ = tx.QueryRow(`SELECT subscription_until FROM entitlements WHERE user_id=?`, user).Scan(&prev)
	base := at
	if t, e := time.Parse(time.RFC3339, prev); e == nil && t.After(base) {
		base = t
	}
	_, e = tx.Exec(`INSERT INTO entitlements(user_id,subscription_until,plan_id) VALUES(?,?,'standard') ON CONFLICT(user_id) DO UPDATE SET subscription_until=excluded.subscription_until,plan_id=CASE WHEN entitlements.plan_id='premium' THEN entitlements.plan_id ELSE 'standard' END`, user, base.AddDate(0, 1, 0).UTC().Format(time.RFC3339))
	if e != nil {
		return e
	}
	return tx.Commit()
}
