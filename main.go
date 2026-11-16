package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"tradebot/internal/adminweb"
	"tradebot/internal/ai"
	"tradebot/internal/aimgmt"
	"tradebot/internal/backtest"
	"tradebot/internal/bnb"
	"tradebot/internal/catalog"
	"tradebot/internal/config"
	"tradebot/internal/custody"
	"tradebot/internal/domain"
	"tradebot/internal/engine"
	"tradebot/internal/marketdata"
	"tradebot/internal/profile"
	"tradebot/internal/risk"
	"tradebot/internal/snapshot"
	"tradebot/internal/telegram"
	"tradebot/internal/treasury"
	"tradebot/internal/webapp"
)

type analyst struct{ client ai.Client }

func (a analyst) Analyze(ctx context.Context, s domain.Snapshot) (domain.Signal, error) {
	r, e := a.client.Analyze(ctx, s)
	return r.Signal, e
}
func emit(v any) error { return json.NewEncoder(os.Stdout).Encode(v) }
func save(name string, v any) error {
	if e := os.MkdirAll("data", 0700); e != nil {
		return e
	}
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(filepath.Join("data", name), b, 0600)
}

func publicAdminURL() (string, error) {
	raw := strings.TrimSpace(os.Getenv("ADMIN_PUBLIC_URL"))
	if raw == "" {
		raw = "http://127.0.0.1:3000/login"
	}
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("ADMIN_PUBLIC_URL must be an absolute URL without credentials, query, or fragment")
	}
	loopback := u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"
	if u.Scheme != "https" && !(loopback && u.Scheme == "http") {
		return "", fmt.Errorf("ADMIN_PUBLIC_URL must use HTTPS outside local development")
	}
	return strings.TrimRight(raw, "/"), nil
}

