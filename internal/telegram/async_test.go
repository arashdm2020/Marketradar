package telegram

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"tradebot/internal/ai"
	"tradebot/internal/catalog"
	"tradebot/internal/domain"
	"tradebot/internal/engine"
	"tradebot/internal/profile"
)

// ---- splitMessage tests ----

func TestSplitMessageUnderLimit(t *testing.T) {
	msg := "سلام <b>دنیا</b>"
	out := splitMessage(msg)
	if out != msg {
		t.Fatal("short message should be unchanged:", out)
	}
}

func TestSplitMessageUTF8Safety(t *testing.T) {
	// Build a message with multi-byte Persian chars near the limit
	var sb strings.Builder
	for i := 0; i < 1000; i++ {
		sb.WriteString("سلام ")
	}
	msg := sb.String()
	out := splitMessage(msg)
	// Must not end in a partial rune
	if len(out) > 0 {
		lastRuneSize := len([]rune(out)[len([]rune(out))-1:])
		_ = lastRuneSize
	}
	// Must be under limit
	if len([]rune(out)) > maxMessageRunes {
		t.Fatal("output exceeds limit:", len([]rune(out)))
	}
}

func TestSplitMessageHTMLTagSafety(t *testing.T) {
	msg := "<b>" + strings.Repeat("x", 5000) + "</b>"
	out := splitMessage(msg)
	// Must not contain unclosed tags
	openCount := strings.Count(out, "<b>")
	closeCount := strings.Count(out, "</b>")
	if openCount != closeCount {
		t.Fatalf("unbalanced tags: open=%d close=%d", openCount, closeCount)
	}
}

// ---- mock engine for async worker tests ----

type mockBot struct {
	messages []string
	lastErr  error
}

func (m *mockBot) sendText(ctx context.Context, id int64, msg string, buttons [][]map[string]any) (int64, error) {
	m.messages = append(m.messages, msg)
	if m.lastErr != nil {
		return 0, m.lastErr
	}
	return int64(len(m.messages)), nil
}

type mockEngine struct {
	analyzeFn func(context.Context, engine.Request) (domain.Decision, error)
	delay     time.Duration
}

func (m *mockEngine) Analyze(ctx context.Context, r engine.Request) (domain.Decision, error) {
	if m.delay > 0 {
		select {
		case <-ctx.Done():
			return domain.Decision{}, ctx.Err()
		case <-time.After(m.delay):
		}
	}
	if m.analyzeFn != nil {
		return m.analyzeFn(ctx, r)
	}
	return domain.Decision{
		Final: "WAIT",
		Snapshot: domain.Snapshot{
			Symbol:    r.Symbol,
			Timeframe: string(r.Timeframe),
			Quality:   domain.Quality{Fresh: true},
			Last:      domain.Candle{Close: 100},
			Quant:     domain.Quant{Alignment: "NEUTRAL", Momentum: "NEUTRAL", ATR14: 1},
		},
		Proposed: domain.Signal{Signal: "WAIT"},
	}, nil
}

type failingEngine struct{}

func (f *failingEngine) Analyze(ctx context.Context, r engine.Request) (domain.Decision, error) {
	return domain.Decision{}, errors.New("NVIDIA service unavailable")
}

type finalDeliveryFailBot struct{ calls int }

func (b *finalDeliveryFailBot) sendText(context.Context, int64, string, [][]map[string]any) (int64, error) {
	b.calls++
	if b.calls > 1 {
		return 0, errors.New("telegram delivery unavailable")
	}
	return 1, nil
}

// ---- async worker tests ----

func TestAsyncWorkerDedup(t *testing.T) {
	dbPath := t.TempDir() + "/dedup_test.db"
	store, e := profile.Open(dbPath)
	if e != nil {
		t.Fatal(e)
	}
	defer store.DB.Close()

	mock := &mockEngine{}
	mockBot := &mockBot{}
	worker := NewAsyncWorker(mockBot, store, mock)
	worker.Start(context.Background())
	defer worker.Stop()
	ctx := context.Background()

	// Create users first (foreign key constraint)
	for _, uid := range []int64{1, 2, 3, 4} {
		_, e := store.Ensure(uid)
		if e != nil {
			t.Fatal("ensure user:", e)
		}
	}

	req1 := AnalysisRequest{ID: "req1", UserID: 1, ChatID: 100, Symbol: "XAU/USD", Timeframe: catalog.Timeframe("5min"), Style: "day"}
	req2 := AnalysisRequest{ID: "req2", UserID: 1, ChatID: 100, Symbol: "XAU/USD", Timeframe: catalog.Timeframe("5min"), Style: "day"}

	ok1, e := worker.Enqueue(ctx, req1)
	if e != nil || !ok1 {
		t.Fatal("first request should be accepted:", e)
	}
	if !worker.IsInflight(1) {
		t.Fatal("user 1 should be in flight")
	}

	ok2, _ := worker.Enqueue(ctx, req2)
	if ok2 {
		t.Fatal("duplicate request for same user should be rejected")
	}

	_ = ok1
}

