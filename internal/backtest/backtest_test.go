package backtest

import (
	"testing"
	"tradebot/internal/domain"
)

func ptr(v float64) *float64 { return &v }
func TestSLFirst(t *testing.T) {
	d := domain.Decision{Final: "BUY", RiskReward: 2, Proposed: domain.Signal{Entry: ptr(100), StopLoss: ptr(99), TP1: ptr(102)}}
	o := Evaluate(d, []domain.Candle{{Open: 100, High: 103, Low: 98, Close: 101}}, 1, 0)
	if o.Status != "SL" || !o.Ambiguous || o.R != -1 {
		t.Fatal(o)
	}
}