func publicAdminRoot(raw string) string {
	u, e := url.Parse(raw)
	if e != nil {
		return raw
	}
	u.Path, u.RawPath, u.RawQuery, u.Fragment = "/", "", "", ""
	return u.String()
}
func main() {
	if e := run(); e != nil {
		slog.Error("command failed", "error", e)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: go run . analyze|inspect|backtest|doctor|bot|web|bnb-check|profile-backfill")
	}
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	md := marketdata.Client{Key: cfg.TwelveKey}
	model := ai.Client{Key: cfg.NVIDIAKey, BaseURL: cfg.BaseURL, Model: cfg.Model}
	command := os.Args[1]
	if command == "profile-backfill" {
		flags := flag.NewFlagSet("profile-backfill", flag.ContinueOnError)
		limit := flags.Int("limit", 20, "maximum existing Telegram users to inspect (1..100)")
		apply := flags.Bool("apply", false, "persist available profile fields")
		if err := flags.Parse(os.Args[2:]); err != nil {
			return err
		}
		token := os.Getenv("TELEGRAM_BOT_TOKEN")
		if token == "" {
			return fmt.Errorf("TELEGRAM_BOT_TOKEN is required")
		}
		path := os.Getenv("SQLITE_PATH")
		if path == "" {
			path = "data/tradebot.db"
		}
		store, err := profile.Open(path)
		if err != nil {
			return err
		}
		defer store.DB.Close()
		bot := telegram.Bot{Token: token, HTTP: &http.Client{Timeout: 10 * time.Second}}
		result, err := telegram.BackfillProfiles(ctx, store, *limit, *apply, bot.LookupChatProfile)
		if err != nil {
			return err
		}
		return emit(map[string]any{"mode": map[bool]string{true: "apply", false: "dry_run"}[*apply], "result": result})
	}
	if command == "bnb-check" {
		xpub, rpcURL := os.Getenv("WEB_BSC_XPUB"), os.Getenv("WEB_BSC_RPC_URL")
		if xpub == "" || rpcURL == "" {
			return fmt.Errorf("WEB_BSC_XPUB and WEB_BSC_RPC_URL are required")
		}
		deriver, e := bnb.NewDeriver(xpub)
		if e != nil {
			return e
		}
		expected := uint64(56)
		if raw := os.Getenv("WEB_BSC_CHAIN_ID"); raw != "" {
			expected, e = strconv.ParseUint(raw, 10, 64)
			if e != nil || expected != 56 && expected != 97 {
				return fmt.Errorf("WEB_BSC_CHAIN_ID must be 56 or 97")
			}
		}
		checkCtx, stop := context.WithTimeout(ctx, 20*time.Second)
		defer stop()
		result, e := bnb.Check(checkCtx, deriver, bnb.RPC{URL: rpcURL}, expected, os.Getenv("WEB_BSC_REFERENCE_ADDRESS"))
		if e != nil {
			return e
		}
		if e = emit(result); e != nil {
			return e
		}
		if !result.NetworkMatches {
			return fmt.Errorf("RPC network mismatch")
		}
		if result.ReferenceConfigured && !result.ReferenceMatches {
			return fmt.Errorf("reference address mismatch")
		}
		return nil
	}
	if command == "doctor" {
		out := map[string]any{"twelvedata_key_present": cfg.TwelveKey != "", "nvidia_key_present": cfg.NVIDIAKey != "", "model": cfg.Model}
		if cfg.TwelveKey != "" {
			_, e := md.Fetch(ctx, cfg.Symbol, cfg.Timeframe, 2)
			out["market_data_ok"] = e == nil
			if e != nil {
				out["market_data_error"] = e.Error()
			}
		}
		out["model_request_verified"] = false
		return emit(out)
	}
	if command == "bot" || command == "web" {
		path := os.Getenv("SQLITE_PATH")
		if path == "" {
			path = "data/tradebot.db"
		}
		store, e := profile.Open(path)
		if e != nil {
			return e
		}
		defer store.DB.Close()
		botCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		allowed := map[int64]bool{}
		for _, part := range strings.Split(os.Getenv("TELEGRAM_ALLOWED_USER_IDS"), ",") {
			if id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64); err == nil {
				allowed[id] = true
			}
		}
		eng := engine.Engine{Market: md, AI: model, MinRR: cfg.MinRR, Buffer: time.Duration(cfg.Buffer) * time.Second}
		recordAttempt := func(a ai.Attempt) {
			if a.AnalysisID == "" {
				return
			}
			_, _ = store.DB.Exec(`INSERT INTO ai_api_attempts(analysis_id,channel,provider,model,status,finish_reason,http_status,started_at,latency_ms,prompt_tokens,completion_tokens,attempt_count,total_latency_ms,breaker_state,failure_class) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, a.AnalysisID, a.Channel, a.Provider, a.Model, a.Status, a.FinishReason, a.HTTPStatus, a.StartedAt.UTC().Format(time.RFC3339), a.LatencyMS, a.PromptTokens, a.CompletionTokens, a.AttemptCount, a.TotalLatencyMS, a.BreakerState, a.FailureClass)
		}
		eng.AI.OnAttempt = recordAttempt
		var aiStore *aimgmt.Store
		if master := os.Getenv("AI_CONFIG_MASTER_KEY"); master != "" {
			aiStore, e = aimgmt.New(store.DB, master)
			if e != nil {
				return e
			}
			if e = aiStore.Seed(aimgmt.Version{Provider: "nvidia", Model: cfg.Model, MaxTokens: 4096, Temperature: 1, ReasoningBudget: 1024, ReasoningEffort: "medium"}, cfg.NVIDIAKey); e != nil {
				return e
			}
			eng.ResolveAI = func() (ai.Client, error) {
				active, e := aiStore.Client(0)
				active.OnAttempt = recordAttempt
				return active, e
			}
		}
		webStore, e := webapp.NewStore(store.DB)
		if e != nil {
			return e
		}
		treasuryStore := &treasury.Store{DB: store.DB, Profiles: store}
		if e = treasury.EnsurePublicConfig(store.DB); e != nil {
			return e
		}
		if e = treasuryStore.SeedDestination(treasury.DefaultDestination); e != nil {
			return e
		}
		if e = treasuryStore.Recover(); e != nil {
			return e
		}
		billing := &webapp.Billing{Store: webStore, Profile: store, ExpectedChainID: 56, Confirmations: 12}
		var openRPC func([]byte) (string, error)
		if aiStore != nil {
			openRPC = aiStore.OpenSecret
		}
		publicBSC, publicErr := treasury.LoadPublicConfig(store.DB, openRPC)
		if publicErr == nil {
			billing.ExpectedChainID = publicBSC.ChainID
			billing.Confirmations = publicBSC.Confirmations
			billing.Chain = bnb.RPC{URL: publicBSC.RPCURL}
			billing.PublicInvoicesEnabled = publicBSC.PublicInvoicing
			if publicBSC.XPUB != "" {
				deriver, de := bnb.NewDeriver(publicBSC.XPUB)
				if de != nil {
					return de
				}
				actual, de := deriver.Address(0)
				if de != nil || !strings.EqualFold(actual, publicBSC.ReferenceAddress) {
					return fmt.Errorf("stored BNB reference address mismatch")
				}
				billing.Deriver = deriver
				billing.ReferenceVerified = true
			}
		}
		billing.TestInvoicesEnabled = os.Getenv("WEB_BSC_TEST_INVOICES_ENABLED") == "true"
		billing.TestEmail = os.Getenv("WEB_BSC_TEST_EMAIL")
		if v := os.Getenv("WEB_BSC_CHAIN_ID"); v != "" {
			n, e := strconv.ParseUint(v, 10, 64)
			if e != nil || n != 56 && n != 97 {
				return fmt.Errorf("invalid BSC chain id")
			}
			billing.ExpectedChainID = n
		}
		if v := os.Getenv("WEB_BSC_CONFIRMATIONS"); v != "" {
			n, e := strconv.ParseUint(v, 10, 64)
			if e != nil || n < 1 || n > 100 {
				return fmt.Errorf("invalid confirmation count")
			}
			billing.Confirmations = n
		}
		if v := os.Getenv("WEB_BSC_XPUB"); v != "" {
			deriver, e := bnb.NewDeriver(v)
			if e != nil {
				return e
			}
			billing.Deriver = deriver
			if reference := os.Getenv("WEB_BSC_REFERENCE_ADDRESS"); reference != "" {
				actual, e := deriver.Address(0)
				if e != nil {
					return e
				}
				if !strings.EqualFold(actual, reference) {
					return fmt.Errorf("BNB reference address mismatch")
				}
				if e = webStore.PinWalletRoot(v); e != nil {
					return e
				}
				billing.ReferenceVerified = true
			}
		}
		if v := os.Getenv("WEB_BSC_RPC_URL"); v != "" {
			rpc := bnb.RPC{URL: v, HTTP: &http.Client{Timeout: 10 * time.Second}}
			chainID, err := rpc.ChainID(botCtx)
			if err != nil {
				// Keep the validated configuration object so read-only balance refresh
				// and the idempotent monitor can recover automatically after a
				// transient outage. Every operation rechecks chain ID before use.
				billing.Chain = rpc
				slog.Warn("bsc_rpc_validation_failed", "error", err.Error())
			} else if chainID != 56 {
				billing.Chain = nil
				slog.Error("bsc_rpc_wrong_network", "chain_id", chainID, "expected_chain_id", 56)
			} else {
				billing.Chain = rpc
			}
		}
		webHandler := &webapp.Handler{Store: webStore, Billing: billing, Analysis: &webapp.AnalysisService{Store: webStore, Config: store, Engine: eng, Context: botCtx}, Profile: store}
		if billing.MonitorReady() {
			go billing.Run(botCtx, 15*time.Second)
		}
		cust, e := custody.New(store.DB)
		if e != nil {
			return e
		}
		if legacyKey := os.Getenv("CUSTODY_MASTER_KEY"); legacyKey != "" {
			migrated, unresolved, migrateErr := cust.MigrateLegacy(legacyKey)
			if migrateErr != nil {
				slog.Warn("legacy_custody_migration_failed", "error", migrateErr.Error())
			} else {
				slog.Info("legacy_custody_migration", "migrated", migrated, "unresolved", unresolved)
			}
		}
		var walletTotal, walletSignable int
		_ = store.DB.QueryRow(`SELECT count(*),COALESCE(sum(CASE WHEN signable=1 AND length(private_key_cipher)>0 AND key_encryption_version=1 THEN 1 ELSE 0 END),0) FROM telegram_wallets`).Scan(&walletTotal, &walletSignable)
		slog.Info("telegram_wallet_compatibility", "total", walletTotal, "signable", walletSignable, "non_signable", walletTotal-walletSignable)
		signer := bnb.NewCustodialSigner(cust)
		var sweepWorker *treasury.Worker
		if billing.Chain != nil && os.Getenv("TREASURY_MAINNET_ENABLED") == "true" && os.Getenv("TREASURY_SWEEP_ENABLED") == "true" {
			sweepWorker = &treasury.Worker{
				Store:             treasuryStore,
				Chain:             billing.Chain,
				Signer:            signer,
				ExpectedChainID:   56,
				DiscoveryInterval: 30 * time.Second,
			}
			go sweepWorker.Run(botCtx, 2*time.Second)
		}
		adminURL, e := publicAdminURL()
		if e != nil {
			return e
		}
		worker := telegram.NewAsyncWorker(nil, store, eng)
		b := telegram.Bot{Token: os.Getenv("TELEGRAM_BOT_TOKEN"), PublicAdminURL: adminURL, Store: store, PlanPrice: func() (*big.Int, error) { return webStore.Price() }, Deriver: billing.Deriver, Custody: cust, Chain: billing.Chain, Allowed: allowed, Engine: eng, Worker: worker}
		worker.SetBot(&b)
		addr := os.Getenv("ADMIN_LISTEN_ADDR")
		if addr == "" {
			addr = "127.0.0.1:8088"
		}
		if addr != "disabled" {
			if !strings.HasPrefix(addr, "127.0.0.1:") && !strings.HasPrefix(addr, "localhost:") {
				return fmt.Errorf("admin panel must bind to loopback")
			}
			admin := adminweb.New(store, b.Token, os.Getenv("TELEGRAM_BOT_USERNAME"))
			admin.Web = webHandler
			admin.AIStore = aiStore
			admin.Treasury = treasuryStore
			admin.Custody = cust
			admin.SweepWorker = sweepWorker
			if command == "bot" {
				admin.NextURL = publicAdminRoot(adminURL)
			}
			server := &http.Server{Addr: addr, Handler: admin.Handler(), ReadHeaderTimeout: 5 * time.Second}
			listener, e := net.Listen("tcp", addr)
			if e != nil {
				return fmt.Errorf("web listen: %w", e)
			}
			slog.Info("web_listening", "address", addr, "billing_ready", billing.Ready(), "test_invoices_enabled", billing.TestInvoicesEnabled)
			if command == "web" {
				go func() { <-botCtx.Done(); _ = server.Shutdown(context.Background()) }()
				e := server.Serve(listener)
				if e != http.ErrServerClosed {
					return e
				}
				return nil
			}
			go func() {
				if e := server.Serve(listener); e != nil && e != http.ErrServerClosed {
					slog.Error("admin web failed", "error", e)
				}
			}()
			defer server.Shutdown(context.Background())
		}
		if command == "web" {
			return fmt.Errorf("web listener disabled")
		}
		return b.Run(botCtx)
	}
	if command != "analyze" && command != "inspect" && command != "backtest" {
		return fmt.Errorf("unknown command")
	}
	interval := 5 * time.Minute
	if command == "analyze" && len(os.Args) > 2 {
		fs := flag.NewFlagSet("analyze", flag.ContinueOnError)
		symbol := fs.String("symbol", cfg.Symbol, "symbol")
		frame := fs.String("timeframe", cfg.Timeframe, "native interval")
		style := fs.String("style", "day", "trading style")
		if e := fs.Parse(os.Args[2:]); e != nil {
			return e
		}
		f, e := catalog.ParseFrame(*frame)
		if e != nil {
			return e
		}
		d, e := (engine.Engine{Market: md, AI: model, MinRR: cfg.MinRR, Buffer: time.Duration(cfg.Buffer) * time.Second}).Analyze(ctx, engine.Request{Symbol: *symbol, Timeframe: f, Style: *style, Language: "fa"})
		if e != nil {
			return e
		}
		return emit(d)
	}
	buffer := time.Duration(cfg.Buffer) * time.Second
	count := cfg.Count
	if command == "backtest" {
		count = 1000
	}
	candles, e := md.Fetch(ctx, cfg.Symbol, cfg.Timeframe, count+2)
	if e != nil {
		return e
	}
	now := time.Now().UTC()
	candles = marketdata.Closed(candles, now, interval, buffer)
	if command == "backtest" {
		fs := flag.NewFlagSet("backtest", flag.ContinueOnError)
		samples := fs.Int("samples", 20, "maximum AI decisions")
		horizon := fs.Int("horizon", cfg.Horizon, "forward candles")
		cost := fs.Float64("cost-r", 0, "modeled round-trip cost in R")
		if e = fs.Parse(os.Args[2:]); e != nil {
			return e
		}
		if *samples < 1 || *samples > 100 || (*horizon != 6 && *horizon != 12) {
			return fmt.Errorf("samples must be 1..100; horizon 6 or 12")
		}
		r, e := backtest.Run(ctx, candles, *samples, *horizon, cfg.MinRR, *cost, analyst{model})
		if e != nil {
			return e
		}
		save("backtest.json", r)
		return emit(r)
	}
	if len(candles) < 500 {
		return fmt.Errorf("provider returned %d closed candles; need 500", len(candles))
	}
	candles = candles[len(candles)-500:]
	s, e := snapshot.Build(cfg.Symbol, "M5", candles, now, interval, buffer)
	if e != nil {
		return e
	}
	if command == "inspect" {
		return emit(s)
	}
	if !s.Quality.Fresh {
		return emit(map[string]any{"final": "ANALYSIS_UNAVAILABLE", "reasons": []string{"market data stale or gapped"}, "quality": s.Quality})
	}
	a, e := model.Analyze(ctx, s)
	if e != nil {
		return emit(map[string]any{"final": "ANALYSIS_UNAVAILABLE", "reasons": []string{e.Error()}})
	}
	d := risk.Validate(s, a.Signal, cfg.MinRR)
	d.Model = cfg.Model
	d.PromptVersion = ai.PromptVersion
	d.LatencyMS = a.LatencyMS
	d.PromptTokens = a.PromptTokens
	d.CompletionTokens = a.CompletionTokens
	save("latest_analysis.json", d)
	return emit(d)
}
