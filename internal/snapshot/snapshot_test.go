package snapshot

import (
	"testing"
	"time"
	"tradebot/internal/domain"
)

func TestManualCandleCountBuild(t *testing.T) {
	at := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	for _, count := range []int{250, 500, 1000} {
		candles := make([]domain.Candle, count)
		for i := range candles {
			price := 3900 + float64(i)/10
			candles[i] = domain.Candle{Time: at.Add(-time.Duration(count-i+1) * 5 * time.Minute), Open: price, High: price + 1, Low: price - 1, Close: price + .2}
		}
		s, e := Build("XAU/USD", "M5", candles, at, 5*time.Minute, 30*time.Second)
		if e != nil || len(s.Candles) != count {
			t.Fatalf("count %d: %v", count, e)
		}
	}
}
