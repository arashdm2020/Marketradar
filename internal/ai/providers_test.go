package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"tradebot/internal/domain"
)

func TestGoogleProviderStructuredResponse(t *testing.T) {
	signal := `{"symbol":"XAU/USD","timeframe":"M5","market_regime":"RANGING","trend":"NEUTRAL","signal":"WAIT","entry":null,"stop_loss":null,"take_profit_1":null,"take_profit_2":null,"reasoning":["شرط ورود تأیید نشده است"],"support_levels":[],"resistance_levels":[],"invalidation":"","setup_quality":"LOW","last_candle_time_utc":"2026-11-14T10:00:00Z"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "generateContent") || r.Header.Get("x-goog-api-key") != "test-key" {
			t.Error("wrong Google request")
		}
		v := map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": signal}}}, "finishReason": "STOP"}}, "usageMetadata": map[string]any{"promptTokenCount": 10, "candidatesTokenCount": 20}}
		_ = json.NewEncoder(w).Encode(v)
	}))
	defer server.Close()
	c := Client{Provider: "google", Key: "test-key", BaseURL: server.URL, Model: "gemini-test", MaxTokens: 256}
	s := domain.Snapshot{Symbol: "XAU/USD", Timeframe: "M5", AnalysisTimeUTC: time.Date(2026, 11, 14, 10, 5, 0, 0, time.UTC), Candles: []domain.Candle{{Time: time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC), Open: 1, High: 2, Low: 1, Close: 2}}}
	result, e := c.Analyze(context.Background(), s)
	if e != nil || result.Signal.Signal != "WAIT" || result.PromptTokens != 10 {
		t.Fatalf("Google result: %+v %v", result, e)
	}
}
