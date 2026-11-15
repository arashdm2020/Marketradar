package ai

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type FailureClass string

const (
	FailureOverloaded FailureClass = "provider_overloaded"
	FailureRateLimit  FailureClass = "provider_rate_limited"
	FailureUpstream   FailureClass = "provider_upstream"
	FailureTransport  FailureClass = "transient_transport"
	FailureDeadline   FailureClass = "deadline_exceeded"
	FailureCircuit    FailureClass = "circuit_open"
	FailurePermanent  FailureClass = "permanent_provider_error"
)

type ProviderError struct {
	Class        FailureClass
	Provider     string
	Model        string
	HTTPStatus   int
	Attempts     int
	TotalLatency time.Duration
	BreakerState string
	Cause        error
}

func (e *ProviderError) Error() string {
	switch e.Class {
	case FailureOverloaded, FailureRateLimit, FailureUpstream, FailureTransport:
		return "AI provider temporarily unavailable"
	case FailureDeadline:
		return "AI provider deadline exceeded"
	case FailureCircuit:
		return "AI provider circuit open"
	default:
		return "AI provider request failed"
	}
}
func (e *ProviderError) Unwrap() error { return e.Cause }

func Classify(err error) FailureClass {
	var pe *ProviderError
	if errors.As(err, &pe) {
		return pe.Class
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return FailureDeadline
	}
	return FailurePermanent
}

type RetryPolicy struct {
	MaxAttempts    int
	BaseDelay      time.Duration
	MaxDelay       time.Duration
	OverallTimeout time.Duration
	Sleep          func(context.Context, time.Duration) error
	Rand           func() float64
	Now            func() time.Time
}

type breakerEntry struct {
	failures  int
	openUntil time.Time
	halfOpen  bool
}

type CircuitBreaker struct {
	mu        sync.Mutex
	entries   map[string]*breakerEntry
	threshold int
	cooldown  time.Duration
	now       func() time.Time
}

func NewCircuitBreaker(threshold int, cooldown time.Duration) *CircuitBreaker {
	if threshold < 1 {
		threshold = 3
	}
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}
	return &CircuitBreaker{entries: map[string]*breakerEntry{}, threshold: threshold, cooldown: cooldown, now: time.Now}
}

func (b *CircuitBreaker) allow(key string) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.entries[key]
	if e == nil {
		e = &breakerEntry{}
		b.entries[key] = e
	}
	now := b.now()
	if !e.openUntil.IsZero() && now.Before(e.openUntil) {
		return "open", false
	}
	if !e.openUntil.IsZero() {
		if e.halfOpen {
			return "half_open", false
		}
		e.halfOpen = true
		return "half_open", true
	}
	return "closed", true
}

func (b *CircuitBreaker) success(key string) {
	b.mu.Lock()
	delete(b.entries, key)
	b.mu.Unlock()
}

func (b *CircuitBreaker) failure(key string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.entries[key]
	if e == nil {
		e = &breakerEntry{}
		b.entries[key] = e
	}
	e.failures++
	if e.halfOpen || e.failures >= b.threshold {
		e.openUntil = b.now().Add(b.cooldown)
		e.halfOpen = false
		return "open"
	}
	return "closed"
}

type Resilience struct {
	Policy  RetryPolicy
	Breaker *CircuitBreaker
}

var defaultResilience = NewResilience()

func NewResilience() *Resilience {
	return &Resilience{
		Policy: RetryPolicy{
			MaxAttempts:    3,
			BaseDelay:      500 * time.Millisecond,
			MaxDelay:       5 * time.Second,
			OverallTimeout: 170 * time.Second,
			Sleep: func(ctx context.Context, d time.Duration) error {
				t := time.NewTimer(d)
				defer t.Stop()
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-t.C:
					return nil
				}
			},
			Rand: rand.Float64,
			Now:  time.Now,
		},
		Breaker: NewCircuitBreaker(3, 30*time.Second),
	}
}

type httpResult struct {
	Raw          []byte
	Status       int
	Attempts     int
	TotalLatency time.Duration
	Attempt      Attempt
	BreakerState string
}

