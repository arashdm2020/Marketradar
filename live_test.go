package main

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"
	"tradebot/internal/ai"
	"tradebot/internal/catalog"
	"tradebot/internal/config"
	"tradebot/internal/engine"
	"tradebot/internal/marketdata"
	"tradebot/internal/profile"
	"tradebot/internal/snapshot"
	"tradebot/internal/telegram"
)

func TestLiveAnalysis(t *testing.T) {
	if os.Getenv("RUN_LIVE") != "1" {
		t.Skip("explicit opt-in required")
	}
	c := config.Load()
	store, e := profile.Open("data/live_test.db")
	if e != nil {
		t.Fatal(e)
	}
	defer store.DB.Close()
	u, e := store.Ensure(-1)
	if e != nil {
		t.Fatal(e)
	}
	u.MinProfit = .5
	u.MaxProfit = 2
	u.MaxRisk = 1
	u.Markets = "XAU/USD"
	u.Completed = true
	u.Timezone = "Asia/Tehran"
	if e = store.Save(u); e != nil {
		t.Fatal(e)
	}
	u, e = store.Get(-1)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	d, e := (engine.Engine{Market: marketdata.Client{Key: c.TwelveKey}, AI: ai.Client{Key: c.NVIDIAKey, BaseURL: c.BaseURL, Model: c.Model}, MinRR: c.MinRR, Buffer: 30 * time.Second}).Analyze(ctx, engine.Request{Symbol: "XAU/USD", Timeframe: catalog.Timeframe("5min"), Style: "day", MinProfit: u.MinProfit, MaxProfit: u.MaxProfit, MaxRisk: u.MaxRisk, Language: "fa"})
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.MarshalIndent(d, "", "  ")
	os.WriteFile("data/live_analysis.json", raw, 0600)
	report := telegram.Render(d, "XAU/USD", "5min", "day", u)
	os.WriteFile("data/live_report.html", []byte(report), 0600)
	if d.Final == "ANALYSIS_UNAVAILABLE" {
		t.Fatalf("unavailable: %v", d.Reasons)
	}
	if len(d.Snapshot.Candles) != 500 {
		t.Fatal("wrong context")
	}
	t.Logf("decision=%s candles=500 tokens=%d/%d latency_ms=%d scenarios=%d report_bytes=%d", d.Final, d.PromptTokens, d.CompletionTokens, d.LatencyMS, len(d.ValidatedConditional), len(report))
}

func TestLiveInputComparison(t *testing.T) {
	if os.Getenv("RUN_COMPARE") != "1" {
		t.Skip("explicit opt-in required")
	}
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	candles, e := (marketdata.Client{Key: cfg.TwelveKey}).Fetch(ctx, "XAU/USD", "5min", 502)
	if e != nil {
		t.Fatal(e)
	}
	candles = marketdata.Closed(candles, time.Now().UTC(), 5*time.Minute, 30*time.Second)
	if len(candles) < 500 {
		t.Fatalf("only %d closed candles", len(candles))
	}
	candles = candles[len(candles)-500:]
	at := candles[len(candles)-1].Time.Add(5*time.Minute + 30*time.Second)
	snap, e := snapshot.Build("XAU/USD", "M5", candles, at, 5*time.Minute, 30*time.Second)
	if e != nil {
		t.Fatal(e)
	}
	results := map[string]any{}
	failed := false
	for _, n := range []int{500, 50} {
		client := ai.Client{Key: cfg.NVIDIAKey, BaseURL: cfg.BaseURL, Model: cfg.Model, ModelCandles: n}
		start := time.Now()
		r, e := client.Analyze(ctx, snap)
		result := map[string]any{"engine_candles": 500, "model_candles": n, "latency_ms": time.Since(start).Milliseconds()}
		if e != nil {
			failed = true
			result["error"] = e.Error()
		} else {
			result["signal"] = r.Signal.Signal
			result["prompt_tokens"] = r.PromptTokens
			result["completion_tokens"] = r.CompletionTokens
		}
		results[strconv.Itoa(n)] = result
		t.Logf("mode=%d result=%v", n, result)
	}
	raw, _ := json.MarshalIndent(results, "", "  ")
	if e = os.WriteFile("data/live_input_comparison.json", raw, 0600); e != nil {
		t.Fatal(e)
	}
	if failed {
		t.Fatal("provider rejected one or both inference requests; comparison unavailable")
	}
}
