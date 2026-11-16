package treasury

import (
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
	"tradebot/internal/profile"
)

type Store struct {
	DB       *sql.DB
	Profiles *profile.Store
}
type Destination struct {
	Version int    `json:"version"`
	Address string `json:"address"`
	Active  bool   `json:"active"`
	At      string `json:"at"`
}
type Job struct {
	PaymentHash        string        `json:"payment_hash"`
	Source             string        `json:"source"`
	AmountWei          string        `json:"amount_wei"`
	DestinationVersion int           `json:"destination_version"`
	Status             string        `json:"status"`
	Nonce              sql.NullInt64 `json:"-"`
	TxHash             string        `json:"tx_hash"`
	RawTx              string        `json:"-"`
	FeeWei             string        `json:"-"`
	UpdatedAt          string        `json:"updated_at"`
	Attempts           int           `json:"attempts"`
	LastError          string        `json:"last_error"`
}

func (s *Store) Migrate() error {
	var done int
	if e := s.DB.QueryRow(`SELECT count(*) FROM schema_version WHERE version=8`).Scan(&done); e != nil || done == 1 {
		return e
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for _, q := range []string{`ALTER TABLE treasury_jobs ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0`, `ALTER TABLE treasury_jobs ADD COLUMN last_error TEXT NOT NULL DEFAULT ''`, `ALTER TABLE treasury_jobs ADD COLUMN next_attempt_at TEXT NOT NULL DEFAULT ''`} {
		if _, e = tx.Exec(q); e != nil && !strings.Contains(strings.ToLower(e.Error()), "duplicate column name") {
			return e
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, e = tx.Exec(`UPDATE treasury_jobs SET status='duplicate_hold',last_error='duplicate active source quarantined; transaction state preserved',updated_at=? WHERE status IN ('queued','preparing','signed','broadcast','broadcast_unknown','insufficient_gas') AND rowid NOT IN (SELECT MIN(rowid) FROM treasury_jobs WHERE status IN ('queued','preparing','signed','broadcast','broadcast_unknown','insufficient_gas') GROUP BY lower(source))`, now); e != nil {
		return e
	}
	if _, e = tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS treasury_one_active_source ON treasury_jobs(lower(source)) WHERE status IN ('queued','preparing','signed','broadcast','broadcast_unknown','insufficient_gas');INSERT OR IGNORE INTO schema_version(version) VALUES(8)`); e != nil {
		return e
	}
	return tx.Commit()
}

const DefaultDestination = "0x0F65096f6C02aA6DC0d0b8c99dDeBbc3CD90B3F5"

// SeedDestination installs the public receiving address only when no destination
// has ever been configured. It never changes an administrator-selected value.
func (s *Store) SeedDestination(address string) error {
	if e := s.Migrate(); e != nil {
		return e
	}
	if !ValidAddress(address) {
		return errors.New("invalid default destination")
	}
	var count int
	if e := s.DB.QueryRow(`SELECT count(*) FROM treasury_destinations`).Scan(&count); e != nil {
		return e
	}
	if count != 0 {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.Exec(`INSERT INTO treasury_destinations(address,active,admin_id,at_utc) VALUES(?,1,?,?)`, strings.ToLower(address), profile.AdminID, now); e != nil {
		return e
	}
	if _, e = tx.Exec(`INSERT INTO admin_audit(admin_id,action,detail,at_utc) VALUES(?,'treasury_destination_seed','mainnet public destination',?)`, profile.AdminID, now); e != nil {
		return e
	}
	return tx.Commit()
}

func ValidAddress(v string) bool {
	if len(v) != 42 || !strings.HasPrefix(v, "0x") {
		return false
	}
	if strings.EqualFold(v, "0x0000000000000000000000000000000000000000") {
		return false
	}
	for _, c := range v[2:] {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}
func (s *Store) ActiveDestination() (Destination, error) {
	var d Destination
	var active int
	e := s.DB.QueryRow(`SELECT version,address,active,at_utc FROM treasury_destinations WHERE active=1`).Scan(&d.Version, &d.Address, &active, &d.At)
	d.Active = active == 1
	return d, e
}
func (s *Store) SetDestination(admin int64, code, address string) (Destination, error) {
	if e := s.Migrate(); e != nil {
		return Destination{}, e
	}
	if admin != profile.AdminID || !ValidAddress(address) {
		return Destination{}, errors.New("invalid destination or admin")
	}
	ok, e := s.Profiles.ConsumeAdminCode(code)
	if e != nil || !ok {
		return Destination{}, errors.New("fresh admin authentication required")
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return Destination{}, e
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, e = tx.Exec(`UPDATE treasury_destinations SET active=0 WHERE active=1`); e != nil {
		return Destination{}, e
	}
	r, e := tx.Exec(`INSERT INTO treasury_destinations(address,active,admin_id,at_utc) VALUES(?,1,?,?)`, strings.ToLower(address), admin, now)
	if e != nil {
		return Destination{}, e
	}
	id, _ := r.LastInsertId()
	if _, e = tx.Exec(`UPDATE treasury_jobs SET status='needs_review',updated_at=? WHERE status IN ('queued','awaiting_destination','insufficient_gas')`, now); e != nil {
		return Destination{}, e
	}
	if _, e = tx.Exec(`INSERT INTO admin_audit(admin_id,action,detail,at_utc) VALUES(?,'treasury_destination',?,?)`, admin, fmt.Sprintf("version=%d", id), now); e != nil {
		return Destination{}, e
	}
	if e = tx.Commit(); e != nil {
		return Destination{}, e
	}
	return Destination{Version: int(id), Address: strings.ToLower(address), Active: true, At: now}, nil
}
func (s *Store) Job(hash string) (Job, error) {
	var j Job
	e := s.DB.QueryRow(`SELECT payment_hash,source,amount_wei,destination_version,status,nonce,tx_hash,raw_tx,fee_wei,attempts,last_error FROM treasury_jobs WHERE payment_hash=?`, strings.ToLower(hash)).Scan(&j.PaymentHash, &j.Source, &j.AmountWei, &j.DestinationVersion, &j.Status, &j.Nonce, &j.TxHash, &j.RawTx, &j.FeeWei, &j.Attempts, &j.LastError)
	return j, e
}
func (s *Store) Jobs() ([]Job, error) {
	rows, e := s.DB.Query(`SELECT payment_hash,source,amount_wei,destination_version,status,nonce,tx_hash,raw_tx,fee_wei,updated_at,attempts,last_error FROM treasury_jobs ORDER BY updated_at DESC LIMIT 200`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var j Job
		var updated string
		if e = rows.Scan(&j.PaymentHash, &j.Source, &j.AmountWei, &j.DestinationVersion, &j.Status, &j.Nonce, &j.TxHash, &j.RawTx, &j.FeeWei, &updated, &j.Attempts, &j.LastError); e != nil {
			return nil, e
		}
		j.UpdatedAt = updated
		out = append(out, j)
	}
	return out, rows.Err()
}
func (s *Store) NextJob() (Job, error) {
	var j Job
	e := s.DB.QueryRow(`SELECT payment_hash,source,amount_wei,destination_version,status,nonce,tx_hash,raw_tx,fee_wei,attempts,last_error FROM treasury_jobs WHERE status IN ('broadcast_unknown','signed','broadcast','queued','insufficient_gas') AND (next_attempt_at='' OR next_attempt_at<=?) ORDER BY CASE status WHEN 'broadcast_unknown' THEN 0 WHEN 'signed' THEN 1 WHEN 'broadcast' THEN 2 WHEN 'queued' THEN 3 ELSE 4 END,updated_at LIMIT 1`, time.Now().UTC().Format(time.RFC3339)).Scan(&j.PaymentHash, &j.Source, &j.AmountWei, &j.DestinationVersion, &j.Status, &j.Nonce, &j.TxHash, &j.RawTx, &j.FeeWei, &j.Attempts, &j.LastError)
	return j, e
}
func (s *Store) Counts() (map[string]int, error) {
	rows, e := s.DB.Query(`SELECT status,count(*) FROM treasury_jobs GROUP BY status`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	m := map[string]int{}
	for rows.Next() {
		var status string
		var n int
		if e = rows.Scan(&status, &n); e != nil {
			return nil, e
		}
		m[status] = n
	}
	return m, rows.Err()
}
func (s *Store) Amounts() (map[string]string, error) {
	rows, e := s.DB.Query(`SELECT status,amount_wei,fee_wei FROM treasury_jobs`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	totals := map[string]*big.Int{}
	for rows.Next() {
		var status, amountRaw, feeRaw string
		if e = rows.Scan(&status, &amountRaw, &feeRaw); e != nil {
			return nil, e
		}
		amount, ok := new(big.Int).SetString(amountRaw, 10)
		if !ok {
			return nil, errors.New("invalid treasury amount")
		}
		if feeRaw != "" {
			fee, ok := new(big.Int).SetString(feeRaw, 10)
			if !ok {
				return nil, errors.New("invalid treasury fee")
			}
			amount.Sub(amount, fee)
		}
		if amount.Sign() < 0 {
			return nil, errors.New("negative treasury amount")
		}
		if totals[status] == nil {
			totals[status] = new(big.Int)
		}
		totals[status].Add(totals[status], amount)
	}
	out := map[string]string{}
	for status, v := range totals {
		out[status] = v.String()
	}
	return out, rows.Err()
}
func (s *Store) Recover() error {
	if e := s.Migrate(); e != nil {
		return e
	}
	_, e := s.DB.Exec(`UPDATE treasury_jobs SET status='queued',last_error='recovered after restart before signing',next_attempt_at='',updated_at=? WHERE status='preparing' AND raw_tx='';UPDATE treasury_jobs SET status='signed',last_error='recovered signed transaction',next_attempt_at='',updated_at=? WHERE status='preparing' AND raw_tx!=''`, time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339))
	return e
}

func (s *Store) RecordFailure(hash string, retainStatus bool, detail string) error {
	if len(detail) > 300 {
		detail = detail[:300]
	}
	var attempts int
	_ = s.DB.QueryRow(`SELECT attempts FROM treasury_jobs WHERE payment_hash=?`, strings.ToLower(hash)).Scan(&attempts)
	statusExpr := "'queued'"
	if retainStatus {
		statusExpr = "status"
	} else if attempts >= 7 {
		statusExpr = "'needs_review'"
	}
	q := `UPDATE treasury_jobs SET status=` + statusExpr + `,attempts=attempts+1,last_error=?,next_attempt_at=?,updated_at=? WHERE payment_hash=?`
	now := time.Now().UTC()
	delay := time.Duration(1<<min(6, attempts+1)) * time.Second
	_, e := s.DB.Exec(q, detail, now.Add(delay).Format(time.RFC3339), now.Format(time.RFC3339), strings.ToLower(hash))
	return e
}
func (s *Store) Review(admin int64, hash string) error {
	if admin != profile.AdminID || len(hash) != 66 {
		return errors.New("forbidden or invalid payment")
	}
	dest, e := s.ActiveDestination()
	if e != nil {
		return e
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	result, e := tx.Exec(`UPDATE treasury_jobs SET destination_version=?,status='queued',updated_at=? WHERE payment_hash=? AND status IN ('needs_review','awaiting_destination')`, dest.Version, time.Now().UTC().Format(time.RFC3339), strings.ToLower(hash))
	if e != nil {
		return e
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return errors.New("job not reviewable")
	}
	if _, e = tx.Exec(`INSERT INTO admin_audit(admin_id,action,detail,at_utc) VALUES(?,'treasury_job_review',?,?)`, admin, strings.ToLower(hash), time.Now().UTC().Format(time.RFC3339)); e != nil {
		return e
	}
	return tx.Commit()
}
