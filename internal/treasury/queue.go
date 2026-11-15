package treasury

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"time"

	"tradebot/internal/bnb"
)

type Chain interface {
	ChainID(context.Context) (uint64, error)
	Balance(context.Context, string) (*big.Int, error)
	PendingNonce(context.Context, string) (uint64, error)
	GasPrice(context.Context) (*big.Int, error)
	TxStatus(context.Context, string) (known, final, success bool, e error)
	SendRaw(context.Context, string) error
}
type Worker struct {
	Store           *Store
	Chain           Chain
	Signer          bnb.Signer
	ExpectedChainID uint64
}

func (w *Worker) Tick(ctx context.Context) error {
	if w == nil || w.Store == nil || w.Chain == nil || w.Signer == nil {
		return errors.New("treasury worker unavailable")
	}
	id, e := w.Chain.ChainID(ctx)
	if e != nil {
		return e
	}
	if id != w.ExpectedChainID {
		return errors.New("treasury network mismatch")
	}
	j, e := w.Store.NextJob()
	if e != nil {
		return e
	}
	if j.Status == "queued" {
		return w.prepare(ctx, j, id)
	}
	return w.reconcile(ctx, j)
}
func (w *Worker) prepare(ctx context.Context, j Job, chainID uint64) error {
	dest, e := w.Store.ActiveDestination()
	if e != nil {
		return e
	}
	if dest.Version != j.DestinationVersion {
		_, e = w.Store.DB.Exec(`UPDATE treasury_jobs SET status='needs_review',updated_at=? WHERE payment_hash=?`, time.Now().UTC().Format(time.RFC3339), j.PaymentHash)
		return e
	}
	var other int
	if e = w.Store.DB.QueryRow(`SELECT count(*) FROM treasury_jobs WHERE source=? AND payment_hash!=? AND status IN ('preparing','signed','broadcast','broadcast_unknown')`, j.Source, j.PaymentHash).Scan(&other); e != nil {
		return e
	}
	if other > 0 {
		return nil
	}
	if j.Status == "queued" {
		result, claimErr := w.Store.DB.Exec(`UPDATE treasury_jobs SET status='preparing',updated_at=? WHERE payment_hash=? AND status='queued'`, time.Now().UTC().Format(time.RFC3339), j.PaymentHash)
		if claimErr != nil {
			return claimErr
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			return nil
		}
	}
	balance, e := w.Chain.Balance(ctx, j.Source)
	if e != nil {
		return e
	}
	gasPrice, e := w.Chain.GasPrice(ctx)
	if e != nil {
		return e
	}
	if gasPrice.Sign() <= 0 {
		return errors.New("invalid gas price")
	}
	fee := new(big.Int).Mul(gasPrice, big.NewInt(21000))
	deposit, ok := new(big.Int).SetString(j.AmountWei, 10)
	if !ok {
		return errors.New("invalid payment amount")
	}
	available := new(big.Int).Set(balance)
	if available.Cmp(deposit) > 0 {
		available.Set(deposit)
	}
	amount := available.Sub(available, fee)
	if amount.Sign() <= 0 {
		_, e = w.Store.DB.Exec(`UPDATE treasury_jobs SET status='needs_review',updated_at=? WHERE payment_hash=?`, time.Now().UTC().Format(time.RFC3339), j.PaymentHash)
		return e
	}
	nonce, e := w.Chain.PendingNonce(ctx, j.Source)
	if e != nil {
		return e
	}
	signed, e := w.Signer.Sign(ctx, bnb.SignRequest{PaymentHash: j.PaymentHash, Source: j.Source, Destination: dest.Address, DestinationVersion: dest.Version, Nonce: nonce, ChainID: chainID, AmountWei: amount.String(), GasPriceWei: gasPrice.String(), GasLimit: 21000})
	if e != nil {
		return e
	}
	if !strings.HasPrefix(signed.Raw, "0x") || len(signed.Hash) != 66 {
		return errors.New("invalid signer response")
	}
	_, e = w.Store.DB.Exec(`UPDATE treasury_jobs SET status='signed',nonce=?,raw_tx=?,tx_hash=?,fee_wei=?,updated_at=? WHERE payment_hash=? AND status='preparing' AND destination_version=?`, nonce, signed.Raw, strings.ToLower(signed.Hash), fee.String(), time.Now().UTC().Format(time.RFC3339), j.PaymentHash, dest.Version)
	return e
}
func (w *Worker) reconcile(ctx context.Context, j Job) error {
	known, final, success, e := w.Chain.TxStatus(ctx, j.TxHash)
	if e != nil {
		return e
	}
	status := "broadcast"
	if final {
		if success {
			status = "transferred"
		} else {
			status = "needs_review"
		}
	} else if !known {
		if e = w.Chain.SendRaw(ctx, j.RawTx); e != nil {
			status = "broadcast_unknown"
		}
	}
	_, saveErr := w.Store.DB.Exec(`UPDATE treasury_jobs SET status=?,updated_at=? WHERE payment_hash=? AND status IN ('signed','broadcast','broadcast_unknown')`, status, time.Now().UTC().Format(time.RFC3339), j.PaymentHash)
	if saveErr != nil {
		return saveErr
	}
	return e
}
func (w *Worker) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = w.Tick(ctx)
		}
	}
}
