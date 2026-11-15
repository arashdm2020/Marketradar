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

func TestSameSnapshotCanSendFiftyOrFiveHundredCandles(t *testing.T) {
	var seen []int
	var attempts []Attempt
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Messages) < 2 {
			t.Error("invalid model request")
			return
		}
		var payload struct {
			Engine  int               `json:"engine_candle_count"`
			Model   int               `json:"model_candle_count"`
			Candles []json.RawMessage `json:"candles"`
		}
		if json.Unmarshal([]byte(body.Messages[1].Content), &payload) != nil || payload.Engine != 500 || payload.Model != len(payload.Candles) {
			t.Error("candle metadata mismatch")
		}
		seen = append(seen, payload.Model)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"length","message":{"content":""}}]}`))
	}))
	defer server.Close()
	snap := domain.Snapshot{Symbol: "XAU/USD", Timeframe: "M5", AnalysisTimeUTC: time.Now().UTC()}
	for i := 0; i < 500; i++ {
		snap.Candles = append(snap.Candles, domain.Candle{Time: time.Now().UTC().Add(-time.Duration(500-i) * 5 * time.Minute), Open: 1, High: 2, Low: 1, Close: 2})
	}
	for _, n := range []int{500, 50} {
		c := Client{Provider: "nvidia", Key: "test", BaseURL: server.URL, Model: "test", ModelCandles: n, OnAttempt: func(a Attempt) { attempts = append(attempts, a) }}
		_, e := c.Analyze(context.Background(), snap)
		if e == nil || !strings.Contains(e.Error(), "incomplete") {
			t.Fatalf("response handling %v", e)
		}
	}
	if len(seen) != 2 || seen[0] != 500 || seen[1] != 50 {
		t.Fatalf("window sizes %v", seen)
	}
	if len(attempts) != 2 || attempts[0].Status != "incomplete" || attempts[1].FinishReason != "length" {
		t.Fatalf("attempt telemetry %+v", attempts)
	}
}
