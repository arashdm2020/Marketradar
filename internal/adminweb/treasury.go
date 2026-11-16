package adminweb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"tradebot/internal/bnb"
	"tradebot/internal/profile"
	"tradebot/internal/treasury"
)

func (s *Server) treasurySummary(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r) {
		return
	}
	if s.Treasury == nil {
		http.Error(w, "treasury unavailable", 503)
		return
	}
	counts, e := s.Treasury.Counts()
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	amounts, e := s.Treasury.Amounts()
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	dest, _ := s.Treasury.ActiveDestination()
	var motherBalance any
	var deposits any
	var rpcHealth string
	var network any
	var signerReady bool
	var sweepEnabled bool
	if s.Web != nil && s.Web.Billing != nil && s.Web.Billing.Chain != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
		defer cancel()
		if id, e := s.Web.Billing.Chain.ChainID(ctx); e == nil {
			network = id
			if id == 56 {
				rpcHealth = "healthy"
			} else {
				rpcHealth = "wrong_network"
			}
		} else {
			rpcHealth = "unavailable"
		}
		rpc, ok := s.Web.Billing.Chain.(interface {
			Balance(context.Context, string) (*big.Int, error)
		})
		if ok && dest.Address != "" {
			if amount, e := rpc.Balance(ctx, dest.Address); e == nil {
				motherBalance = amount.String()
			}
			rows, e := s.Treasury.DB.Query(`SELECT address FROM telegram_wallets ORDER BY derivation_index LIMIT 100`)
			if e == nil {
				defer rows.Close()
				sum := new(big.Int)
				complete := true
				for rows.Next() {
					var a string
					if rows.Scan(&a) != nil {
						complete = false
						break
					}
					bal, e := rpc.Balance(ctx, a)
					if e != nil {
						complete = false
						break
					}
					sum.Add(sum, bal)
				}
				if complete && rows.Err() == nil {
					deposits = sum.String()
				}
			}
		}
	}
	if s.Custody != nil {
		signerReady = true
	}
	if s.SweepWorker != nil && s.Web != nil && s.Web.Billing != nil && s.Web.Billing.Chain != nil && signerReady {
		sweepEnabled = true
	}
	jsonOut(w, map[string]any{"destination": dest, "counts": counts, "amounts_wei": amounts, "mother_balance_wei": motherBalance, "deposit_balance_wei": deposits, "network": network, "rpc_health": rpcHealth, "signer_ready": signerReady, "sweep_enabled": sweepEnabled, "pending_onchain_wei": nil})
}
func (s *Server) treasuryConfig(w http.ResponseWriter, r *http.Request) {
	ensureTreasuryConfigSchema(s)
	if r.Method == "GET" {
		if !s.guard(w, r) {
			return
		}
		var rpcLegacy, fingerprint, reference, at string
		var cipher []byte
		var chain, confirmations, monitoring, publicInvoicing int
		e := s.Store.DB.QueryRow(`SELECT rpc_url,xpub_fingerprint,reference_address,chain_id,updated_at,COALESCE(rpc_cipher,X''),confirmations,monitoring_seconds,public_invoicing FROM treasury_public_config WHERE id=1`).Scan(&rpcLegacy, &fingerprint, &reference, &chain, &at, &cipher, &confirmations, &monitoring, &publicInvoicing)
		if e != nil {
			jsonOut(w, map[string]any{"configured": false, "rpc_status": "not_configured", "monitoring_ready": false, "transfer_ready": false})
			return
		}
		rpcURL := rpcLegacy
		if len(cipher) > 0 && s.AIStore != nil {
			if clear, openErr := s.AIStore.OpenSecret(cipher); openErr == nil {
				rpcURL = clear
			}
		}
		if rpcURL == "" {
			jsonOut(w, map[string]any{"configured": false, "rpc_status": "not_configured", "monitoring_ready": false, "transfer_ready": false})
			return
		}
		endpoint := sanitizeRPCEndpoint(rpcURL)
		ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
		defer cancel()
		start := time.Now()
		rpcClient := bnb.RPC{URL: rpcURL, HTTP: &http.Client{Timeout: 5 * time.Second}}
		actualChain, chainErr := rpcClient.ChainID(ctx)
		head, headErr := rpcClient.Head(ctx)
		latency := time.Since(start).Milliseconds()
		status := "healthy"
		diagnostic := "اتصال RPC برقرار است"
		if headErr != nil || chainErr != nil || actualChain != 56 {
			status = "unavailable"
			diagnostic = "RPC شبکهٔ BSC Mainnet را تأیید نکرد"
		}
		var balance any
		dest, _ := s.Treasury.ActiveDestination()
		if headErr == nil && dest.Address != "" {
			if value, balErr := rpcClient.Balance(ctx, dest.Address); balErr == nil {
				balance = value.String()
			} else {
				status = "degraded"
				diagnostic = "ارتفاع بلوک دریافت شد اما موجودی مقصد خوانده نشد"
			}
		}
		jsonOut(w, map[string]any{"configured": true, "rpc_endpoint": endpoint, "rpc_status": status, "rpc_latency_ms": latency, "block_height": head, "refresh_at": time.Now().UTC().Format(time.RFC3339), "diagnostic": diagnostic, "xpub_fingerprint": fingerprint, "reference_address": reference, "chain_id": actualChain, "confirmations": confirmations, "monitoring_seconds": monitoring, "public_invoicing": publicInvoicing == 1, "updated_at": at, "monitoring_ready": headErr == nil && chainErr == nil && actualChain == 56, "destination": dest, "destination_balance_wei": balance, "transfer_ready": false})
		return
	}
	if !s.mutation(w, r) {
		return
	}
	rpcURL, xpub, reference := strings.TrimSpace(r.FormValue("rpc_url")), strings.TrimSpace(r.FormValue("xpub")), strings.TrimSpace(r.FormValue("reference_address"))
	publicInvoicing := r.FormValue("public_invoicing") == "true"
	if rpcURL == "" {
		var opener func([]byte) (string, error)
		if s.AIStore != nil {
			opener = s.AIStore.OpenSecret
		}
		if existing, loadErr := treasury.LoadPublicConfig(s.Store.DB, opener); loadErr == nil {
			rpcURL = existing.RPCURL
		}
	}
	confirmations, ce := strconv.Atoi(r.FormValue("confirmations"))
	if r.FormValue("confirmations") == "" {
		confirmations = 12
		ce = nil
	}
	monitoring, me := strconv.Atoi(r.FormValue("monitoring_seconds"))
	if r.FormValue("monitoring_seconds") == "" {
		monitoring = 15
		me = nil
	}
	if ce != nil || me != nil || confirmations < 1 || confirmations > 100 || monitoring < 5 || monitoring > 3600 {
		http.Error(w, "invalid payment policy", 400)
		return
	}
	parsed, parseErr := url.Parse(rpcURL)
	if parseErr != nil || parsed.Scheme != "https" && parsed.Scheme != "http" || parsed.Host == "" {
		http.Error(w, "invalid RPC URL", 400)
		return
	}
	fingerprint := ""
	var validatedDeriver *bnb.Deriver
	if xpub != "" || reference != "" {
		deriver, e := bnb.NewDeriver(xpub)
		if e != nil {
			http.Error(w, "invalid xpub", 400)
			return
		}
		derived, e := deriver.Address(0)
		if e != nil || !strings.EqualFold(derived, reference) {
			http.Error(w, "reference address does not match derivation index 0", 400)
			return
		}
		sum := sha256.Sum256([]byte(xpub))
		fingerprint = hex.EncodeToString(sum[:8])
		validatedDeriver = deriver
		if s.Web == nil || s.Web.Store == nil || s.Web.Store.PinWalletRoot(xpub) != nil {
			http.Error(w, "wallet root conflicts with existing allocation", 409)
			return
		}
	}
	if publicInvoicing {
		deriverReady := validatedDeriver != nil || s.Web != nil && s.Web.Billing != nil && s.Web.Billing.Deriver != nil && s.Web.Billing.ReferenceVerified
		if !deriverReady || s.Web == nil || s.Web.Store == nil {
			http.Error(w, "HD wallet must be verified before public invoicing", 409)
			return
		}
		if _, priceErr := s.Web.Store.Price(); priceErr != nil {
			http.Error(w, "plan price must be configured before public invoicing", 409)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	rpcClient := bnb.RPC{URL: rpcURL, HTTP: &http.Client{Timeout: 6 * time.Second}}
	chain, e := rpcClient.ChainID(ctx)
	if e != nil || chain != 56 {
		http.Error(w, "RPC must report BSC chain ID 56", 400)
		return
	}
	if _, e = rpcClient.Head(ctx); e != nil {
		http.Error(w, "RPC block height unavailable", 400)
		return
	}
	if s.AIStore == nil {
		http.Error(w, "server encryption unavailable", 503)
		return
	}
	sealed, e := s.AIStore.SealSecret(rpcURL)
	if e != nil {
		http.Error(w, "server encryption unavailable", 503)
		return
	}
	var sealedXPUB []byte
	if xpub != "" {
		sealedXPUB, e = s.AIStore.SealSecret(xpub)
		if e != nil {
			http.Error(w, "server encryption unavailable", 503)
			return
		}
	}
	tx, e := s.Store.DB.Begin()
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	defer tx.Rollback()
	if e == nil {
		_, e = tx.Exec(`INSERT INTO treasury_public_config(id,rpc_url,xpub_fingerprint,reference_address,chain_id,updated_at,rpc_cipher,confirmations,monitoring_seconds,xpub_cipher,public_invoicing) VALUES(1,'',?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET rpc_url='',rpc_cipher=excluded.rpc_cipher,xpub_cipher=CASE WHEN length(excluded.xpub_cipher)>0 THEN excluded.xpub_cipher ELSE treasury_public_config.xpub_cipher END,xpub_fingerprint=CASE WHEN excluded.xpub_fingerprint='' THEN treasury_public_config.xpub_fingerprint ELSE excluded.xpub_fingerprint END,reference_address=CASE WHEN excluded.reference_address='' THEN treasury_public_config.reference_address ELSE excluded.reference_address END,chain_id=excluded.chain_id,updated_at=excluded.updated_at,confirmations=excluded.confirmations,monitoring_seconds=excluded.monitoring_seconds,public_invoicing=excluded.public_invoicing`, fingerprint, strings.ToLower(reference), chain, time.Now().UTC().Format(time.RFC3339), sealed, confirmations, monitoring, sealedXPUB, publicInvoicing)
	}
	if e == nil {
		_, e = tx.Exec(`INSERT INTO admin_audit(admin_id,action,detail,at_utc) VALUES(?,'treasury_public_config',?,datetime('now'))`, profile.AdminID, "chain_id=56 fingerprint="+fingerprint)
	}
	if e != nil || tx.Commit() != nil {
		http.Error(w, "save failed", 500)
		return
	}
	if s.Web != nil && s.Web.Billing != nil {
		s.Web.Billing.Chain = rpcClient
		s.Web.Billing.ExpectedChainID = 56
		s.Web.Billing.Confirmations = uint64(confirmations)
		s.Web.Billing.PublicInvoicesEnabled = publicInvoicing
		if validatedDeriver != nil {
			s.Web.Billing.Deriver = validatedDeriver
			s.Web.Billing.ReferenceVerified = true
		}
	}
	jsonOut(w, map[string]any{"ok": true, "chain_id": 56, "rpc_endpoint": sanitizeRPCEndpoint(rpcURL), "reference_address": strings.ToLower(reference), "xpub_fingerprint": fingerprint, "monitoring_ready": true, "transfer_ready": false})
}
func ensureTreasuryConfigSchema(s *Server) {
	_ = treasury.EnsurePublicConfig(s.Store.DB)
}
func sanitizeRPCEndpoint(raw string) string {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" {
		return "configured"
	}
	return u.Scheme + "://" + u.Host
}
func (s *Server) treasuryDestination(w http.ResponseWriter, r *http.Request) {
	if !s.mutation(w, r) {
		return
	}
	if s.Treasury == nil {
		http.Error(w, "treasury unavailable", 503)
		return
	}
	d, e := s.Treasury.SetDestination(profile.AdminID, r.FormValue("admin_code"), r.FormValue("address"))
	if e != nil {
		http.Error(w, "fresh admin code or valid address required", 403)
		return
	}
	jsonOut(w, d)
}
func (s *Server) treasuryReview(w http.ResponseWriter, r *http.Request) {
	if !s.mutation(w, r) {
		return
	}
	if s.Treasury == nil {
		http.Error(w, "treasury unavailable", 503)
		return
	}
	if e := s.Treasury.Review(profile.AdminID, r.FormValue("payment_hash")); e != nil {
		http.Error(w, "job not reviewable", 400)
		return
	}
	jsonOut(w, map[string]any{"ok": true})
}
func (s *Server) treasurySweep(w http.ResponseWriter, r *http.Request) {
	if !s.mutation(w, r) {
		return
	}
	if s.Treasury == nil || s.SweepWorker == nil || s.Web == nil || s.Web.Billing == nil || s.Web.Billing.Chain == nil {
		http.Error(w, "sweep unavailable", 503)
		return
	}
	code := r.FormValue("admin_code")
	address := strings.TrimSpace(r.FormValue("address"))
	if code == "" || address == "" {
		http.Error(w, "fresh admin code and wallet address required", 400)
		return
	}
	ok, e := s.Store.ConsumeAdminCode(code)
	if e != nil || !ok {
		http.Error(w, "fresh admin authentication required", 403)
		return
	}
	if !treasury.ValidAddress(address) {
		http.Error(w, "invalid wallet address", 400)
		return
	}
	if s.Custody != nil {
		hasKey, err := s.Custody.HasKey(address)
		if err != nil || !hasKey {
			http.Error(w, "wallet is not signable", 400)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	chain := s.Web.Billing.Chain
	if id, err := chain.ChainID(ctx); err != nil || id != 56 {
		http.Error(w, "BSC network unavailable or wrong chain", 503)
		return
	}
	balance, err := chain.Balance(ctx, address)
	if err != nil {
		http.Error(w, "unable to read wallet balance", 503)
		return
	}
	gasPrice, err := chain.GasPrice(ctx)
	if err != nil {
		http.Error(w, "unable to read gas price", 503)
		return
	}
	fee := new(big.Int).Mul(gasPrice, big.NewInt(21000))
	if balance.Cmp(fee) <= 0 {
		http.Error(w, "insufficient balance for gas", 400)
		return
	}
	dest, err := s.Treasury.ActiveDestination()
	if err != nil {
		http.Error(w, "treasury destination unavailable", 503)
		return
	}
	if !strings.EqualFold(dest.Address, treasury.DefaultDestination) {
		http.Error(w, "treasury destination mismatch", 409)
		return
	}
	amount := new(big.Int).Sub(balance, fee)
	txHash := "manual:" + address + ":" + fmt.Sprintf("%d", time.Now().UnixNano())
	dbtx, e := s.Treasury.DB.Begin()
	if e != nil {
		http.Error(w, "sweep transaction unavailable", 500)
		return
	}
	defer dbtx.Rollback()
	_, e = dbtx.Exec(`INSERT INTO web_payment_events(tx_hash,address,amount_wei,block_number,block_time,classification,seen_at) VALUES(?,?,?,0,?,'manual_sweep',?)`, txHash, address, amount.String(), time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339))
	if e != nil {
		http.Error(w, "payment record failed", 500)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, e = dbtx.Exec(`INSERT INTO treasury_jobs(payment_hash,source,amount_wei,destination_version,status,fee_wei,updated_at) VALUES(?,?,?,?,'queued',?,?)`, txHash, address, amount.String(), dest.Version, fee.String(), now)
	if e != nil {
		if strings.Contains(strings.ToLower(e.Error()), "unique") {
			http.Error(w, "wallet already has an active sweep", 409)
		} else {
			http.Error(w, "sweep job creation failed", 500)
		}
		return
	}
	if _, e = dbtx.Exec(`INSERT INTO admin_audit(admin_id,target_id,action,detail,at_utc) VALUES(?,0,'manual_sweep',?,?)`, profile.AdminID, address, now); e != nil {
		http.Error(w, "audit failed", 500)
		return
	}
	if e = dbtx.Commit(); e != nil {
		http.Error(w, "sweep commit failed", 500)
		return
	}
	jsonOut(w, map[string]any{"ok": true, "payment_hash": txHash, "source": address, "destination": dest.Address, "amount_wei": amount.String(), "fee_wei": fee.String(), "status": "queued"})
}
func (s *Server) treasuryWallets(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r) {
		return
	}
	rows, e := s.Treasury.DB.Query(`SELECT telegram_id,address,derivation_index,created_at,signable FROM telegram_wallets ORDER BY derivation_index DESC LIMIT 200`)
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	defer rows.Close()
	type walletRow struct {
		telegramID, idx  int64
		address, created string
		signable         int
	}
	var wallets []walletRow
	for rows.Next() {
		var x walletRow
		if e = rows.Scan(&x.telegramID, &x.address, &x.idx, &x.created, &x.signable); e != nil {
			http.Error(w, "database error", 500)
			return
		}
		wallets = append(wallets, x)
	}
	rows.Close()
	out := []map[string]any{}
	var balanceReader interface {
		Balance(context.Context, string) (*big.Int, error)
	}
	if s.Web != nil && s.Web.Billing != nil {
		balanceReader, _ = s.Web.Billing.Chain.(interface {
			Balance(context.Context, string) (*big.Int, error)
		})
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	for _, x := range wallets {
		var balance any
		if balanceReader != nil && ctx.Err() == nil {
			if n, be := balanceReader.Balance(ctx, x.address); be == nil {
				balance = n.String()
			}
		}
		out = append(out, map[string]any{"telegram_id": x.telegramID, "address": x.address, "index": x.idx, "created_at": x.created, "has_key": x.signable == 1, "balance_wei": balance})
	}
	jsonOut(w, out)
}
func (s *Server) treasuryJobs(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r) {
		return
	}
	jobs, e := s.Treasury.Jobs()
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	out := []map[string]any{}
	for _, j := range jobs {
		out = append(out, map[string]any{"payment_hash": j.PaymentHash, "source": j.Source, "amount_wei": j.AmountWei, "destination_version": j.DestinationVersion, "status": j.Status, "tx_hash": j.TxHash, "fee_wei": j.FeeWei, "updated_at": j.UpdatedAt, "attempts": j.Attempts, "last_error": j.LastError})
	}
	jsonOut(w, out)
}
