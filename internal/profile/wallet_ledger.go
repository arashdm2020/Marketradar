package profile

import "errors"

type WalletRecord struct {
	TelegramID int64  `json:"telegram_id"`
	Address    string `json:"address"`
	Index      int64  `json:"index"`
	CreatedAt  string `json:"created_at"`
	Signable   bool   `json:"signable"`
}

type WalletTransaction struct {
	InvoiceID            string `json:"invoice_id"`
	TxHash               string `json:"tx_hash"`
	AmountWei            string `json:"amount_wei"`
	InvoiceStatus        string `json:"invoice_status"`
	ReconciliationStatus string `json:"reconciliation_status"`
	BlockTime            string `json:"block_time"`
	SweepStatus          string `json:"sweep_status"`
	Confirmations        int    `json:"confirmations"`
}

func (s *Store) WalletRecordForTelegram(telegramID int64) (WalletRecord, error) {
	var w WalletRecord
	var signable int
	err := s.DB.QueryRow(`SELECT telegram_id,address,derivation_index,created_at,signable FROM telegram_wallets WHERE telegram_id=?`, telegramID).Scan(&w.TelegramID, &w.Address, &w.Index, &w.CreatedAt, &signable)
	w.Signable = signable == 1
	return w, err
}

// WalletRecordForWeb returns the same Telegram-owned wallet only after the
// identities have been explicitly linked. It never provisions another wallet.
func (s *Store) WalletRecordForWeb(webUserID int64) (WalletRecord, error) {
	var telegramID int64
	if err := s.DB.QueryRow(`SELECT telegram_id FROM web_telegram_links WHERE web_user_id=?`, webUserID).Scan(&telegramID); err != nil {
		return WalletRecord{}, err
	}
	return s.WalletRecordForTelegram(telegramID)
}
func (s *Store) TelegramForWebUser(webUserID int64)(int64,error){var id int64;e:=s.DB.QueryRow(`SELECT telegram_id FROM web_telegram_links WHERE web_user_id=?`,webUserID).Scan(&id);return id,e}

func (s *Store) WalletTransactions(telegramID int64, limit, offset int) ([]WalletTransaction, int, error) {
	if limit < 1 || limit > 100 || offset < 0 {
		return nil, 0, errors.New("invalid pagination")
	}
	w, err := s.WalletRecordForTelegram(telegramID)
	if err != nil {
		return nil, 0, err
	}
	var total int
	if err = s.DB.QueryRow(`SELECT count(*) FROM telegram_invoices WHERE telegram_id=?`, telegramID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.DB.Query(`SELECT i.id,i.tx_hash,COALESCE(p.amount_wei,''),i.status,COALESCE(p.classification,''),COALESCE(p.block_time,''),COALESCE(p.confirmation_count,0),COALESCE(j.status,'') FROM telegram_invoices i LEFT JOIN web_payment_events p ON p.invoice_id=i.id OR (i.tx_hash!='' AND p.tx_hash=i.tx_hash) LEFT JOIN treasury_jobs j ON j.payment_hash=p.tx_hash WHERE i.telegram_id=? AND lower(i.address)=lower(?) ORDER BY i.created_at DESC LIMIT ? OFFSET ?`, telegramID, w.Address, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := make([]WalletTransaction, 0, limit)
	for rows.Next() {
		var x WalletTransaction
		if err = rows.Scan(&x.InvoiceID, &x.TxHash, &x.AmountWei, &x.InvoiceStatus, &x.ReconciliationStatus, &x.BlockTime, &x.Confirmations, &x.SweepStatus); err != nil {
			return nil, 0, err
		}
		out = append(out, x)
	}
	if err = rows.Err(); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}
