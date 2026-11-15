package indicators

import (
	"math"
	"testing"
	"time"
	"tradebot/internal/domain"
)

func TestIndicators(t *testing.T) {
	c := make([]domain.Candle, 210)
	for i := range c {
		v := 100 + float64(i)
		c[i] = domain.Candle{Time: time.Unix(int64(i*300), 0).UTC(), Open: v, High: v + 1, Low: v - 1, Close: v}
	}
	if math.Abs(EMA(c, 20)-299.5) > 0.001 {
		t.Fatal(EMA(c, 20))
	}
	if RSI(c, 14) != 100 {
		t.Fatal(RSI(c, 14))
	}
	if math.Abs(ATR(c, 14)-2) > 0.001 {
		t.Fatal(ATR(c, 14))
	}
}
