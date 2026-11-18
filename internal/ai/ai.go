package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"tradebot/internal/domain"
)

const PromptVersion = "v3-evidence-entry"

type Client struct {
	HTTP                *http.Client
	Key, BaseURL, Model string
	ModelCandles        int
	Provider            string
	MaxTokens           int
	Temperature         float64
	ReasoningBudget     int
	ReasoningEffort     string
	OnAttempt           func(Attempt)
	Resilience          *Resilience
	Fallback            *Client
	FallbackEnabled     bool
	FallbackVerified    bool
}
type Result struct {
	Signal                         domain.Signal
	LatencyMS                      int64
	PromptTokens, CompletionTokens int
	Raw                            json.RawMessage
}

func Parse(b []byte) (domain.Signal, error) {
	var s domain.Signal
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e := d.Decode(&s); e != nil {
		return s, e
	}
	if d.Decode(new(any)) != io.EOF {
		return s, fmt.Errorf("trailing JSON")
	}
	if s.Symbol == "" || s.Timeframe == "" || s.LastCandleTimeUTC == "" || s.SetupQuality == "" || s.Reasoning == nil {
		return s, fmt.Errorf("missing required signal fields")
	}
	if s.Signal != "BUY" && s.Signal != "SELL" && s.Signal != "WAIT" {
		return s, fmt.Errorf("invalid signal")
	}
	return s, nil
}
func (c Client) Analyze(ctx context.Context, s domain.Snapshot) (Result, error) {
	return c.AnalyzeWithContext(ctx, s, nil)
}
func (c Client) AnalyzeWithContext(ctx context.Context, s domain.Snapshot, userContext any) (Result, error) {
	result, e := c.analyzePrimaryWithContext(ctx, s, userContext)
	if e == nil || !c.FallbackEnabled {
		return result, e
	}
	if c.Fallback == nil || !c.FallbackVerified {
		return Result{}, e
	}
	fallback := *c.Fallback
	if fallback.OnAttempt == nil {
		fallback.OnAttempt = c.OnAttempt
	}
	if fallback.Resilience == nil {
		fallback.Resilience = c.Resilience
	}
	return fallback.analyzePrimaryWithContext(ctx, s, userContext)
}

