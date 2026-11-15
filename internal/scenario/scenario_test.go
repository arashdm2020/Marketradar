package scenario

import (
	"testing"
	"time"
	"tradebot/internal/domain"
)

func snapshot() domain.Snapshot {
	tm := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	return domain.Snapshot{
		Symbol:            "XAU/USD",
		Timeframe:         "M5",
		LastCandleTimeUTC: tm,
		Last:              domain.Candle{Time: tm, Open: 100, High: 102, Low: 99, Close: 100},
		Quant: domain.Quant{
			ATR14:     1.0,
			Alignment: "BULLISH",
			EMA20:     99.5,
			EMA50:     98.0,
			Structure: "HIGHER_HIGH_HIGHER_LOW",
			Supports: []domain.Level{
				{Price: 98.0, Kind: "LOW"},
				{Price: 96.0, Kind: "LOW"},
			},
			Resistances: []domain.Level{
				{Price: 102.0, Kind: "HIGH"},
				{Price: 104.0, Kind: "HIGH"},
			},
			Swings: []domain.Level{
				{Price: 96.0, Time: tm, Kind: "LOW"},
				{Price: 102.0, Time: tm, Kind: "HIGH"},
			},
		},
		Quality: domain.Quality{Fresh: true},
	}
}

func TestScenarioGeneration(t *testing.T) {
	s := snapshot()
	setups := Generate(s, 1.5)
	if len(setups) == 0 {
		t.Fatal("expected at least one scenario, got 0")
	}
	for _, s2 := range setups {
		if s2.Direction != "BUY" && s2.Direction != "SELL" {
			t.Fatalf("invalid direction: %s", s2.Direction)
		}
		if s2.TriggerKind != "CLOSE_ABOVE" && s2.TriggerKind != "CLOSE_BELOW" && s2.TriggerKind != "BREAK_RETEST_BUY" && s2.TriggerKind != "BREAK_RETEST_SELL" && s2.TriggerKind != "PULLBACK_BUY" && s2.TriggerKind != "PULLBACK_SELL" {
			t.Fatalf("invalid trigger kind: %s", s2.TriggerKind)
		}
		if s2.TriggerPrice <= 0 {
			t.Fatal("trigger price must be positive")
		}
		if s2.Entry <= 0 || s2.StopLoss <= 0 || s2.TP1 <= 0 {
			t.Fatal("entry, sl, tp must be positive")
		}
		if s2.Invalidation == "" || s2.Expiration == "" || s2.Trigger == "" {
			t.Fatal("trigger, invalidation, expiration must be non-empty")
		}
	}
}

func TestScenarioGeometry(t *testing.T) {
	s := snapshot()
	setups := Generate(s, 1.5)
	for _, s2 := range setups {
		if s2.Direction == "BUY" {
			if !(s2.StopLoss < s2.Entry && s2.Entry < s2.TP1) {
				t.Fatalf("bad BUY geometry: entry=%v sl=%v tp=%v", s2.Entry, s2.StopLoss, s2.TP1)
			}
			rr := (s2.TP1 - s2.Entry) / (s2.Entry - s2.StopLoss)
			if rr < 1.5 {
				t.Fatalf("RR below minimum: %.2f", rr)
			}
		} else {
			if !(s2.TP1 < s2.Entry && s2.Entry < s2.StopLoss) {
				t.Fatalf("bad SELL geometry: entry=%v sl=%v tp=%v", s2.Entry, s2.StopLoss, s2.TP1)
			}
		}
	}
}

func TestScenarioEmptyOnNoATR(t *testing.T) {
	s := snapshot()
	s.Quant.ATR14 = 0
	setups := Generate(s, 1.5)
	if len(setups) != 0 {
		t.Fatal("expected no scenarios with zero ATR")
	}
}
