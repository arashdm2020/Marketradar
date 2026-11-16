package profile

import (
	"errors"
	"time"
)

type PaymentEventView struct {
	TxHash                string `json:"tx_hash"`
	AmountWei             string `json:"amount_wei"`
	Classification        string `json:"classification"`
	BlockTime             string `json:"block_time"`
	ConfirmationCount     int    `json:"confirmation_count"`
	ConfirmedAt           string `json:"confirmed_at"`
	SubscriptionActivated bool   `json:"subscription_activated"`
	SubscriptionUntil     string `json:"subscription_until"`
}

type BillingInvoiceView struct {
	TelegramInvoice
	Payments              []PaymentEventView `json:"payments"`
	SubscriptionActivated bool               `json:"subscription_activated"`
	SubscriptionUntil     string             `json:"subscription_until"`
}

// PageTelegramBilling returns only invoices owned by the canonical Telegram
// identity, enriched with persisted reconciliation and activation data.
func (s *Store) PageTelegramBilling(telegramID int64, limit, offset int, status string) ([]BillingInvoiceView, int, error) {
	if limit < 1 || limit > 20 || offset < 0 {
		return nil, 0, errors.New("invalid pagination")
	}
	switch status {
	case "", "pending", "paid", "expired", "superseded", "failed":
	default:
		return nil, 0, errors.New("invalid invoice status")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := s.DB.Exec(`UPDATE telegram_invoices SET status='expired',updated_at=? WHERE telegram_id=? AND status='pending' AND expires_at<=?`, now, telegramID, now); err != nil {
		return nil, 0, err
	}
	var total int
	if err := s.DB.QueryRow(`SELECT count(*) FROM telegram_invoices WHERE telegram_id=? AND (?='' OR status=?)`, telegramID, status, status).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.DB.Query(`SELECT id,telegram_id,plan_id,address,price_wei,chain_id,created_at,expires_at,start_block,status,tx_hash,updated_at FROM telegram_invoices WHERE telegram_id=? AND (?='' OR status=?) ORDER BY created_at DESC LIMIT ? OFFSET ?`, telegramID, status, status, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	out := make([]BillingInvoiceView, 0, limit)
	for rows.Next() {
		var item BillingInvoiceView
		var created, expires, updated string
		if err = rows.Scan(&item.ID, &item.TelegramID, &item.PlanID, &item.Address, &item.PriceWei, &item.ChainID, &created, &expires, &item.StartBlock, &item.Status, &item.TxHash, &updated); err != nil {
			rows.Close()
			return nil, 0, err
		}
		item.CreatedAt, _ = time.Parse(time.RFC3339, created)
		item.ExpiresAt, _ = time.Parse(time.RFC3339, expires)
		item.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
		item.Payments = make([]PaymentEventView, 0)
		out = append(out, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, 0, err
	}
	if err = rows.Close(); err != nil {
		return nil, 0, err
	}
	// The SQLite pool intentionally uses one connection; enrich only after the
	// invoice cursor is closed so this remains deadlock-free.
	for i := range out {
		item := &out[i]
		payments, queryErr := s.DB.Query(`SELECT p.tx_hash,p.amount_wei,p.classification,p.block_time,p.confirmation_count,p.confirmed_at,p.subscription_activated,COALESCE((SELECT n.subscription_until FROM payment_notification_outbox n WHERE n.invoice_id=p.invoice_id AND n.kind='subscription_activated' ORDER BY n.id DESC LIMIT 1),'') FROM web_payment_events p WHERE p.invoice_id=? OR (?<>'' AND p.tx_hash=?) ORDER BY p.block_number DESC,p.seen_at DESC LIMIT 50`, item.ID, item.TxHash, item.TxHash)
		if queryErr != nil {
			return nil, 0, queryErr
		}
		for payments.Next() {
			var payment PaymentEventView
			var activated int
			if queryErr = payments.Scan(&payment.TxHash, &payment.AmountWei, &payment.Classification, &payment.BlockTime, &payment.ConfirmationCount, &payment.ConfirmedAt, &activated, &payment.SubscriptionUntil); queryErr != nil {
				payments.Close()
				return nil, 0, queryErr
			}
			payment.SubscriptionActivated = activated == 1
			item.SubscriptionActivated = item.SubscriptionActivated || payment.SubscriptionActivated
			if item.SubscriptionUntil == "" && payment.SubscriptionUntil != "" {
				item.SubscriptionUntil = payment.SubscriptionUntil
			}
			item.Payments = append(item.Payments, payment)
		}
		if queryErr = payments.Err(); queryErr != nil {
			payments.Close()
			return nil, 0, queryErr
		}
		if queryErr = payments.Close(); queryErr != nil {
			return nil, 0, queryErr
		}
	}
	return out, total, nil
}

type WalletActivity struct {
	InvoiceID             string `json:"invoice_id"`
	PlanID                string `json:"plan_id"`
	InvoiceStatus         string `json:"invoice_status"`
	TxHash                string `json:"tx_hash"`
	AmountWei             string `json:"amount_wei"`
	ReconciliationStatus  string `json:"reconciliation_status"`
	BlockTime             string `json:"block_time"`
	Confirmations         int    `json:"confirmations"`
	SubscriptionActivated bool   `json:"subscription_activated"`
	SubscriptionUntil     string `json:"subscription_until"`
}

func (s *Store) WalletActivity(telegramID int64, limit, offset int) ([]WalletActivity, int, error) {
	if limit < 1 || limit > 100 || offset < 0 {
		return nil, 0, errors.New("invalid pagination")
	}
	wallet, err := s.WalletRecordForTelegram(telegramID)
	if err != nil {
		return nil, 0, err
	}
	var total int
	if err = s.DB.QueryRow(`SELECT count(*) FROM web_payment_events WHERE lower(address)=lower(?)`, wallet.Address).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.DB.Query(`SELECT COALESCE(p.invoice_id,''),COALESCE(i.plan_id,''),COALESCE(i.status,''),p.tx_hash,p.amount_wei,p.classification,p.block_time,p.confirmation_count,p.subscription_activated,COALESCE((SELECT n.subscription_until FROM payment_notification_outbox n WHERE n.invoice_id=p.invoice_id AND n.kind='subscription_activated' ORDER BY n.id DESC LIMIT 1),'') FROM web_payment_events p LEFT JOIN telegram_invoices i ON i.id=p.invoice_id WHERE lower(p.address)=lower(?) ORDER BY p.block_number DESC,p.seen_at DESC LIMIT ? OFFSET ?`, wallet.Address, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := make([]WalletActivity, 0, limit)
	for rows.Next() {
		var item WalletActivity
		var activated int
		if err = rows.Scan(&item.InvoiceID, &item.PlanID, &item.InvoiceStatus, &item.TxHash, &item.AmountWei, &item.ReconciliationStatus, &item.BlockTime, &item.Confirmations, &activated, &item.SubscriptionUntil); err != nil {
			return nil, 0, err
		}
		item.SubscriptionActivated = activated == 1
		out = append(out, item)
	}
	return out, total, rows.Err()
}