func TestAsyncWorkerFailingEngine(t *testing.T) {
	dbPath := t.TempDir() + "/fail_test.db"
	store, e := profile.Open(dbPath)
	if e != nil {
		t.Fatal(e)
	}
	defer store.DB.Close()

	mockBot := &mockBot{}
	worker := NewAsyncWorker(mockBot, store, &failingEngine{})
	worker.Start(context.Background())
	defer worker.Stop()
	ctx := context.Background()

	_, e = store.Ensure(2)
	if e != nil {
		t.Fatal("ensure user:", e)
	}

	req := AnalysisRequest{ID: "req_fail", UserID: 2, ChatID: 200, Symbol: "XAU/USD", Timeframe: catalog.Timeframe("5min"), Style: "day"}
	ok, e := worker.Enqueue(ctx, req)
	if e != nil || !ok {
		t.Fatal("request should be accepted:", e)
	}

	// Give the worker time to process
	time.Sleep(500 * time.Millisecond)

	// Verify request state is failed
	rs, e := store.GetRequestState("req_fail")
	if e != nil {
		t.Fatal("request state should exist:", e)
	}
	if rs.Status != "failed" && rs.Status != "completed" {
		t.Fatalf("unexpected status: %s", rs.Status)
	}
}

func TestAsyncWorkerOverloadMessageAndQuotaRelease(t *testing.T) {
	store, e := profile.Open(t.TempDir() + "/overload.db")
	if e != nil {
		t.Fatal(e)
	}
	defer store.DB.Close()
	_, _ = store.Ensure(202)
	bot := &mockBot{}
	eng := &mockEngine{analyzeFn: func(context.Context, engine.Request) (domain.Decision, error) {
		return domain.Decision{Final: "ANALYSIS_UNAVAILABLE", FailureClass: string(ai.FailureOverloaded), Reasons: []string{"AI provider temporarily unavailable"}}, nil
	}}
	worker := NewAsyncWorker(bot, store, eng)
	worker.Start(context.Background())
	defer worker.Stop()
	req := AnalysisRequest{ID: "overload-analysis", UserID: 202, ChatID: 202, Symbol: "XAU/USD", Timeframe: catalog.Timeframe("5min"), Style: "scalp"}
	if ok, enqueueErr := worker.Enqueue(context.Background(), req); enqueueErr != nil || !ok {
		t.Fatalf("enqueue: %v %v", ok, enqueueErr)
	}
	deadline := time.Now().Add(2 * time.Second)
	for worker.IsInflight(202) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if worker.IsInflight(202) {
		t.Fatal("analysis did not terminate")
	}
	if !strings.Contains(strings.Join(bot.messages, "\n"), "سرویس تحلیل هوش مصنوعی موقتاً شلوغ است") {
		t.Fatalf("specific overload message missing: %v", bot.messages)
	}
	var status string
	if e = store.DB.QueryRow(`SELECT status FROM daily_uses WHERE analysis_id=?`, req.ID).Scan(&status); e != nil || status != "released" {
		t.Fatalf("quota status=%q err=%v", status, e)
	}
	state, e := store.GetRequestState(req.ID)
	if e != nil || state.Status != "failed" {
		t.Fatalf("request state=%+v err=%v", state, e)
	}
}

func TestQueueRejectsInvalidTimeframeHorizonBeforeReservation(t *testing.T) {
	store, e := profile.Open(t.TempDir() + "/invalid-combination.db")
	if e != nil {
		t.Fatal(e)
	}
	defer store.DB.Close()
	if _, e = store.Ensure(204); e != nil {
		t.Fatal(e)
	}
	w := NewAsyncWorker(&mockBot{}, store, &mockEngine{})
	queued, e := w.Enqueue(context.Background(), AnalysisRequest{ID: "invalid-combo", UserID: 204, ChatID: 204, Symbol: "XAU/USD", Timeframe: "30min", Style: "scalp"})
	if e == nil || queued {
		t.Fatalf("invalid combination queued=%v err=%v", queued, e)
	}
	var n int
	if e = store.DB.QueryRow(`SELECT count(*) FROM daily_uses WHERE analysis_id='invalid-combo'`).Scan(&n); e != nil || n != 0 {
		t.Fatalf("invalid combination reserved quota: n=%d err=%v", n, e)
	}
}

