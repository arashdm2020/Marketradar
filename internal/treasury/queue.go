package treasury

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
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
	Store             *Store
	Chain             Chain
	Signer            bnb.Signer
	ExpectedChainID   uint64
	DiscoveryInterval time.Duration
}

// DiscoverBalances queues one sweep per signable Telegram deposit wallet that
// currently contains enough native BNB to pay a plain 21,000-gas transfer.
// An existing non-final job for the same source suppresses duplicate queueing.
func (w *Worker) DiscoverBalances(ctx context.Context) (int, error) {
	if w == nil || w.Store == nil || w.Chain == nil {
		return 0, errors.New("treasury worker unavailable")
	}
	chainID, e := w.Chain.ChainID(ctx)
	if e != nil {
		return 0, e
	}
	if chainID != w.ExpectedChainID {
		return 0, errors.New("treasury network mismatch")
	}
	dest, e := w.Store.ActiveDestination()
	if e != nil {
		return 0, e
	}
	gasPrice, e := w.Chain.GasPrice(ctx)
	if e != nil || gasPrice.Sign() <= 0 {
		return 0, errors.New("invalid gas price")
	}
	fee := new(big.Int).Mul(gasPrice, big.NewInt(21000))
	rows, e := w.Store.DB.Query(`SELECT address FROM telegram_wallets WHERE signable=1 AND length(private_key_cipher)>0 AND key_encryption_version=1 ORDER BY derivation_index`)
	if e != nil {
		return 0, e
	}
	var addresses []string
	for rows.Next() {
		var address string
		if e = rows.Scan(&address); e != nil {
			rows.Close()
			return 0, e
		}
		addresses = append(addresses, address)
	}
	if e = rows.Close(); e != nil {
		return 0, e
	}
	queued := 0
	for _, address := range addresses {
		var active int
		e = w.Store.DB.QueryRow(`SELECT count(*) FROM treasury_jobs WHERE source=? AND status IN ('queued','preparing','signed','broadcast','broadcast_unknown','insufficient_gas','needs_review','awaiting_destination','duplicate_hold')`, address).Scan(&active)
		if e != nil {
			return queued, e
		}
		if active != 0 {
			continue
		}
		balance, balanceErr := w.Chain.Balance(ctx, address)
		if balanceErr != nil {
			return queued, balanceErr
		}
		if balance.Cmp(fee) <= 0 {
			continue
		}
		now := time.Now().UTC()
		hash := sha256.Sum256([]byte(fmt.Sprintf("auto-sweep:%s:%s:%d", strings.ToLower(address), balance.String(), now.UnixNano())))
		jobID := fmt.Sprintf("0x%x", hash[:])
		tx, insertErr := w.Store.DB.Begin()
		if insertErr != nil {
			return queued, insertErr
		}
		if _, insertErr = tx.Exec(`INSERT INTO web_payment_events(tx_hash,address,amount_wei,block_number,block_time,classification,seen_at) VALUES(?,?,?,0,?,'balance_sweep',?)`, jobID, strings.ToLower(address), balance.String(), now.Format(time.RFC3339), now.Format(time.RFC3339)); insertErr != nil {
			tx.Rollback()
			return queued, insertErr
		}
		result, insertErr := tx.Exec(`INSERT INTO treasury_jobs(payment_hash,source,amount_wei,destination_version,status,fee_wei,updated_at) SELECT ?,?,?,?,'queued',?,? WHERE NOT EXISTS (SELECT 1 FROM treasury_jobs WHERE source=? AND status IN ('queued','preparing','signed','broadcast','broadcast_unknown','insufficient_gas','needs_review','awaiting_destination','duplicate_hold'))`, jobID, strings.ToLower(address), balance.String(), dest.Version, fee.String(), now.Format(time.RFC3339), strings.ToLower(address))
		if insertErr != nil {
			tx.Rollback()
			return queued, insertErr
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			tx.Rollback()
			continue
		}
		if insertErr = tx.Commit(); insertErr != nil {
			return queued, insertErr
		}
		if n == 1 {
			queued++
		}
	}
	return queued, nil
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
	if j.Status == "queued" || j.Status == "insufficient_gas" {
		e = w.prepare(ctx, j, id)
		if e != nil {
			_ = w.Store.RecordFailure(j.PaymentHash, false, e.Error())
		}
		return e
	}
	e = w.reconcile(ctx, j)
	if e != nil {
		_ = w.Store.RecordFailure(j.PaymentHash, true, e.Error())
	}
	return e
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
	if j.Status == "queued" || j.Status == "insufficient_gas" {
		result, claimErr := w.Store.DB.Exec(`UPDATE treasury_jobs SET status='preparing',updated_at=? WHERE payment_hash=? AND status IN ('queued','insufficient_gas')`, time.Now().UTC().Format(time.RFC3339), j.PaymentHash)
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
		now := time.Now().UTC()
		_, e = w.Store.DB.Exec(`UPDATE treasury_jobs SET status='insufficient_gas',last_error='balance cannot cover gas',next_attempt_at=?,updated_at=? WHERE payment_hash=?`, now.Add(30*time.Second).Format(time.RFC3339), now.Format(time.RFC3339), j.PaymentHash)
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
	result, e := w.Store.DB.Exec(`UPDATE treasury_jobs SET status='signed',nonce=?,raw_tx=?,tx_hash=?,fee_wei=?,last_error='',next_attempt_at='',updated_at=? WHERE payment_hash=? AND status='preparing' AND destination_version=?`, nonce, signed.Raw, strings.ToLower(signed.Hash), fee.String(), time.Now().UTC().Format(time.RFC3339), j.PaymentHash, dest.Version)
	if e != nil {
		if strings.Contains(strings.ToLower(e.Error()), "unique") {
			_, _ = w.Store.DB.Exec(`UPDATE treasury_jobs SET status='needs_review',last_error='nonce conflict',updated_at=? WHERE payment_hash=?`, time.Now().UTC().Format(time.RFC3339), j.PaymentHash)
		}
		return e
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("treasury claim lost")
	}
	return nil
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
	lastError := ""
	if e != nil {
		lastError = e.Error()
	}
	_, saveErr := w.Store.DB.Exec(`UPDATE treasury_jobs SET status=?,last_error=?,next_attempt_at='',updated_at=? WHERE payment_hash=? AND status IN ('signed','broadcast','broadcast_unknown')`, status, lastError, time.Now().UTC().Format(time.RFC3339), j.PaymentHash)
	if saveErr != nil {
		return saveErr
	}
	return e
}
func (w *Worker) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	processTicker := time.NewTicker(interval)
	defer processTicker.Stop()
	discoveryInterval := w.DiscoveryInterval
	if discoveryInterval <= 0 {
		discoveryInterval = 30 * time.Second
	}
	discoveryTicker := time.NewTicker(discoveryInterval)
	defer discoveryTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-discoveryTicker.C:
			if queued, e := w.DiscoverBalances(ctx); e != nil {
				slog.Warn("treasury_balance_discovery_failed", "error", e.Error())
			} else if queued > 0 {
				slog.Info("treasury_sweeps_queued", "count", queued)
			}
		case <-processTicker.C:
			if e := w.Tick(ctx); e != nil && !errors.Is(e, sql.ErrNoRows) {
				slog.Warn("treasury_tick_failed", "error", e.Error())
			}
		}
	}
}
