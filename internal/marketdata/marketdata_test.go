package marketdata

import (
	"testing"
	"time"
	"tradebot/internal/domain"
)

func candle(t time.Time) domain.Candle {
	return domain.Candle{Time: t.UTC(), Open: 100, High: 101, Low: 99, Close: 100}
}
func TestNormalizeAndClosed(t *testing.T) {
	a := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	out, e := Normalize([]domain.Candle{candle(a.Add(5 * time.Minute)), candle(a)})
	if e != nil || !out[0].Time.Equal(a) {
		t.Fatal(e)
	}
	if len(Closed(out, a.Add(10*time.Minute), 5*time.Minute, 30*time.Second)) != 1 {
		t.Fatal("buffer ignored")
	}
	if _, e = Normalize([]domain.Candle{candle(a), candle(a)}); e == nil {
		t.Fatal("duplicate accepted")
	}
	bad := candle(a)
	bad.Low = 102
	if _, e = Normalize([]domain.Candle{bad}); e == nil {
		t.Fatal("invalid OHLC accepted")
	}
}
func TestSessionGap(t *testing.T) {
	fri := time.Date(2026, 11, 6, 21, 55, 0, 0, time.UTC)
	sun := time.Date(2026, 11, 8, 22, 0, 0, 0, time.UTC)
	q := Check([]domain.Candle{candle(fri), candle(sun)}, sun.Add(6*time.Minute), 5*time.Minute, 30*time.Second)
	if len(q.Gaps) != 0 {
		t.Fatal(q.Gaps)
	}
	mon := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	q = Check([]domain.Candle{candle(mon), candle(mon.Add(10 * time.Minute))}, mon.Add(16*time.Minute), 5*time.Minute, 30*time.Second)
	if len(q.Gaps) != 1 || q.Fresh {
		t.Fatal(q)
	}
}
