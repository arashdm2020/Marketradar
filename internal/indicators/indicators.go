package indicators

import (
	"math"
	"tradebot/internal/domain"
)

func EMA(c []domain.Candle, n int) float64 {
	if len(c) < n || n < 1 {
		return math.NaN()
	}
	v := 0.0
	for _, x := range c[:n] {
		v += x.Close
	}
	v /= float64(n)
	k := 2 / float64(n+1)
	for _, x := range c[n:] {
		v += (x.Close - v) * k
	}
	return v
}
func RSI(c []domain.Candle, n int) float64 {
	if len(c) <= n {
		return math.NaN()
	}
	gain, loss := 0.0, 0.0
	for i := 1; i <= n; i++ {
		d := c[i].Close - c[i-1].Close
		if d > 0 {
			gain += d
		} else {
			loss -= d
		}
	}
	gain /= float64(n)
	loss /= float64(n)
	for i := n + 1; i < len(c); i++ {
		d := c[i].Close - c[i-1].Close
		g, l := 0.0, 0.0
		if d > 0 {
			g = d
		} else {
			l = -d
		}
		gain = (gain*float64(n-1) + g) / float64(n)
		loss = (loss*float64(n-1) + l) / float64(n)
	}
	if loss == 0 {
		if gain == 0 {
			return 50
		}
		return 100
	}
	return 100 - 100/(1+gain/loss)
}
func ATR(c []domain.Candle, n int) float64 {
	if len(c) <= n {
		return math.NaN()
	}
	tr := func(i int) float64 {
		x := c[i]
		p := c[i-1].Close
		return math.Max(x.High-x.Low, math.Max(math.Abs(x.High-p), math.Abs(x.Low-p)))
	}
	v := 0.0
	for i := 1; i <= n; i++ {
		v += tr(i)
	}
	v /= float64(n)
	for i := n + 1; i < len(c); i++ {
		v = (v*float64(n-1) + tr(i)) / float64(n)
	}
	return v
}
