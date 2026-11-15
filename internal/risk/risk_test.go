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
	v := domain.Signal{Symbol: s.Symbol, Timeframe: s.Timeframe, LastCandleTimeUTC: tm.Format(time.RFC3339), Signal: "BUY", SetupQuality: "HIGH", Entry: p(100.5), StopLoss: p(99), TP1: p(103)}
	d := Validate(s, v, 1.5)
	if d.Final != "BUY" || d.RiskReward != 5.0/3.0 {
		t.Fatal(d.Reasons, d.RiskReward)
	}
	v.TP1 = p(101)
	if d = Validate(s, v, 1.5); d.Final != "WAIT" {
		t.Fatal(d)
	}
}
