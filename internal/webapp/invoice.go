package webapp

import (
	"context"
	"database/sql"
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
	return b != nil && b.Store != nil && b.Deriver != nil && b.Chain != nil && b.ExpectedChainID != 0 && b.Confirmations > 0 && b.ReferenceVerified
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
	price, e := b.Store.Price()
	if e != nil {
		return Invoice{}, e
	}
	chainID, e := b.Chain.ChainID(ctx)
	if e != nil {
		return Invoice{}, e
	}
	if chainID != b.ExpectedChainID {
		return Invoice{}, errors.New("wrong BNB network")
	}
	head, e := b.Chain.Head(ctx)
	if e != nil {
		return Invoice{}, e
	}
	tx, e := b.Store.DB.BeginTx(ctx, nil)
	if e != nil {
		return Invoice{}, e
	}
	defer tx.Rollback()
	_, e = tx.ExecContext(ctx, `UPDATE web_invoices SET status='expired' WHERE user_id=? AND status='pending' AND expires_at<=?`, user, now.UTC().Format(time.RFC3339))
	if e != nil {
		return Invoice{}, e
	}
	var inv Invoice
	var created, expires string
	e = tx.QueryRowContext(ctx, `SELECT id,user_id,address,price_wei,chain_id,created_at,expires_at,start_block,status,tx_hash FROM web_invoices WHERE user_id=? AND status='pending'`, user).Scan(&inv.ID, &inv.UserID, &inv.Address, &inv.PriceWei, &inv.ChainID, &created, &expires, &inv.StartBlock, &inv.Status, &inv.TxHash)
	if e == nil {
		inv.CreatedAt, _ = time.Parse(time.RFC3339, created)
		inv.ExpiresAt, _ = time.Parse(time.RFC3339, expires)
		return inv, tx.Commit()
	}
	if e != sql.ErrNoRows {
		return Invoice{}, e
	}
	var index uint32
	var address string
	e = tx.QueryRowContext(ctx, `SELECT derivation_index,address FROM web_wallets WHERE user_id=?`, user).Scan(&index, &address)
	if e == sql.ErrNoRows {
		var next int64
		if e = tx.QueryRowContext(ctx, `SELECT next_index FROM wallet_sequence WHERE id=1`).Scan(&next); e != nil {
			return Invoice{}, e
		}
		if next < 0 || next >= 1<<31 {
			return Invoice{}, errors.New("wallet index exhausted")
		}
		index = uint32(next)
		address, e = b.Deriver.Address(index)
		if e != nil {
			return Invoice{}, e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE wallet_sequence SET next_index=next_index+1 WHERE id=1`); e != nil {
			return Invoice{}, e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO web_wallets(user_id,derivation_index,address) VALUES(?,?,?)`, user, index, address); e != nil {
			return Invoice{}, e
		}
	} else if e != nil {
		return Invoice{}, e
	}
	id := profile.NewID()
	if id == "" {
		return Invoice{}, errors.New("random unavailable")
	}
	inv = Invoice{ID: id, UserID: user, Address: address, PriceWei: price.String(), ChainID: chainID, CreatedAt: now.UTC(), ExpiresAt: now.UTC().Add(15 * time.Minute), StartBlock: head + 1, Status: "pending"}
	_, e = tx.ExecContext(ctx, `INSERT INTO web_invoices(id,user_id,address,price_wei,chain_id,created_at,expires_at,start_block,status) VALUES(?,?,?,?,?,?,?,?,?)`, inv.ID, user, address, inv.PriceWei, chainID, inv.CreatedAt.Format(time.RFC3339), inv.ExpiresAt.Format(time.RFC3339), inv.StartBlock, inv.Status)
	if e != nil {
		return Invoice{}, e
	}
	return inv, tx.Commit()
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
