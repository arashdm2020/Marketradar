package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
	"tradebot/internal/domain"
)

func TestParse(t *testing.T) {
	valid := `{"symbol":"XAU/USD","timeframe":"M5","market_regime":"RANGE","trend":"NEUTRAL","signal":"WAIT","entry":null,"stop_loss":null,"take_profit_1":null,"take_profit_2":null,"reasoning":[],"support_levels":[],"resistance_levels":[],"invalidation":"","setup_quality":"LOW","last_candle_time_utc":"2026-01-01T00:00:00Z"}`
	if _, e := Parse([]byte(valid)); e != nil {
		t.Fatal(e)
	}
	if _, e := Parse([]byte(valid + ` {}`)); e == nil {
		t.Fatal("trailing JSON accepted")
	}
}
func TestModelWindowKeepsFullQuantContext(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if e := json.NewDecoder(r.Body).Decode(&req); e != nil {
			t.Error(e)
			return
		}
		if e := json.Unmarshal([]byte(req.Messages[1].Content), &got); e != nil {
			t.Error(e)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"symbol\":\"XAU/USD\",\"timeframe\":\"M5\",\"market_regime\":\"RANGE\",\"trend\":\"NEUTRAL\",\"signal\":\"WAIT\",\"entry\":null,\"stop_loss\":null,\"take_profit_1\":null,\"take_profit_2\":null,\"reasoning\":[],\"support_levels\":[],\"resistance_levels\":[],\"invalidation\":\"\",\"setup_quality\":\"LOW\",\"last_candle_time_utc\":\"2026-01-01T00:00:00Z\"}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	candles := make([]domain.Candle, 500)
	for i := range candles {
		candles[i] = domain.Candle{Time: time.Unix(int64(i*300), 0).UTC(), Open: 100, High: 101, Low: 99, Close: 100}
	}
	c := Client{Key: "test", BaseURL: server.URL, Model: "model", ModelCandles: 50}
	_, e := c.Analyze(context.Background(), domain.Snapshot{Symbol: "XAU/USD", Timeframe: "M5", Candles: candles, Quant: domain.Quant{EMA200: 100}})
	if e != nil {
		t.Fatal(e)
	}
	if got["engine_candle_count"].(float64) != 500 || got["model_candle_count"].(float64) != 50 || len(got["candles"].([]any)) != 50 {
		t.Fatal("wrong candle window")
	}
	quant := got["quant"].(map[string]any)
	if quant["EMA200"].(float64) != 100 {
		t.Fatal("500-candle quant lost")
	}
}
