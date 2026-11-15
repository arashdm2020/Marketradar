package risk

import (
	"math"
	"time"
	"tradebot/internal/domain"
)

func Validate(s domain.Snapshot, p domain.Signal, minRR float64) domain.Decision {
	d := domain.Decision{Snapshot: s, Proposed: p, Final: "WAIT", IndicativeOnly: true}
	bad := func(x string) { d.Reasons = append(d.Reasons, x) }
	if !s.Quality.Fresh {
		bad("market data stale or gapped")
	}
	if p.Symbol != s.Symbol || p.Timeframe != s.Timeframe || p.LastCandleTimeUTC != s.LastCandleTimeUTC.Format(time.RFC3339) {
		bad("snapshot identity mismatch")
	}
	if p.Signal == "WAIT" {
		bad("AI chose WAIT")
		return d
	}
	if p.Signal != "BUY" && p.Signal != "SELL" {
		bad("invalid signal")
		return d
	}
	if p.SetupQuality != "HIGH" && p.SetupQuality != "MEDIUM" {
		bad("setup quality insufficient")
	}
	if p.Entry == nil || p.StopLoss == nil || p.TP1 == nil {
		bad("missing trade prices")
		return d
	}
	for _, v := range []*float64{p.Entry, p.StopLoss, p.TP1, p.TP2} {
		if v != nil && (*v <= 0 || math.IsNaN(*v) || math.IsInf(*v, 0)) {
			bad("invalid price")
			return d
		}
	}
	entry, sl, tp := *p.Entry, *p.StopLoss, *p.TP1
	rr := 0.0
	if p.Signal == "BUY" {
		if !(sl < entry && entry < tp) {
			bad("invalid BUY geometry")
		} else {
			rr = (tp - entry) / (entry - sl)
		}
		if p.TP2 != nil && *p.TP2 < tp {
			bad("TP2 below TP1")
		}
	} else {
		if !(tp < entry && entry < sl) {
			bad("invalid SELL geometry")
		} else {
			rr = (entry - tp) / (sl - entry)
		}
		if p.TP2 != nil && *p.TP2 > tp {
			bad("TP2 above TP1")
		}
	}
	d.RiskReward = rr
	if rr < minRR {
		bad("risk/reward below minimum")
	}
	atr := s.Quant.ATR14
	if atr <= 0 {
		bad("invalid ATR")
	} else {
		if math.Abs(entry-s.Last.Close) > 2*atr {
			bad("entry too far from last close")
		}
		if math.Abs(entry-sl) < 0.2*atr || math.Abs(entry-sl) > 5*atr {
			bad("stop distance outside ATR bounds")
		}
		if p.Signal == "BUY" && sl >= s.Last.Low && entry <= s.Last.Close {
			bad("stop does not clear reference candle low")
		}
		if p.Signal == "SELL" && sl <= s.Last.High && entry >= s.Last.Close {
			bad("stop does not clear reference candle high")
		}
	}
	if len(d.Reasons) == 0 {
		d.Final = p.Signal
		d.Reasons = []string{"conditional paper signal; bid/ask and spread unavailable"}
	}
	return d
}
