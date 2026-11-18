package backtest

import (
	"context"
	"testing"
	"time"
	"tradebot/internal/catalog"
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

type checkingAnalyst struct {
	t     *testing.T
	want  int
	calls int
}

func (a *checkingAnalyst) Analyze(_ context.Context, s domain.Snapshot) (domain.Signal, error) {
	a.calls++
	if len(s.Candles) != a.want || !s.Candles[len(s.Candles)-1].Time.Equal(s.LastCandleTimeUTC) || !s.LastCandleTimeUTC.Before(s.AnalysisTimeUTC) {
		a.t.Fatalf("look-ahead or wrong history size: %d", len(s.Candles))
	}
	return domain.Signal{Symbol: s.Symbol, Timeframe: s.Timeframe, Signal: "WAIT", SetupQuality: "LOW", Reasoning: []string{"شکست تأیید نشده"}, LastCandleTimeUTC: s.LastCandleTimeUTC.Format(time.RFC3339)}, nil
}

func TestReplayUsesOnlyPrecedingCandles(t *testing.T) {
	for _, tc := range []struct {
		frame    string
		count    int
		duration time.Duration
	}{{"5min", 200, 5 * time.Minute}, {"1h", 250, time.Hour}} {
		t.Run(tc.frame, func(t *testing.T) {
			start := time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)
			all := make([]domain.Candle, tc.count+12+3)
			for i := range all {
				v := 100.0 + float64(i%5)*0.01
				all[i] = domain.Candle{Time: start.Add(time.Duration(i) * tc.duration), Open: v, High: v + 1, Low: v - 1, Close: v}
			}
			a := &checkingAnalyst{t: t, want: tc.count}
			r, err := RunFor(context.Background(), all, "XAU/USD", catalog.Timeframe(tc.frame), 3, 12, 1.5, 0, a)
			if err != nil || a.calls != 3 || r.WAIT != 3 || r.BUY != 0 || r.SELL != 0 {
				t.Fatalf("report=%+v calls=%d err=%v", r, a.calls, err)
			}
		})
	}
}
