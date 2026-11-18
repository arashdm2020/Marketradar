package risk

import (
	"testing"
	"time"
	"tradebot/internal/domain"
)

func p(v float64) *float64 { return &v }
func TestGeometryAndRR(t *testing.T) {
	tm := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	s := domain.Snapshot{Symbol: "XAU/USD", Timeframe: "M5", LastCandleTimeUTC: tm, Last: domain.Candle{Close: 100, Low: 99.5, High: 100.5}, Quant: domain.Quant{ATR14: 1}, Quality: domain.Quality{Fresh: true}}
	v := domain.Signal{Symbol: s.Symbol, Timeframe: s.Timeframe, LastCandleTimeUTC: tm.Format(time.RFC3339), Signal: "BUY", SetupQuality: "HIGH", EntryTrigger: "بستن بالای 99.9", TriggerKind: "CLOSE_ABOVE", TriggerPrice: p(99.9), Invalidation: "بستن زیر 99", Entry: p(100.5), StopLoss: p(99), TP1: p(103)}
	d := Validate(s, v, 1.5)
	if d.Final != "BUY" || d.RiskReward != 5.0/3.0 {
		t.Fatal(d.Reasons, d.RiskReward)
	}
	v.TP1 = p(101)
	if d = Validate(s, v, 1.5); d.Final != "WAIT" {
		t.Fatal(d)
	}
}

func TestTriggeredSellAndPendingBreakout(t *testing.T) {
	tm := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	s := domain.Snapshot{Symbol: "XAU/USD", Timeframe: "M5", LastCandleTimeUTC: tm, Last: domain.Candle{Close: 100, Low: 99.5, High: 100.5}, Quant: domain.Quant{ATR14: 1}, Quality: domain.Quality{Fresh: true}}
	v := domain.Signal{Symbol: s.Symbol, Timeframe: s.Timeframe, LastCandleTimeUTC: tm.Format(time.RFC3339), Signal: "SELL", SetupQuality: "MEDIUM", EntryTrigger: "بستن زیر 100.1", TriggerKind: "CLOSE_BELOW", TriggerPrice: p(100.1), Invalidation: "بستن بالای 101", Entry: p(99.5), StopLoss: p(101), TP1: p(96.5)}
	if got := Validate(s, v, 1.5); got.Final != "SELL" || got.RiskReward != 2 {
		t.Fatalf("sell: %+v", got)
	}
	v.TriggerPrice = p(99)
	if got := Validate(s, v, 1.5); got.Final != "WAIT" {
		t.Fatalf("pending trigger accepted: %+v", got)
	}
	if got := ValidateConditional(s, v, 1.5); got.Final != "SELL" {
		t.Fatalf("valid pending setup rejected: %+v", got)
	}
	v.Invalidation = ""
	if got := ValidateConditional(s, v, 1.5); got.Final != "WAIT" {
		t.Fatalf("missing invalidation accepted: %+v", got)
	}
}
