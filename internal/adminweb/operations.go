package adminweb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"tradebot/internal/bnb"
	"tradebot/internal/profile"
	"tradebot/internal/treasury"
)

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r) {
		return
	}
	jsonOut(w, map[string]any{"id": profile.AdminID, "role": "admin"})
}

func (s *Server) operationsUsers(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r) {
		return
	}
	telegram, e := s.Store.AdminUsers(profile.AdminID)
	if e != nil {
		http.Error(w, "users unavailable", 500)
		return
	}
	web := []any{}
	if s.Web != nil {
		v, err := s.Web.Store.AdminUsers()
		if err == nil {
			for _, x := range v {
				web = append(web, x)
			}
		}
	}
	jsonOut(w, map[string]any{"telegram": telegram, "web": web})
}
func (s *Server) operationsSubscriptions(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r) {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	var active, expired int
	_ = s.Store.DB.QueryRow(`SELECT count(*) FROM web_users WHERE subscription_until>?`, now).Scan(&active)
	_ = s.Store.DB.QueryRow(`SELECT count(*) FROM web_users WHERE subscription_until!='' AND subscription_until<=?`, now).Scan(&expired)
	var price any
	if s.Web != nil {
		if p, e := s.Web.Store.Price(); e == nil {
			price = p.String()
		}
	}
	jsonOut(w, map[string]any{"active_web": active, "expired_web": expired, "price_wei": price, "plan_period_days": 30})
}
func (s *Server) operationsPayments(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r) {
		return
	}
	rows, e := s.Store.DB.Query(`SELECT id,user_id,address,price_wei,chain_id,created_at,expires_at,status,tx_hash,channel FROM (SELECT id,user_id,address,price_wei,chain_id,created_at,expires_at,status,tx_hash,'web' channel FROM web_invoices UNION ALL SELECT id,telegram_id,address,price_wei,chain_id,created_at,expires_at,status,tx_hash,'telegram' channel FROM telegram_invoices) ORDER BY created_at DESC LIMIT 500`)
	if e != nil {
		http.Error(w, "payments unavailable", 500)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, address, price, created, expires, status, hash, channel string
		var uid, chain int64
		if rows.Scan(&id, &uid, &address, &price, &chain, &created, &expires, &status, &hash, &channel) == nil {
			out = append(out, map[string]any{"id": id, "user_id": uid, "channel": channel, "address": address, "price_wei": price, "chain_id": chain, "created_at": created, "expires_at": expires, "status": status, "tx_hash": hash})
		}
	}
	rows.Close()
	for _, item := range out {
		var amount, blockTime, class, notification string
		var confirmations, activated int
		hash, _ := item["tx_hash"].(string)
		id, _ := item["id"].(string)
		_ = s.Store.DB.QueryRow(`SELECT amount_wei,block_time,classification,confirmation_count,subscription_activated FROM web_payment_events WHERE tx_hash=? OR invoice_id=? ORDER BY seen_at DESC LIMIT 1`, hash, id).Scan(&amount, &blockTime, &class, &confirmations, &activated)
		_ = s.Store.DB.QueryRow(`SELECT COALESCE(group_concat(kind||':'||status,','),'') FROM payment_notification_outbox WHERE invoice_id=?`, id).Scan(&notification)
		item["amount_wei"], item["block_time"], item["classification"] = amount, blockTime, class
		item["confirmation_count"], item["subscription_activated"], item["notification_status"] = confirmations, activated == 1, notification
	}
	jsonOut(w, out)
}
func (s *Server) operationsFinance(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r) {
		return
	}
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	for _, v := range []string{from, to} {
		if v != "" {
			if _, e := time.Parse("2006-01-02", v); e != nil {
				http.Error(w, "invalid date", 400)
				return
			}
		}
	}
	invoiceRows, e := s.Store.DB.Query(`SELECT status,price_wei,created_at FROM (SELECT status,price_wei,created_at FROM web_invoices UNION ALL SELECT status,price_wei,created_at FROM telegram_invoices) WHERE (?='' OR substr(created_at,1,10)>=?) AND (?='' OR substr(created_at,1,10)<=?)`, from, from, to, to)
	if e != nil {
		http.Error(w, "finance unavailable", 500)
		return
	}
	defer invoiceRows.Close()
	outstanding := new(big.Int)
	invoices := map[string]int{}
	for invoiceRows.Next() {
		var status, amount, created string
		if invoiceRows.Scan(&status, &amount, &created) == nil {
			invoices[status]++
			if status == "pending" {
				if n, ok := new(big.Int).SetString(amount, 10); ok {
					outstanding.Add(outstanding, n)
				}
			}
		}
	}
	paymentRows, e := s.Store.DB.Query(`SELECT classification,amount_wei,block_time FROM web_payment_events WHERE (?='' OR substr(block_time,1,10)>=?) AND (?='' OR substr(block_time,1,10)<=?)`, from, from, to, to)
	if e != nil {
		http.Error(w, "finance unavailable", 500)
		return
	}
	defer paymentRows.Close()
	confirmed := new(big.Int)
	paymentStates := map[string]int{}
	daily := map[string]*big.Int{}
	for paymentRows.Next() {
		var classification, amount, at string
		if paymentRows.Scan(&classification, &amount, &at) == nil {
			paymentStates[classification]++
			if classification == "paid" || classification == "paid_telegram" {
				if n, ok := new(big.Int).SetString(amount, 10); ok {
					confirmed.Add(confirmed, n)
					day := at
					if len(day) > 10 {
						day = day[:10]
					}
					if daily[day] == nil {
						daily[day] = new(big.Int)
					}
					daily[day].Add(daily[day], n)
				}
			}
		}
	}
	// Sweep amounts are internal transfers and are never included in revenue.
	settled := new(big.Int)
	pending := new(big.Int)
	fees := new(big.Int)
	jobRows, e := s.Store.DB.Query(`SELECT status,amount_wei,fee_wei,updated_at FROM treasury_jobs WHERE (?='' OR substr(updated_at,1,10)>=?) AND (?='' OR substr(updated_at,1,10)<=?)`, from, from, to, to)
	if e == nil {
		defer jobRows.Close()
		for jobRows.Next() {
			var status, amount, fee, at string
			if jobRows.Scan(&status, &amount, &fee, &at) == nil {
				n, ok := new(big.Int).SetString(amount, 10)
				if !ok {
					continue
				}
				if status == "transferred" {
					settled.Add(settled, n)
					if f, ok := new(big.Int).SetString(fee, 10); ok {
						fees.Add(fees, f)
					}
				} else {
					pending.Add(pending, n)
				}
			}
		}
	}
	trend := []map[string]string{}
	for day, amount := range daily {
		trend = append(trend, map[string]string{"day": day, "paid_wei": amount.String()})
	}
	jsonOut(w, map[string]any{"confirmed_wei": confirmed.String(), "recognized_revenue_wei": confirmed.String(), "outstanding_wei": outstanding.String(), "settled_wei": settled.String(), "pending_sweep_wei": pending.String(), "network_fees_wei": fees.String(), "invoice_statuses": invoices, "payment_statuses": paymentStates, "daily": trend, "accounting_note": "Revenue is based only on unique paid customer deposits; sweeps are internal transfers."})
}
func (s *Server) operationsWallets(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r) {
		return
	}
	var chain *bnb.RPC
	var opener func([]byte) (string, error)
	if s.AIStore != nil {
		opener = s.AIStore.OpenSecret
	}
	if cfg, ce := treasury.LoadPublicConfig(s.Store.DB, opener); ce == nil {
		chain = &bnb.RPC{URL: cfg.RPCURL, HTTP: &http.Client{Timeout: 4 * time.Second}}
	}
	rows, e := s.Store.DB.Query(`SELECT w.telegram_id,w.derivation_index,w.address,w.created_at,w.signable,COALESCE(l.web_user_id,0),COALESCE((SELECT max(block_time) FROM web_payment_events p WHERE lower(p.address)=lower(w.address)),'') FROM telegram_wallets w LEFT JOIN web_telegram_links l ON l.telegram_id=w.telegram_id ORDER BY w.derivation_index DESC LIMIT 200`)
	if e != nil {
		http.Error(w, "wallets unavailable", 500)
		return
	}
	type walletRow struct {
		uid, idx, webID        int64
		address, created, last string
		signable               int
	}
	var saved []walletRow
	for rows.Next() {
		var x walletRow
		if rows.Scan(&x.uid, &x.idx, &x.address, &x.created, &x.signable, &x.webID, &x.last) == nil {
			saved = append(saved, x)
		}
	}
	rows.Close()
	out := []map[string]any{}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	for _, x := range saved {
		var balance any
		if chain != nil && ctx.Err() == nil {
			if n, be := chain.Balance(ctx, x.address); be == nil {
				balance = n.String()
			}
		}
		var invoiceCount, confirmed int
		var reconcile, sweep string
		_ = s.Store.DB.QueryRow(`SELECT count(*),sum(CASE WHEN status='paid' THEN 1 ELSE 0 END) FROM telegram_invoices WHERE telegram_id=?`, x.uid).Scan(&invoiceCount, &confirmed)
		_ = s.Store.DB.QueryRow(`SELECT COALESCE(classification,'') FROM web_payment_events WHERE lower(address)=lower(?) ORDER BY block_time DESC LIMIT 1`, x.address).Scan(&reconcile)
		_ = s.Store.DB.QueryRow(`SELECT COALESCE(status,'') FROM treasury_jobs WHERE lower(source)=lower(?) ORDER BY updated_at DESC LIMIT 1`, x.address).Scan(&sweep)
		out = append(out, map[string]any{"telegram_id": x.uid, "web_user_id": x.webID, "index": x.idx, "address": x.address, "created_at": x.created, "signable": x.signable == 1, "last_payment": x.last, "balance_wei": balance, "invoice_count": invoiceCount, "confirmed_payments": confirmed, "reconciliation_status": reconcile, "sweep_status": sweep})
	}
	jsonOut(w, out)
}
func (s *Server) operationsWalletTransactions(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r) {
		return
	}
	id, e := strconv.ParseInt(r.URL.Query().Get("telegram_id"), 10, 64)
	if e != nil || id <= 0 {
		http.Error(w, "invalid Telegram user", 400)
		return
	}
	limit := 50
	offset := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, _ = strconv.Atoi(raw)
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		offset, _ = strconv.Atoi(raw)
	}
	items, total, e := s.Store.WalletTransactions(id, limit, offset)
	if e != nil {
		http.Error(w, "wallet history unavailable", 404)
		return
	}
	jsonOut(w, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}
