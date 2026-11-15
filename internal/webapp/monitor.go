package webapp

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"math/big"
	"strings"
	"time"
	"tradebot/internal/bnb"
)

func (b *Billing) ScanOnce(ctx context.Context) (int, error) {
	if !b.MonitorReady() {
		return 0, errors.New("BNB monitor not configured")
	}
	id, e := b.Chain.ChainID(ctx)
	if e != nil {
		return 0, e
	}
	if id != b.ExpectedChainID {
		return 0, errors.New("wrong BNB network")
	}
	if _, e = b.Store.DB.ExecContext(ctx, `UPDATE web_invoices SET status='expired' WHERE status='pending' AND expires_at<?`, time.Now().UTC().Format(time.RFC3339)); e != nil {
		return 0, e
	}
	head, e := b.Chain.Head(ctx)
	if e != nil {
		return 0, e
	}
	if head+1 < b.Confirmations {
		return 0, nil
	}
	safe := head - (b.Confirmations - 1)
	var min sql.NullInt64
	e = b.Store.DB.QueryRow(`SELECT MIN(start_block) FROM web_invoices`).Scan(&min)
	if e != nil {
		return 0, e
	}
	if !min.Valid {
		var telegramPending int
		_ = b.Store.DB.QueryRow(`SELECT count(*) FROM telegram_invoices WHERE status IN ('pending','expired')`).Scan(&telegramPending)
		if telegramPending == 0 {
			return 0, nil
		}
		lookback := uint64(500)
		if safe < lookback {
			lookback = safe
		}
		min.Valid = true
		min.Int64 = int64(safe - lookback)
	}
	var cursor int64
	e = b.Store.DB.QueryRow(`SELECT cursor FROM web_chain_scan WHERE id=1`).Scan(&cursor)
	if e == sql.ErrNoRows {
		cursor = min.Int64 - 1
		_, e = b.Store.DB.Exec(`INSERT INTO web_chain_scan(id,cursor) VALUES(1,?)`, cursor)
	}
	if e != nil {
		return 0, e
	}
	if cursor >= 0 && safe <= uint64(cursor) {
		return 0, nil
	}
	start := uint64(cursor + 1)
	limit := safe
	if limit > start+99 {
		limit = start + 99
	}
	processed := 0
	for n := start; n <= limit; n++ {
		block, e := b.Chain.Block(ctx, n)
		if e != nil {
			return processed, e
		}
		if block.Number != n {
			return processed, errors.New("block mismatch")
		}
		for _, tx := range block.Transactions {
			if tx.To == "" {
				continue
			}
			var user int64
			e = b.Store.DB.QueryRow(`SELECT user_id FROM web_wallets WHERE lower(address)=lower(?) UNION ALL SELECT telegram_id FROM telegram_wallets WHERE lower(address)=lower(?) LIMIT 1`, tx.To, tx.To).Scan(&user)
			if e == sql.ErrNoRows {
				continue
			}
			if e != nil {
				return processed, e
			}
			receipt, e := b.Chain.Receipt(ctx, tx.Hash)
			if e != nil {
				return processed, e
			}
			if receipt.BlockNumber != n || !strings.EqualFold(receipt.BlockHash, block.Hash) || !strings.EqualFold(tx.BlockHash, block.Hash) || tx.BlockNumber != n {
				return processed, errors.New("transaction receipt block mismatch")
			}
			value, e := valueWei(tx.Value)
			if e != nil {
				return processed, e
			}
			classification := ""
			if receipt.Status != 1 {
				classification = "failed_tx"
			}
			if tx.Input != "" && tx.Input != "0x" {
				classification = "contract_call"
			}
			if e = b.apply(ctx, tx, block, value, classification); e != nil {
				return processed, e
			}
			processed++
		}
		if _, e = b.Store.DB.ExecContext(ctx, `UPDATE web_chain_scan SET cursor=? WHERE id=1`, n); e != nil {
			return processed, e
		}
	}
	return processed, nil
}

