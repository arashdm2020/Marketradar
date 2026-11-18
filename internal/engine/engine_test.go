package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"tradebot/internal/ai"
	"tradebot/internal/catalog"
	"tradebot/internal/marketdata"
)

func TestAnalysisSendsExactClosedHistory(t *testing.T) {
	for _, frame := range []catalog.Timeframe{"1min", "5min", "15min", "30min", "1h", "4h"} {
		t.Run(string(frame), func(t *testing.T) {
			want, _ := catalog.RequiredCandles(frame)
			var received int
			var lastSent time.Time
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/market" {
					size, _ := strconv.Atoi(r.URL.Query().Get("outputsize"))
					interval := frame.Duration()
					last := time.Now().UTC().Truncate(interval)
					values := make([]map[string]string, size)
					for i := range values {
						v := 100.0 + float64(i%7)*0.01
						values[i] = map[string]string{"datetime": last.Add(-time.Duration(i) * interval).Format("2006-01-02 15:04:05"), "open": fmt.Sprint(v), "high": fmt.Sprint(v + 1), "low": fmt.Sprint(v - 1), "close": fmt.Sprint(v)}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"values": values})
					return
				}
				var req struct {
					Messages []struct{ Role, Content string } `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				var input struct {
					EngineCount int     `json:"engine_candle_count"`
					ModelCount  int     `json:"model_candle_count"`
					Candles     [][]any `json:"candles"`
				}
				if err := json.Unmarshal([]byte(req.Messages[1].Content), &input); err != nil {
					t.Error(err)
					return
				}
				received = len(input.Candles)
				if input.EngineCount != want || input.ModelCount != want {
					t.Errorf("metadata: %d/%d want %d", input.EngineCount, input.ModelCount, want)
				}
				lastSent, _ = time.Parse(time.RFC3339, input.Candles[received-1][0].(string))
				response := map[string]any{"symbol": "XAU/USD", "timeframe": frame.Label(), "market_regime": "RANGING", "trend": "NEUTRAL", "signal": "WAIT", "reasoning": []string{"سطح شکست هنوز تأیید نشده است"}, "support_levels": []float64{}, "resistance_levels": []float64{}, "invalidation": "", "setup_quality": "LOW", "last_candle_time_utc": lastSent.Format(time.RFC3339)}
				content, _ := json.Marshal(response)
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(content)}, "finish_reason": "stop"}}})
			}))
			defer server.Close()
			style := "day"
			if frame == "1min" {
				style = "scalp"
			}
			e := Engine{Market: marketdata.Client{Key: "test", BaseURL: server.URL + "/market", HTTP: server.Client()}, AI: ai.Client{Key: "test", BaseURL: server.URL, Model: "mock", HTTP: server.Client()}, MinRR: 1.5, Buffer: 30 * time.Second}
			d, err := e.Analyze(context.Background(), Request{Symbol: "XAU/USD", Timeframe: frame, Style: style, EngineCandles: 500, ModelCandles: 50})
			if err != nil {
				t.Fatal(err)
			}
			if received != want || len(d.Snapshot.Candles) != want {
				t.Fatalf("received %d snapshot %d want %d", received, len(d.Snapshot.Candles), want)
			}
			if !lastSent.Equal(d.Snapshot.LastCandleTimeUTC) || lastSent.Add(frame.Duration()+30*time.Second).After(d.Snapshot.AnalysisTimeUTC) {
				t.Fatal("unconfirmed candle sent")
			}
			if d.Final == "ANALYSIS_UNAVAILABLE" {
				t.Fatalf("unexpected failure: %v", d.Reasons)
			}
		})
	}
}
