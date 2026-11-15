package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"tradebot/internal/domain"
)

const validWaitResponse = `{"choices":[{"message":{"content":"{\"symbol\":\"XAU/USD\",\"timeframe\":\"M5\",\"market_regime\":\"RANGE\",\"trend\":\"NEUTRAL\",\"signal\":\"WAIT\",\"entry\":null,\"stop_loss\":null,\"take_profit_1\":null,\"take_profit_2\":null,\"reasoning\":[],\"support_levels\":[],\"resistance_levels\":[],\"invalidation\":\"\",\"setup_quality\":\"LOW\",\"last_candle_time_utc\":\"2026-01-01T00:00:00Z\"}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":20}}`

func testSnapshot() domain.Snapshot {
	return domain.Snapshot{Symbol: "XAU/USD", Timeframe: "M5"}
}

func fastResilience() *Resilience {
	r := NewResilience()
	r.Policy.BaseDelay = 10 * time.Millisecond
	r.Policy.MaxDelay = 100 * time.Millisecond
	r.Policy.OverallTimeout = time.Second
	r.Policy.Rand = func() float64 { return 0.5 }
	return r
}

func TestTransientRetryHonorsRetryAfterAndRecordsAttempts(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := requests.Add(1)
		if n < 3 {
			if n == 1 {
				w.Header().Set("Retry-After", "1")
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"code":503,"message":"Service temporarily overloaded"}`))
			return
		}
		_, _ = w.Write([]byte(validWaitResponse))
	}))
	defer server.Close()
	r := fastResilience()
	var delays []time.Duration
	r.Policy.Sleep = func(_ context.Context, d time.Duration) error { delays = append(delays, d); return nil }
	var attempts []Attempt
	c := Client{Key: "test", BaseURL: server.URL, Model: "retry-model", Resilience: r, OnAttempt: func(a Attempt) { attempts = append(attempts, a) }}
	if _, e := c.Analyze(context.Background(), testSnapshot()); e != nil {
		t.Fatal(e)
	}
	if requests.Load() != 3 || len(attempts) != 3 {
		t.Fatalf("requests=%d attempts=%d", requests.Load(), len(attempts))
	}
	if len(delays) != 2 || delays[0] != time.Second || delays[1] != 20*time.Millisecond {
		t.Fatalf("unexpected backoff: %v", delays)
	}
	if attempts[0].FailureClass != string(FailureOverloaded) || attempts[2].Status != "completed" {
		t.Fatalf("attempt telemetry: %+v", attempts)
	}
}

func TestOverallDeadlineStopsRetries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		_, _ = w.Write([]byte(validWaitResponse))
	}))
	defer server.Close()
	r := fastResilience()
	r.Policy.OverallTimeout = 30 * time.Millisecond
	c := Client{Key: "test", BaseURL: server.URL, Model: "deadline-model", Resilience: r}
	_, e := c.Analyze(context.Background(), testSnapshot())
	if Classify(e) != FailureDeadline {
		t.Fatalf("classification=%s error=%v", Classify(e), e)
	}
}

func TestCircuitBreakerOpenAndSingleHalfOpenProbe(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	breaker := NewCircuitBreaker(2, time.Minute)
	breaker.now = func() time.Time { return now }
	r := fastResilience()
	r.Policy.MaxAttempts = 1
	r.Breaker = breaker
	c := Client{Key: "test", BaseURL: server.URL, Model: "breaker-model", Resilience: r}
	for i := 0; i < 2; i++ {
		_, _ = c.Analyze(context.Background(), testSnapshot())
	}
	_, e := c.Analyze(context.Background(), testSnapshot())
	if Classify(e) != FailureCircuit || requests.Load() != 2 {
		t.Fatalf("circuit did not open: class=%s requests=%d", Classify(e), requests.Load())
	}
	now = now.Add(time.Minute + time.Second)
	var wg sync.WaitGroup
	allowed := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, ok := breaker.allow("nvidia/breaker-model"); allowed <- ok }()
	}
	wg.Wait()
	close(allowed)
	probes := 0
	for ok := range allowed {
		if ok {
			probes++
		}
	}
	if probes != 1 {
		t.Fatalf("half-open probes=%d", probes)
	}
}

func TestFallbackRequiresExplicitCompatibilityVerification(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(validWaitResponse)) }))
	defer fallback.Close()
	r := fastResilience()
	r.Policy.MaxAttempts = 1
	f := Client{Key: "test", BaseURL: fallback.URL, Model: "verified-fallback", Resilience: r}
	c := Client{Key: "test", BaseURL: primary.URL, Model: "primary", Resilience: r, Fallback: &f, FallbackEnabled: true}
	if _, e := c.Analyze(context.Background(), testSnapshot()); e == nil {
		t.Fatal("unverified fallback was used")
	}
	c.FallbackVerified = true
	if _, e := c.Analyze(context.Background(), testSnapshot()); e != nil {
		t.Fatalf("verified fallback unavailable: %v", e)
	}
}

func TestParseRetryAfterHTTPDate(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := parseRetryAfter(now.Add(3*time.Second).Format(http.TimeFormat), now); got != 3*time.Second {
		t.Fatalf("retry-after=%s", got)
	}
}