func (s *Server) operationsAnalyses(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r) {
		return
	}
	out := []map[string]any{}
	for _, table := range []string{"request_states", "web_analyses"} {
		rows, e := s.Store.DB.Query(`SELECT id,user_id,symbol,timeframe,status,model,config_version,engine_candles,model_candles,created_at FROM ` + table + ` ORDER BY created_at DESC LIMIT 200`)
		if e != nil {
			continue
		}
		for rows.Next() {
			var id, symbol, frame, status, model, created string
			var uid string
			var version, engine, ai int
			if rows.Scan(&id, &uid, &symbol, &frame, &status, &model, &version, &engine, &ai, &created) == nil {
				out = append(out, map[string]any{"id": id, "user_id": uid, "channel": strings.TrimSuffix(table, "_analyses"), "symbol": symbol, "timeframe": frame, "status": status, "model": model, "config_version": version, "engine_candles": engine, "model_candles": ai, "created_at": created})
			}
		}
		rows.Close()
	}
	jsonOut(w, out)
}
func (s *Server) operationsSystem(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r) {
		return
	}
	type health struct {
		State     string `json:"state"`
		CheckedAt string `json:"checked_at"`
		LatencyMS int64  `json:"latency_ms"`
		Message   string `json:"message"`
	}
	probe := func(fn func(context.Context) error) health {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		start := time.Now()
		err := fn(ctx)
		h := health{State: "healthy", CheckedAt: time.Now().UTC().Format(time.RFC3339), LatencyMS: time.Since(start).Milliseconds(), Message: "پاسخ معتبر دریافت شد"}
		if err != nil {
			h.State = "unavailable"
			h.Message = "پاسخ معتبر دریافت نشد"
		}
		return h
	}
	database := probe(func(ctx context.Context) error { return s.Store.DB.PingContext(ctx) })
	telegram := health{State: "not_configured", Message: "توکن ربات تنظیم نشده است"}
	if s.BotToken != "" {
		telegram = probe(func(ctx context.Context) error {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.telegram.org/bot"+s.BotToken+"/getMe", nil)
			resp, e := http.DefaultClient.Do(req)
			if e != nil {
				return e
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				return fmt.Errorf("status")
			}
			return nil
		})
	}
	market := health{State: "not_configured", Message: "کلید Twelve Data تنظیم نشده است"}
	if key := os.Getenv("TWELVEDATA_API_KEY"); key != "" {
		market = probe(func(ctx context.Context) error {
			u := "https://api.twelvedata.com/time_series?" + url.Values{"symbol": {"XAU/USD"}, "interval": {"5min"}, "outputsize": {"1"}, "apikey": {key}}.Encode()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
			resp, e := http.DefaultClient.Do(req)
			if e != nil {
				return e
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				return fmt.Errorf("status")
			}
			var v map[string]any
			if json.NewDecoder(resp.Body).Decode(&v) != nil || v["status"] == "error" {
				return fmt.Errorf("provider")
			}
			return nil
		})
	}
	aiHealth := health{State: "not_checked", Message: "آزمایش ارائه‌دهنده هنوز اجرا نشده است"}
	var aiStatus, aiAt string
	var aiLatency int64
	if s.Store.DB.QueryRow(`SELECT status,latency_ms,at_utc FROM ai_test_runs ORDER BY at_utc DESC LIMIT 1`).Scan(&aiStatus, &aiLatency, &aiAt) == nil {
		aiHealth = health{State: "healthy", CheckedAt: aiAt, LatencyMS: aiLatency, Message: "آخرین آزمایش ارائه‌دهنده موفق بود"}
		if aiStatus != "ok" && aiStatus != "success" {
			aiHealth.State = "degraded"
			aiHealth.Message = "آخرین آزمایش ارائه‌دهنده ناموفق بود"
		}
	}
	rpcHealth := health{State: "not_configured", Message: "RPC شبکه BSC تنظیم نشده است"}
	ensureTreasuryConfigSchema(s)
	var cipher []byte
	if s.Store.DB.QueryRow(`SELECT COALESCE(rpc_cipher,X'') FROM treasury_public_config WHERE id=1`).Scan(&cipher) == nil && len(cipher) > 0 && s.AIStore != nil {
		if raw, e := s.AIStore.OpenSecret(cipher); e == nil {
			rpcHealth = probe(func(ctx context.Context) error {
				id, e := (bnb.RPC{URL: raw, HTTP: &http.Client{Timeout: 4 * time.Second}}).ChainID(ctx)
				if e != nil || id != 56 {
					return fmt.Errorf("chain")
				}
				return nil
			})
		}
	}
	var pending int
	_ = s.Store.DB.QueryRow(`SELECT count(*) FROM treasury_jobs WHERE status!='transferred'`).Scan(&pending)
	jsonOut(w, map[string]any{"database": database, "telegram": telegram, "market_data": market, "ai": aiHealth, "rpc": rpcHealth, "signer": health{State: "not_configured", Message: "امضاکنندهٔ مستقل متصل نیست"}, "pending_jobs": pending, "checked_at": time.Now().UTC().Format(time.RFC3339)})
}
func (s *Server) operationsAudit(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r) {
		return
	}
	rows, e := s.Store.DB.Query(`SELECT id,admin_id,COALESCE(target_id,0),action,detail,at_utc FROM admin_audit ORDER BY id DESC LIMIT 200`)
	if e != nil {
		http.Error(w, "audit unavailable", 500)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, admin, target int64
		var action, detail, at string
		if rows.Scan(&id, &admin, &target, &action, &detail, &at) == nil {
			out = append(out, map[string]any{"id": id, "admin_id": admin, "target_id": target, "action": action, "detail": detail, "at": at})
		}
	}
	jsonOut(w, out)
}

var _ = sql.ErrNoRows
var _ = strconv.Itoa