func (c Client) analyzePrimaryWithContext(ctx context.Context, s domain.Snapshot, userContext any) (Result, error) {
	if c.Key == "" {
		return Result{}, fmt.Errorf("NVIDIA key missing")
	}
	sent := s.Candles
	if c.ModelCandles > 0 && c.ModelCandles < len(sent) {
		sent = sent[len(sent)-c.ModelCandles:]
	}
	candles := make([][5]any, 0, len(sent))
	for _, x := range sent {
		candles = append(candles, [5]any{x.Time.Format(time.RFC3339), x.Open, x.High, x.Low, x.Close})
	}
	payload, _ := json.Marshal(map[string]any{"symbol": s.Symbol, "timeframe": s.Timeframe, "analysis_time_utc": s.AnalysisTimeUTC, "last_candle_time_utc": s.LastCandleTimeUTC, "engine_candle_count": len(s.Candles), "model_candle_count": len(sent), "candles_columns": []string{"time_utc", "open", "high", "low", "close"}, "candles": candles, "quant": s.Quant, "quality": s.Quality, "user_preferences": userContext})
	system := `You are a market analyst using only the supplied closed candles and deterministic quant snapshot. Evaluate trend continuation and reversal. Weigh confirmed structure, swings, momentum, volatility, and actual support/resistance together; a minor indicator disagreement alone is not a veto. Distinguish material invalidation risk from ordinary uncertainty. Prefer BUY or SELL when a current closed-candle trigger, defensible entry, stop and target form a sufficiently strong setup; otherwise use WAIT. A breakout awaiting retest is conditional, not an immediate trade. Do not chase a move more than two ATR from the last close. Do not calculate indicators or invent levels, liquidity, order flow, volume, news, spread, quotes, or higher-timeframe evidence not supplied. Never present confidence as a calibrated win probability. All prices are indicative and are checked by Go risk rules.`
	system += ` Return exactly one compact JSON object with fields symbol,timeframe,market_regime,trend,signal,entry_trigger,trigger_kind,trigger_price,entry,stop_loss,take_profit_1,take_profit_2,reasoning,support_levels,resistance_levels,invalidation,setup_quality,last_candle_time_utc,conditional_setups. Signal is BUY, SELL or WAIT. For BUY/SELL, entry_trigger and invalidation must be specific nonempty Persian strings, entry/stop/TP and trigger_price must be numbers, trigger_kind must be CLOSE_ABOVE, CLOSE_BELOW, TOUCH, PULLBACK_BUY or PULLBACK_SELL, and setup_quality must be MEDIUM or HIGH. The trigger must already be confirmed by the last closed candle; otherwise use WAIT with a conditional setup. Use 1-3 brief evidence-based Persian reasoning statements. For WAIT, prices and trigger_price are null, entry_trigger and trigger_kind are empty, and reasoning identifies the missing evidence and an exact level or candle condition that would change the decision. If a valid future setup exists, provide at most two conditional_setups with the exact activation condition. Copy symbol, timeframe, timestamp and level prices from the snapshot; no extra fields.`
	system += ` Required JSON types: reasoning is an array of strings; support_levels and resistance_levels are arrays of numbers from quant levels; conditional_setups is an array of objects. entry, stop_loss, take_profit_1, take_profit_2 are numbers or null. setup_quality is LOW, MEDIUM or HIGH. Each conditional setup has exactly {"direction":"BUY","trigger":"Persian condition","trigger_kind":"CLOSE_ABOVE","trigger_price":0.0,"entry":0.0,"stop_loss":0.0,"take_profit_1":0.0,"take_profit_2":null,"invalidation":"Persian condition","expiration":"Persian condition"}; use CLOSE_ABOVE or CLOSE_BELOW at a supplied resistance or support, replace zeros with defensible prices, or omit it. Every invalidation is a string. Return no OHLC or indicator fields.`
	effort := os.Getenv("NVIDIA_REASONING_EFFORT")
	if effort == "" {
		effort = "medium"
	}
	if c.ReasoningEffort != "" {
		effort = c.ReasoningEffort
	}
	maxTokens := envInt("NVIDIA_MAX_TOKENS", 4096)
	if c.MaxTokens > 0 {
		maxTokens = c.MaxTokens
	}
	budget := envInt("NVIDIA_REASONING_BUDGET", 1024)
	if c.ReasoningBudget > 0 {
		budget = c.ReasoningBudget
	}
	temp := 1.0
	if v, e := strconv.ParseFloat(os.Getenv("NVIDIA_TEMPERATURE"), 64); e == nil {
		temp = v
	}
	if c.Temperature > 0 {
		temp = c.Temperature
	}
	provider := c.Provider
	if provider == "" {
		provider = "nvidia"
	}
	requestBody := map[string]any{"model": c.Model, "temperature": temp, "max_tokens": maxTokens, "stream": false, "messages": []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": string(payload)}}}
	if provider == "nvidia" {
		requestBody["reasoning_effort"] = effort
		requestBody["reasoning_budget"] = budget
	}
	if provider == "google" {
		requestBody = map[string]any{"systemInstruction": map[string]any{"parts": []map[string]string{{"text": system}}}, "contents": []map[string]any{{"role": "user", "parts": []map[string]string{{"text": string(payload)}}}}, "generationConfig": map[string]any{"temperature": temp, "maxOutputTokens": maxTokens, "responseMimeType": "application/json"}}
	}
	body, _ := json.Marshal(requestBody)
	h := c.HTTP
	if h == nil {
		h = &http.Client{Timeout: 150 * time.Second}
	}
	base := strings.TrimRight(c.BaseURL, "/")
	endpoint := base + "/chat/completions"
	if provider == "google" {
		endpoint = base + "/models/" + c.Model + ":generateContent"
	}
	start := time.Now()
	analysisID, channel := AnalysisContext(ctx)
	attempt := Attempt{AnalysisID: analysisID, Channel: channel, Provider: provider, Model: c.Model, StartedAt: start, Status: "transport_error"}
	defer func() {
		if c.OnAttempt != nil {
			if attempt.LatencyMS == 0 {
				attempt.LatencyMS = time.Since(attempt.StartedAt).Milliseconds()
			}
			attempt.TotalLatencyMS = time.Since(start).Milliseconds()
			c.OnAttempt(attempt)
		}
	}()
	resilience := c.Resilience
	if resilience == nil {
		resilience = defaultResilience
	}
	httpResult, e := resilience.execute(ctx, h, endpoint, func(requestCtx context.Context) (*http.Request, error) {
		req, requestErr := http.NewRequestWithContext(requestCtx, "POST", endpoint, bytes.NewReader(body))
		if requestErr != nil {
			return nil, requestErr
		}
		if provider == "google" {
			req.Header.Set("x-goog-api-key", c.Key)
		} else {
			req.Header.Set("Authorization", "Bearer "+c.Key)
		}
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	}, attempt, c.OnAttempt)
	if !httpResult.Attempt.StartedAt.IsZero() {
		attempt = httpResult.Attempt
	}
	if e != nil {
		if attempt.Status == "" {
			attempt.Status = "http_error"
		}
		attempt.FailureClass = string(Classify(e))
		attempt.BreakerState = httpResult.BreakerState
		slog.Warn("ai_inference_failed", "provider", provider, "model", c.Model, "http_status", attempt.HTTPStatus, "attempt_count", httpResult.Attempts, "total_latency_ms", httpResult.TotalLatency.Milliseconds(), "circuit_breaker", httpResult.BreakerState, "failure_class", Classify(e))
		return Result{}, e
	}
	raw := httpResult.Raw
	attempt.HTTPStatus = httpResult.Status
	attempt.AttemptCount = httpResult.Attempts
	attempt.BreakerState = httpResult.BreakerState
	attempt.TotalLatencyMS = httpResult.TotalLatency.Milliseconds()
	if provider == "google" {
		var g struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
				FinishReason string `json:"finishReason"`
			} `json:"candidates"`
			Usage struct {
				Prompt     int `json:"promptTokenCount"`
				Completion int `json:"candidatesTokenCount"`
			} `json:"usageMetadata"`
		}
		if e = json.Unmarshal(raw, &g); e != nil {
			return Result{}, e
		}
		attempt.PromptTokens = g.Usage.Prompt
		attempt.CompletionTokens = g.Usage.Completion
		if len(g.Candidates) > 0 {
			attempt.FinishReason = g.Candidates[0].FinishReason
		}
		if len(g.Candidates) != 1 || g.Candidates[0].FinishReason != "STOP" || len(g.Candidates[0].Content.Parts) != 1 {
			attempt.Status = "incomplete"
			return Result{}, fmt.Errorf("AI response incomplete")
		}
		content := strings.TrimSpace(g.Candidates[0].Content.Parts[0].Text)
		signal, e := Parse([]byte(content))
		if e != nil {
			attempt.Status = "invalid_response"
			return Result{}, fmt.Errorf("AI invalid signal: %w", e)
		}
		attempt.Status = "completed"
		return Result{signal, time.Since(start).Milliseconds(), g.Usage.Prompt, g.Usage.Completion, json.RawMessage(content)}, nil
	}
	var wire struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			Details          struct {
				ReasoningTokens *int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if e = json.Unmarshal(raw, &wire); e != nil {
		return Result{}, e
	}
	attempt.PromptTokens = wire.Usage.PromptTokens
	attempt.CompletionTokens = wire.Usage.CompletionTokens
	finish := ""
	length := 0
	if len(wire.Choices) > 0 {
		finish = wire.Choices[0].FinishReason
		attempt.FinishReason = finish
		length = len(wire.Choices[0].Message.Content)
	}
	mode := "full_history"
	if len(sent) < len(s.Candles) {
		mode = "recent_window_with_full_quant"
	}
	slog.Info("ai_inference", "provider", provider, "model", c.Model, "prompt_tokens", wire.Usage.PromptTokens, "completion_tokens", wire.Usage.CompletionTokens, "reasoning_tokens", reasoningValue(wire.Usage.Details.ReasoningTokens), "max_tokens", maxTokens, "reasoning_budget", budget, "reasoning_effort", effort, "finish_reason", finish, "content_bytes", length, "latency_ms", time.Since(start).Milliseconds(), "attempt_count", httpResult.Attempts, "circuit_breaker", httpResult.BreakerState, "input_mode", mode, "engine_candles", len(s.Candles), "model_candles", len(candles))
	if len(wire.Choices) != 1 || wire.Choices[0].FinishReason != "stop" {
		attempt.Status = "incomplete"
		if len(wire.Choices) == 1 {
			return Result{}, fmt.Errorf("NVIDIA response incomplete: %s", wire.Choices[0].FinishReason)
		}
		return Result{}, fmt.Errorf("NVIDIA response missing choices")
	}
	content := strings.TrimSpace(wire.Choices[0].Message.Content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimSuffix(content, "```")
	signal, e := Parse([]byte(strings.TrimSpace(content)))
	if e != nil {
		attempt.Status = "invalid_response"
		return Result{}, fmt.Errorf("NVIDIA invalid signal: %w", e)
	}
	attempt.Status = "completed"
	return Result{signal, time.Since(start).Milliseconds(), wire.Usage.PromptTokens, wire.Usage.CompletionTokens, json.RawMessage(content)}, nil
}
func envInt(k string, d int) int {
	v, e := strconv.Atoi(os.Getenv(k))
	if e != nil || v < 0 {
		return d
	}
	return v
}

func reasoningValue(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}