func (b *Billing) apply(ctx context.Context, transaction bnb.Transaction, block bnb.Block, value *big.Int, preclassified string) error {
	if !validHexHash(transaction.Hash) {
		return errors.New("invalid transaction hash")
	}
	tx, e := b.Store.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	res, e := tx.ExecContext(ctx, `INSERT OR IGNORE INTO web_payment_events(tx_hash,address,amount_wei,block_number,block_time,classification,seen_at) VALUES(?,?,?,?,?,'observing',?)`, strings.ToLower(transaction.Hash), transaction.To, value.String(), block.Number, block.Time.UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339))
	if e != nil {
		return e
	}
	inserted, _ := res.RowsAffected()
	if inserted == 0 {
		_, e = tx.ExecContext(ctx, `UPDATE web_payment_events SET duplicate_count=duplicate_count+1,seen_at=? WHERE tx_hash=?`, time.Now().UTC().Format(time.RFC3339), strings.ToLower(transaction.Hash))
		if e != nil {
			return e
		}
		return tx.Commit()
	}
	classification := preclassified
	matchID := ""
	userID := int64(0)
	if classification == "" {
		telegramRows, te := tx.QueryContext(ctx, `SELECT id,telegram_id,price_wei,chain_id,created_at,expires_at,status FROM telegram_invoices WHERE lower(address)=lower(?) ORDER BY created_at ASC`, transaction.To)
		if te != nil {
			return te
		}
		for telegramRows.Next() {
			var id, price, created, expiry, status string
			var uid int64
			var invoiceChain uint64
			if te = telegramRows.Scan(&id, &uid, &price, &invoiceChain, &created, &expiry, &status); te != nil {
				telegramRows.Close()
				return te
			}
			start, _ := time.Parse(time.RFC3339, created)
			end, _ := time.Parse(time.RFC3339, expiry)
			expected, ok := new(big.Int).SetString(price, 10)
			if invoiceChain == b.ExpectedChainID && !block.Time.Before(start) && !block.Time.After(end) && ok && expected.Cmp(value) == 0 && (status == "pending" || status == "expired") {
				matchID = id
				userID = uid
				classification = "paid_telegram"
				break
			}
		}
		if te = telegramRows.Err(); te != nil {
			telegramRows.Close()
			return te
		}
		telegramRows.Close()
		if classification == "paid_telegram" {
			res, ue := tx.ExecContext(ctx, `UPDATE telegram_invoices SET status='paid',tx_hash=? WHERE id=? AND status IN ('pending','expired')`, strings.ToLower(transaction.Hash), matchID)
			if ue != nil {
				return ue
			}
			changed, _ := res.RowsAffected()
			if changed != 1 {
				return errors.New("telegram invoice changed during settlement")
			}
			res, ue = tx.ExecContext(ctx, `INSERT OR IGNORE INTO payment_events(charge_id,user_id,kind,created_at) VALUES(?,?,?,?)`, strings.ToLower(transaction.Hash), userID, "bnb_telegram", block.Time.UTC().Format(time.RFC3339))
			if ue != nil {
				return ue
			}
			inserted, _ := res.RowsAffected()
			if inserted == 1 {
				var prev string
				_ = tx.QueryRowContext(ctx, `SELECT subscription_until FROM entitlements WHERE user_id=?`, userID).Scan(&prev)
				base := block.Time.UTC()
				if p, pe := time.Parse(time.RFC3339, prev); pe == nil && p.After(base) {
					base = p
				}
				if _, ue = tx.ExecContext(ctx, `INSERT INTO entitlements(user_id,subscription_until) VALUES(?,?) ON CONFLICT(user_id) DO UPDATE SET subscription_until=excluded.subscription_until`, userID, base.AddDate(0, 1, 0).Format(time.RFC3339)); ue != nil {
					return ue
				}
			}
			var destinationVersion int
			_ = tx.QueryRowContext(ctx, `SELECT version FROM treasury_destinations WHERE active=1`).Scan(&destinationVersion)
			jobStatus := "awaiting_destination"
			if destinationVersion > 0 {
				jobStatus = "queued"
			}
			if _, ue = tx.ExecContext(ctx, `INSERT OR IGNORE INTO treasury_jobs(payment_hash,source,amount_wei,destination_version,status,updated_at) VALUES(?,?,?,?,?,?)`, strings.ToLower(transaction.Hash), transaction.To, value.String(), destinationVersion, jobStatus, time.Now().UTC().Format(time.RFC3339)); ue != nil {
				return ue
			}
			if _, ue = tx.ExecContext(ctx, `UPDATE web_payment_events SET invoice_id=?,classification=? WHERE tx_hash=?`, matchID, classification, strings.ToLower(transaction.Hash)); ue != nil {
				return ue
			}
			return tx.Commit()
		}
		rows, e := tx.QueryContext(ctx, `SELECT id,user_id,price_wei,chain_id,created_at,expires_at,status FROM web_invoices WHERE lower(address)=lower(?) ORDER BY created_at ASC`, transaction.To)
		if e != nil {
			return e
		}
		anyInvoice := false
		inWindow := false
		for rows.Next() {
			anyInvoice = true
			var id, price, created, expiry, status string
			var uid int64
			var invoiceChain uint64
			if e = rows.Scan(&id, &uid, &price, &invoiceChain, &created, &expiry, &status); e != nil {
				rows.Close()
				return e
			}
			start, _ := time.Parse(time.RFC3339, created)
			end, _ := time.Parse(time.RFC3339, expiry)
			if invoiceChain != b.ExpectedChainID {
				classification = "wrong_network"
				continue
			}
			if block.Time.Before(start) || block.Time.After(end) {
				continue
			}
			inWindow = true
			expected, ok := new(big.Int).SetString(price, 10)
			if !ok {
				rows.Close()
				return errors.New("invalid invoice amount")
			}
			if expected.Cmp(value) != 0 {
				classification = "amount_mismatch"
				continue
			}
			if status == "pending" || status == "expired" {
				matchID = id
				userID = uid
				classification = "paid"
				break
			}
			classification = "already_paid"
		}
		if e = rows.Err(); e != nil {
			rows.Close()
			return e
		}
		rows.Close()
		if classification == "" {
			if inWindow {
				classification = "ambiguous"
			} else if anyInvoice {
				classification = "late_or_outside_window"
			} else {
				classification = "unmatched"
			}
		}
	}
	if classification == "paid" {
		var prev string
		if e = tx.QueryRowContext(ctx, `SELECT subscription_until FROM web_users WHERE id=?`, userID).Scan(&prev); e != nil {
			return e
		}
		base := time.Now().UTC()
		if t, e := time.Parse(time.RFC3339, prev); e == nil && t.After(base) {
			base = t
		}
		res, e = tx.ExecContext(ctx, `UPDATE web_invoices SET status='paid',tx_hash=? WHERE id=? AND status IN ('pending','expired')`, strings.ToLower(transaction.Hash), matchID)
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return errors.New("invoice changed during settlement")
		}
		_, e = tx.ExecContext(ctx, `UPDATE web_users SET subscription_until=? WHERE id=?`, base.AddDate(0, 1, 0).Format(time.RFC3339), userID)
		if e != nil {
			return e
		}
		var destinationVersion int
		_ = tx.QueryRowContext(ctx, `SELECT version FROM treasury_destinations WHERE active=1`).Scan(&destinationVersion)
		jobStatus := "awaiting_destination"
		if destinationVersion > 0 {
			jobStatus = "queued"
		}
		if _, e = tx.ExecContext(ctx, `INSERT OR IGNORE INTO treasury_jobs(payment_hash,source,amount_wei,destination_version,status,updated_at) VALUES(?,?,?,?,?,?)`, strings.ToLower(transaction.Hash), transaction.To, value.String(), destinationVersion, jobStatus, time.Now().UTC().Format(time.RFC3339)); e != nil {
			return e
		}
	}
	_, e = tx.ExecContext(ctx, `UPDATE web_payment_events SET invoice_id=?,classification=? WHERE tx_hash=?`, matchID, classification, strings.ToLower(transaction.Hash))
	if e != nil {
		return e
	}
	return tx.Commit()
}
func (b *Billing) Run(ctx context.Context, period time.Duration) {
	if period <= 0 {
		period = 15 * time.Second
	}
	timer := time.NewTicker(period)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			n, e := b.ScanOnce(ctx)
			if e != nil {
				slog.Warn("bnb_scan_failed", "error", e.Error())
			} else if n > 0 {
				slog.Info("bnb_transactions_checked", "count", n)
			}
		}
	}
}