func (r *Resilience) execute(ctx context.Context, client *http.Client, endpoint string, request func(context.Context) (*http.Request, error), base Attempt, onAttempt func(Attempt)) (httpResult, error) {
	if r == nil {
		r = defaultResilience
	}
	p := r.Policy
	if p.MaxAttempts < 1 {
		p.MaxAttempts = 1
	}
	if p.Now == nil {
		p.Now = time.Now
	}
	if p.Rand == nil {
		p.Rand = func() float64 { return 0.5 }
	}
	if p.Sleep == nil {
		p.Sleep = func(ctx context.Context, d time.Duration) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d):
				return nil
			}
		}
	}
	if p.OverallTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.OverallTimeout)
		defer cancel()
	}
	key := strings.ToLower(base.Provider + "/" + base.Model)
	state, allowed := r.Breaker.allow(key)
	start := p.Now()
	if !allowed {
		err := &ProviderError{Class: FailureCircuit, Provider: base.Provider, Model: base.Model, BreakerState: state}
		slog.Warn("ai_request_blocked", "provider", base.Provider, "model", base.Model, "circuit_breaker", state, "failure_class", err.Class)
		return httpResult{BreakerState: state}, err
	}

	for n := 1; n <= p.MaxAttempts; n++ {
		attempt := base
		attempt.StartedAt = p.Now()
		attempt.AttemptCount = n
		attempt.BreakerState = state
		req, e := request(ctx)
		if e != nil {
			return httpResult{}, e
		}
		resp, e := client.Do(req)
		attempt.LatencyMS = p.Now().Sub(attempt.StartedAt).Milliseconds()
		class := FailureTransport
		status := 0
		var raw []byte
		var retryAfter time.Duration
		if e == nil {
			status = resp.StatusCode
			attempt.HTTPStatus = status
			raw, e = io.ReadAll(io.LimitReader(resp.Body, 2<<20))
			resp.Body.Close()
			if e != nil {
				class = FailureTransport
			} else {
				class = classifyStatus(status)
				retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), p.Now())
			}
		} else if errors.Is(e, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			class = FailureDeadline
		} else if ne, ok := e.(net.Error); !ok || (!ne.Timeout() && !ne.Temporary()) {
			class = FailurePermanent
		}
		if e == nil && status >= 200 && status < 300 {
			r.Breaker.success(key)
			return httpResult{Raw: raw, Status: status, Attempts: n, TotalLatency: p.Now().Sub(start), Attempt: attempt, BreakerState: state}, nil
		}
		attempt.Status = "http_error"
		attempt.FailureClass = string(class)
		attempt.TotalLatencyMS = p.Now().Sub(start).Milliseconds()
		transient := class == FailureOverloaded || class == FailureRateLimit || class == FailureUpstream || class == FailureTransport
		if transient && n < p.MaxAttempts && ctx.Err() == nil {
			if onAttempt != nil {
				onAttempt(attempt)
			}
			delay := retryAfter
			if delay <= 0 {
				delay = p.BaseDelay << (n - 1)
				if p.MaxDelay > 0 && delay > p.MaxDelay {
					delay = p.MaxDelay
				}
				delay = time.Duration(float64(delay) * (0.75 + 0.5*p.Rand()))
			}
			slog.Warn("ai_retry", "provider", base.Provider, "model", base.Model, "http_status", status, "attempt", n, "failure_class", class, "backoff_ms", delay.Milliseconds(), "circuit_breaker", state)
			if sleepErr := p.Sleep(ctx, delay); sleepErr != nil {
				class = FailureDeadline
				e = sleepErr
				break
			}
			continue
		}
		breakerState := state
		if transient {
			breakerState = r.Breaker.failure(key)
		}
		pe := &ProviderError{Class: class, Provider: base.Provider, Model: base.Model, HTTPStatus: status, Attempts: n, TotalLatency: p.Now().Sub(start), BreakerState: breakerState, Cause: e}
		attempt.BreakerState = breakerState
		attempt.TotalLatencyMS = pe.TotalLatency.Milliseconds()
		return httpResult{Raw: raw, Status: status, Attempts: n, TotalLatency: pe.TotalLatency, Attempt: attempt, BreakerState: breakerState}, pe
	}
	pe := &ProviderError{Class: FailureDeadline, Provider: base.Provider, Model: base.Model, Attempts: p.MaxAttempts, TotalLatency: p.Now().Sub(start), BreakerState: state, Cause: ctx.Err()}
	return httpResult{Attempts: p.MaxAttempts, TotalLatency: pe.TotalLatency, BreakerState: state}, pe
}

func classifyStatus(status int) FailureClass {
	switch status {
	case http.StatusTooManyRequests:
		return FailureRateLimit
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		if status == http.StatusServiceUnavailable {
			return FailureOverloaded
		}
		return FailureUpstream
	default:
		return FailurePermanent
	}
}

func parseRetryAfter(raw string, now time.Time) time.Duration {
	if raw == "" {
		return 0
	}
	if seconds, e := strconv.Atoi(strings.TrimSpace(raw)); e == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, e := http.ParseTime(raw); e == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
}

func (e *ProviderError) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("class", string(e.Class)),
		slog.Int("http_status", e.HTTPStatus),
		slog.Int("attempts", e.Attempts),
		slog.Int64("total_latency_ms", e.TotalLatency.Milliseconds()),
		slog.String("circuit_breaker", e.BreakerState),
	)
}
