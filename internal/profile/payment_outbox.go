package profile

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type PaymentNotification struct {
	ID                                                            int64
	TelegramID                                                    int64
	InvoiceID, TxHash, Kind, AmountWei, PlanID, SubscriptionUntil string
	Attempts                                                      int
}

func (s *Store) ClaimPaymentNotifications(now time.Time, limit int) ([]PaymentNotification, error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New("invalid outbox limit")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	nowRaw := now.UTC().Format(time.RFC3339)
	_, err = tx.Exec(`UPDATE payment_notification_outbox SET status='retry',locked_until='' WHERE status='sending' AND locked_until<=?`, nowRaw)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(`SELECT id,telegram_id,invoice_id,tx_hash,kind,amount_wei,plan_id,subscription_until,attempts FROM payment_notification_outbox WHERE status IN ('pending','retry') AND next_attempt_at<=? ORDER BY id LIMIT ?`, nowRaw, limit)
	if err != nil {
		return nil, err
	}
	var out []PaymentNotification
	for rows.Next() {
		var n PaymentNotification
		if err = rows.Scan(&n.ID, &n.TelegramID, &n.InvoiceID, &n.TxHash, &n.Kind, &n.AmountWei, &n.PlanID, &n.SubscriptionUntil, &n.Attempts); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, n)
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		res, e := tx.Exec(`UPDATE payment_notification_outbox SET status='sending',attempts=attempts+1,locked_until=? WHERE id=? AND status IN ('pending','retry')`, now.Add(time.Minute).UTC().Format(time.RFC3339), out[i].ID)
		if e != nil {
			return nil, e
		}
		changed, _ := res.RowsAffected()
		if changed != 1 {
			return nil, fmt.Errorf("outbox claim conflict: %d", out[i].ID)
		}
		out[i].Attempts++
	}
	return out, tx.Commit()
}

func (s *Store) CompletePaymentNotification(id int64, delivered bool, detail string, now time.Time) error {
	if delivered {
		_, err := s.DB.Exec(`UPDATE payment_notification_outbox SET status='delivered',delivered_at=?,locked_until='',last_error='' WHERE id=? AND status='sending'`, now.UTC().Format(time.RFC3339), id)
		return err
	}
	var attempts int
	if err := s.DB.QueryRow(`SELECT attempts FROM payment_notification_outbox WHERE id=?`, id).Scan(&attempts); err != nil {
		return err
	}
	delay := time.Duration(1<<min(attempts, 8)) * time.Second
	_, err := s.DB.Exec(`UPDATE payment_notification_outbox SET status='retry',next_attempt_at=?,locked_until='',last_error=? WHERE id=? AND status='sending'`, now.Add(delay).UTC().Format(time.RFC3339), truncateOutboxError(detail), id)
	return err
}

func truncateOutboxError(v string) string {
	if len(v) > 300 {
		return v[:300]
	}
	return v
}

func EnqueuePaymentNotificationTx(tx *sql.Tx, telegramID int64, invoiceID, txHash, kind, amountWei, planID, until string, now time.Time) error {
	_, err := tx.Exec(`INSERT OR IGNORE INTO payment_notification_outbox(event_key,telegram_id,invoice_id,tx_hash,kind,amount_wei,plan_id,subscription_until,next_attempt_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, txHash+":"+kind, telegramID, invoiceID, txHash, kind, amountWei, planID, until, now.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	return err
}
