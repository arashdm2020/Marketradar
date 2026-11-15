package structure

import "tradebot/internal/domain"

func Analyze(c []domain.Candle) (string, []domain.Level, []domain.Level, []domain.Level) {
	var swings, supports, resistances []domain.Level
	for i := 2; i+2 < len(c); i++ {
		hi, lo := true, true
		for j := i - 2; j <= i+2; j++ {
			if j == i {
				continue
			}
			if c[j].High >= c[i].High {
				hi = false
			}
			if c[j].Low <= c[i].Low {
				lo = false
			}
		}
		confirmed := c[i+2].Time
		if hi {
			l := domain.Level{Price: c[i].High, Time: confirmed, Kind: "HIGH"}
			swings = append(swings, l)
			resistances = append(resistances, l)
		}
		if lo {
			l := domain.Level{Price: c[i].Low, Time: confirmed, Kind: "LOW"}
			swings = append(swings, l)
			supports = append(supports, l)
		}
	}
	if len(supports) > 5 {
		supports = supports[len(supports)-5:]
	}
	if len(resistances) > 5 {
		resistances = resistances[len(resistances)-5:]
	}
	state := "RANGE"
	if len(supports) > 1 && len(resistances) > 1 {
		a, b := supports[len(supports)-2], supports[len(supports)-1]
		x, y := resistances[len(resistances)-2], resistances[len(resistances)-1]
		if b.Price > a.Price && y.Price > x.Price {
			state = "HIGHER_HIGH_HIGHER_LOW"
		}
		if b.Price < a.Price && y.Price < x.Price {
			state = "LOWER_HIGH_LOWER_LOW"
		}
	}
	return state, swings, supports, resistances
}