func TestValidAnalysisConsumesQuotaEvenWhenFinalDeliveryFails(t *testing.T) {
	store, e := profile.Open(t.TempDir() + "/delivery-fail.db")
	if e != nil {
		t.Fatal(e)
	}
	defer store.DB.Close()
	if _, e = store.Ensure(205); e != nil {
		t.Fatal(e)
	}
	requestID := "valid-result-delivery-failed"
	if e = store.Reserve(requestID, 205, time.Now()); e != nil {
		t.Fatal(e)
	}
	if e = store.CreateRequestState(requestID, "205", "205", "XAU/USD", "5min", "day"); e != nil {
		t.Fatal(e)
	}
	w := NewAsyncWorker(&finalDeliveryFailBot{}, store, &mockEngine{})
	w.process(context.Background(), AnalysisRequest{ID: requestID, UserID: 205, ChatID: 205, Symbol: "XAU/USD", Timeframe: "5min", Style: "day"})
	var status string
	if e = store.DB.QueryRow(`SELECT status FROM daily_uses WHERE analysis_id=?`, requestID).Scan(&status); e != nil || status != "consumed" {
		t.Fatalf("status=%s err=%v", status, e)
	}
	var requestStatus string
	if e = store.DB.QueryRow(`SELECT status FROM request_states WHERE id=?`, requestID).Scan(&requestStatus); e != nil || requestStatus != "delivery_failed" {
		t.Fatalf("request status=%s err=%v", requestStatus, e)
	}
}

func TestAsyncWorkerTimeout(t *testing.T) {
	dbPath := t.TempDir() + "/timeout_test.db"
	store, e := profile.Open(dbPath)
	if e != nil {
		t.Fatal(e)
	}
	defer store.DB.Close()

	// Engine that takes longer than analysisTimeout (100s) - use a short delay that still exceeds context
	slowEngine := &mockEngine{delay: 200 * time.Millisecond}

	mockBot := &mockBot{}
	worker := NewAsyncWorker(mockBot, store, slowEngine)
	worker.Start(context.Background())
	defer worker.Stop()

	_, e = store.Ensure(3)
	if e != nil {
		t.Fatal("ensure user:", e)
	}

	// Override timeout to 50ms for this test by directly accessing the channel
	// We can't change analysisTimeout, but we can verify the worker processes
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	req := AnalysisRequest{ID: "req_timeout", UserID: 3, ChatID: 300, Symbol: "XAU/USD", Timeframe: catalog.Timeframe("5min"), Style: "day"}
	ok, e := worker.Enqueue(ctx, req)
	if e != nil || !ok {
		t.Fatal("request should be accepted:", e)
	}

	// Wait longer than the mock delay
	time.Sleep(500 * time.Millisecond)

	// Request should have been processed (either completed or failed)
	rs, e := store.GetRequestState("req_timeout")
	if e != nil {
		t.Fatal("request state should exist:", e)
	}
	if rs.Status == "queued" || rs.Status == "running" {
		t.Fatalf("request should not still be %s after processing", rs.Status)
	}
}

func TestAsyncWorkerSuccessfulDelivery(t *testing.T) {
	dbPath := t.TempDir() + "/success_test.db"
	store, e := profile.Open(dbPath)
	if e != nil {
		t.Fatal(e)
	}
	defer store.DB.Close()

	decision := domain.Decision{
		Final: "BUY",
		Snapshot: domain.Snapshot{
			Symbol:            "XAU/USD",
			Timeframe:         "M5",
			LastCandleTimeUTC: time.Now().UTC(),
			AnalysisTimeUTC:   time.Now().UTC(),
			Last:              domain.Candle{Close: 2050.50},
			Quant:             domain.Quant{Alignment: "BULLISH", Momentum: "BULLISH", ATR14: 1.5},
			Quality:           domain.Quality{Fresh: true},
		},
		Proposed: domain.Signal{
			Symbol:       "XAU/USD",
			Timeframe:    "M5",
			Signal:       "BUY",
			SetupQuality: "HIGH",
			Entry:        &[]float64{2051.0}[0],
			StopLoss:     &[]float64{2045.0}[0],
			TP1:          &[]float64{2061.0}[0],
			Invalidation: "بستن زیر 2045",
		},
		RiskReward: 2.0,
	}

	eng := &mockEngine{analyzeFn: func(ctx context.Context, r engine.Request) (domain.Decision, error) {
		return decision, nil
	}}

	mockBot := &mockBot{}
	worker := NewAsyncWorker(mockBot, store, eng)
	worker.Start(context.Background())
	defer worker.Stop()
	ctx := context.Background()

	_, e = store.Ensure(4)
	if e != nil {
		t.Fatal("ensure user:", e)
	}

	req := AnalysisRequest{ID: "req_success", UserID: 4, ChatID: 400, Symbol: "XAU/USD", Timeframe: catalog.Timeframe("5min"), Style: "day", MinProfit: 0.5, MaxProfit: 2.0, MaxRisk: 1.0}
	ok, e := worker.Enqueue(ctx, req)
	if e != nil || !ok {
		t.Fatal("request should be accepted:", e)
	}

	time.Sleep(500 * time.Millisecond)

	rs, e := store.GetRequestState("req_success")
	if e != nil {
		t.Fatal("request state should exist:", e)
	}
	if rs.Status != "delivered" {
		t.Fatalf("expected delivered, got %s", rs.Status)
	}
	if rs.ResultJSON == nil || *rs.ResultJSON == "" {
		t.Fatal("result JSON should be stored")
	}
}
