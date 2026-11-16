package webapp

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"time"
	"tradebot/internal/bnb"
	"tradebot/internal/profile"
)

type Invoice struct {
	ID         string    `json:"id"`
	UserID     int64     `json:"-"`
	PlanID     string    `json:"plan_id"`
	Address    string    `json:"address"`
	PriceWei   string    `json:"price_wei"`
	ChainID    uint64    `json:"chain_id"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	StartBlock uint64    `json:"-"`
	Status     string    `json:"status"`
	TxHash     string    `json:"tx_hash,omitempty"`
}
type Billing struct {
	Store                 *Store
	Profile               *profile.Store
	Deriver               *bnb.Deriver
	Chain                 bnb.Chain
	ExpectedChainID       uint64
	Confirmations         uint64
	ReferenceVerified     bool
	TestInvoicesEnabled   bool
	TestEmail             string
	PublicInvoicesEnabled bool
}

func (b *Billing) Ready() bool {
	return b != nil && b.Store != nil && b.profileStore() != nil && b.Chain != nil && b.ExpectedChainID == 56 && b.Confirmations > 0
}
func (b *Billing) profileStore() *profile.Store {
	if b == nil || b.Store == nil {
		return nil
	}
	if b.Profile != nil {
		return b.Profile
	}
	return &profile.Store{DB: b.Store.DB}
}
func (b *Billing) MonitorReady() bool {
	return b != nil && b.Store != nil && b.Chain != nil && b.ExpectedChainID != 0 && b.Confirmations > 0
}
func (b *Billing) CreateInvoice(ctx context.Context, user int64, now time.Time) (Invoice, error) {
	if !b.Ready() {
		return Invoice{}, errors.New("BNB billing not configured")
	}
	var email string
	if e := b.Store.DB.QueryRowContext(ctx, `SELECT email FROM web_users WHERE id=?`, user).Scan(&email); e != nil {
		return Invoice{}, e
	}
	if !b.PublicInvoicesEnabled && (!b.TestInvoicesEnabled || b.TestEmail == "" || normalizeEmail(email) != normalizeEmail(b.TestEmail)) {
		return Invoice{}, errors.New("public invoicing disabled")
	}
	profiles := b.profileStore()
	telegramID, e := profiles.TelegramForWebUser(user)
	if e != nil {
		return Invoice{}, errors.New("verified Telegram account required")
	}
	wallet, e := profiles.WalletRecordForTelegram(telegramID)
	if e != nil {
		return Invoice{}, errors.New("Telegram wallet unavailable")
	}
	if !wallet.Signable {
		return Invoice{}, errors.New("wallet is not eligible for checkout")
	}
	price, e := b.Store.Price()
	if e != nil {
		return Invoice{}, e
	}
	chainID, e := b.Chain.ChainID(ctx)
	if e != nil {
		return Invoice{}, e
	}
	if chainID != 56 || chainID != b.ExpectedChainID {
		return Invoice{}, errors.New("wrong BNB network")
	}
	head, e := b.Chain.Head(ctx)
	if e != nil {
		return Invoice{}, e
	}
	shared, e := profiles.CreateTelegramInvoice(telegramID, wallet.Address, price.String(), chainID, now.UTC().Add(15*time.Minute), head+1)
	if e != nil {
		return Invoice{}, e
	}
	return Invoice{ID: shared.ID, UserID: user, PlanID: shared.PlanID, Address: shared.Address, PriceWei: shared.PriceWei, ChainID: shared.ChainID, CreatedAt: shared.CreatedAt, ExpiresAt: shared.ExpiresAt, StartBlock: shared.StartBlock, Status: shared.Status, TxHash: shared.TxHash}, nil
}

func (b *Billing) InvoiceForWebUser(user int64, id string) (Invoice, error) {
	profiles := b.profileStore()
	if profiles == nil {
		return Invoice{}, errors.New("billing profile unavailable")
	}
	telegramID, err := profiles.TelegramForWebUser(user)
	if err != nil {
		return Invoice{}, err
	}
	shared, err := profiles.TelegramInvoice(telegramID, id)
	if err != nil {
		return Invoice{}, err
	}
	return Invoice{ID: shared.ID, UserID: user, PlanID: shared.PlanID, Address: shared.Address, PriceWei: shared.PriceWei, ChainID: shared.ChainID, CreatedAt: shared.CreatedAt, ExpiresAt: shared.ExpiresAt, StartBlock: shared.StartBlock, Status: shared.Status, TxHash: shared.TxHash}, nil
}
func (s *Store) Invoice(user int64, id string) (Invoice, error) {
	var v Invoice
	var created, expires string
	e := s.DB.QueryRow(`SELECT id,user_id,address,price_wei,chain_id,created_at,expires_at,start_block,status,tx_hash FROM web_invoices WHERE user_id=? AND id=?`, user, id).Scan(&v.ID, &v.UserID, &v.Address, &v.PriceWei, &v.ChainID, &created, &expires, &v.StartBlock, &v.Status, &v.TxHash)
	v.CreatedAt, _ = time.Parse(time.RFC3339, created)
	v.ExpiresAt, _ = time.Parse(time.RFC3339, expires)
	if e == nil && v.Status == "pending" && time.Now().After(v.ExpiresAt) {
		_, _ = s.DB.Exec(`UPDATE web_invoices SET status='expired' WHERE id=? AND status='pending'`, v.ID)
		v.Status = "expired"
	}
	return v, e
}

type ReviewPayment struct {
	TxHash, InvoiceID, Address, AmountWei, BlockTime, Classification string
	BlockNumber                                                      uint64
	DuplicateCount                                                   int
}

func (s *Store) ReviewPayments() ([]ReviewPayment, error) {
	rows, e := s.DB.Query(`SELECT tx_hash,invoice_id,address,amount_wei,block_number,block_time,classification,duplicate_count FROM web_payment_events WHERE classification!='paid' OR duplicate_count>0 ORDER BY seen_at DESC LIMIT 100`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []ReviewPayment
	for rows.Next() {
		var p ReviewPayment
		if e = rows.Scan(&p.TxHash, &p.InvoiceID, &p.Address, &p.AmountWei, &p.BlockNumber, &p.BlockTime, &p.Classification, &p.DuplicateCount); e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func validHexHash(h string) bool {
	if len(h) != 66 || !strings.HasPrefix(h, "0x") {
		return false
	}
	for _, c := range h[2:] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
func valueWei(hexValue string) (*big.Int, error) {
	if !strings.HasPrefix(hexValue, "0x") {
		return nil, errors.New("invalid transaction value")
	}
	n, ok := new(big.Int).SetString(hexValue[2:], 16)
	if !ok || n.Sign() < 0 {
		return nil, errors.New("invalid transaction value")
	}
	return n, nil
}
