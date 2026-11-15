package condition

import (
	"time"
	"tradebot/internal/catalog"
	"tradebot/internal/domain"
)

// State is persisted for each alert. A breakout retest needs two separate closed candles.
func Step(kind string, level, entry float64, stage int, c domain.Candle) (int, bool) {
	switch kind {
	case "CLOSE_ABOVE":
		return stage, c.Close > level
	case "CLOSE_BELOW":
		return stage, c.Close < level
	case "TOUCH":
		return stage, c.Low <= level && c.High >= level
	case "BREAK_RETEST_BUY":
		if stage == 0 {
			if c.Close > level {
				return 1, false
			}
			return 0, false
		}
		return 1, c.Low <= entry && c.Close > level
	case "BREAK_RETEST_SELL":
		if stage == 0 {
			if c.Close < level {
				return 1, false
			}
			return 0, false
		}
		return 1, c.High >= entry && c.Close < level
	case "PULLBACK_BUY":
		return stage, c.Low <= entry && c.Close > entry
	case "PULLBACK_SELL":
		return stage, c.High >= entry && c.Close < entry
	}
	return stage, false
}
func Expiry(at time.Time, frame catalog.Timeframe, style string) time.Time {
	bars := 12
	switch style {
	case "scalp":
		bars = 4
	case "day":
		bars = 8
	case "swing":
		bars = 16
	case "position":
		bars = 24
	}
	t := at
	for n := 0; n < bars; {
		t = t.Add(frame.Duration())
		if frame == "1week" || marketOpen(t) {
			n++
		}
	}
	return t.UTC()
}
func marketOpen(t time.Time) bool {
	u := t.UTC()
	w := u.Weekday()
	return w != time.Saturday && !(w == time.Sunday && u.Hour() < 22) && !(w == time.Friday && u.Hour() >= 22)
}
