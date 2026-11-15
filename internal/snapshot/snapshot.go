package snapshot

import (
	"fmt"
	"time"
	"tradebot/internal/domain"
	"tradebot/internal/indicators"
	"tradebot/internal/marketdata"
	"tradebot/internal/structure"
)

func Build(symbol, timeframe string, c []domain.Candle, at time.Time, interval, buffer time.Duration) (domain.Snapshot, error) {
	if len(c) < 250 || len(c) > 1000 {
		return domain.Snapshot{}, fmt.Errorf("snapshot requires 250..1000 candles, got %d", len(c))
	}
	v, e := marketdata.Normalize(c)
	if e != nil {
		return domain.Snapshot{}, e
	}
	if !v[len(v)-1].Time.Add(interval).Add(buffer).Before(at) && !v[len(v)-1].Time.Add(interval).Add(buffer).Equal(at) {
		return domain.Snapshot{}, fmt.Errorf("last candle incomplete")
	}
	q := marketdata.Check(v, at, interval, buffer)
	last := v[len(v)-1]
	state, sw, su, re := structure.Analyze(v)
	z := domain.Quant{EMA20: indicators.EMA(v, 20), EMA50: indicators.EMA(v, 50), EMA200: indicators.EMA(v, 200), RSI14: indicators.RSI(v, 14), ATR14: indicators.ATR(v, 14), RecentReturn: (last.Close/v[len(v)-6].Close - 1) * 100, Structure: state, Swings: sw, Supports: su, Resistances: re}
	z.ATRPercent = z.ATR14 / last.Close * 100
	z.Alignment = "MIXED"
	if z.EMA20 > z.EMA50 && z.EMA50 > z.EMA200 {
		z.Alignment = "BULLISH"
	}
	if z.EMA20 < z.EMA50 && z.EMA50 < z.EMA200 {
		z.Alignment = "BEARISH"
	}
	z.Momentum = "NEUTRAL"
	if z.RSI14 > 55 && z.RecentReturn > 0 {
		z.Momentum = "BULLISH"
	}
	if z.RSI14 < 45 && z.RecentReturn < 0 {
		z.Momentum = "BEARISH"
	}
	return domain.Snapshot{Symbol: symbol, Timeframe: timeframe, AnalysisTimeUTC: at.UTC(), LastCandleTimeUTC: last.Time, Candles: v, Last: last, Quant: z, Quality: q}, nil
}
