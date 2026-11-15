package scenario

import (
	"fmt"
	"math"
	"time"
	"tradebot/internal/catalog"
	"tradebot/internal/condition"
	"tradebot/internal/domain"
)

// Generate builds deterministic conditional setups from snapshot structure data.
// Each candidate is validated by the risk engine before being returned.
// Immediate BUY/SELL/WAIT decisions are not touched; setups are PENDING only.
func Generate(s domain.Snapshot, minRR float64, styles ...string) []domain.ConditionalSetup {
	var out []domain.ConditionalSetup
	last := s.Last
	inst, _ := catalog.Find(s.Symbol)
	format := fmt.Sprintf("%%.%df", inst.Precision)
	atr := s.Quant.ATR14
	if atr <= 0 || math.IsNaN(atr) || math.IsInf(atr, 0) {
		return out
	}
	style := "day"
	if len(styles) > 0 {
		style = styles[0]
	}
	frame, _ := catalog.ParseFrame(frameInterval(s.Timeframe))
	exp := condition.Expiry(s.AnalysisTimeUTC, frame, style).Format(time.RFC3339)

	// tryBuild validates geometry and returns a ConditionalSetup if acceptable.
	tryBuild := func(dir string, trigPrice float64, trigKind string, entry, sl, tp float64, tp2 *float64, trigger, inv string) (domain.ConditionalSetup, bool) {
		entryP, slP, tpP := entry, sl, tp
		sig := domain.Signal{
			Symbol:            s.Symbol,
			Timeframe:         s.Timeframe,
			LastCandleTimeUTC: s.LastCandleTimeUTC.Format(time.RFC3339),
			Signal:            dir,
			SetupQuality:      "MEDIUM",
			Entry:             &entryP,
			StopLoss:          &slP,
			TP1:               &tpP,
			TP2:               tp2,
		}
		if riskValidate(s, sig, minRR).Final != dir {
			return domain.ConditionalSetup{}, false
		}
		return domain.ConditionalSetup{
			Direction:    dir,
			Trigger:      trigger,
			TriggerKind:  trigKind,
			TriggerPrice: trigPrice,
			Entry:        entry,
			StopLoss:     sl,
			TP1:          tp,
			TP2:          tp2,
			Invalidation: inv,
			Expiration:   exp,
		}, true
	}

	// --- Breakout of resistance → BUY ---
	for _, r := range s.Quant.Resistances {
		if r.Price <= last.Close {
			continue
		}
		entry := r.Price
		sl := r.Price - 0.5*atr
		riskDist := entry - sl
		if riskDist <= 0 {
			continue
		}
		tp := entry + 2*riskDist
		if tp <= entry {
			continue
		}
		if c, ok := tryBuild("BUY", r.Price, "BREAK_RETEST_BUY", entry, sl, tp, nil,
			"بستن کندل بالای مقاومت "+fmt.Sprintf(format, r.Price)+" و سپس بازگشت به سطح",
			fmt.Sprintf("بستن کندل زیر %.4f", r.Price)); ok {
			out = append(out, c)
		}
	}

	// --- Breakout of support → SELL ---
	for _, spt := range s.Quant.Supports {
		if spt.Price >= last.Close {
			continue
		}
		entry := spt.Price
		sl := spt.Price + 0.5*atr
		riskDist := sl - entry
		if riskDist <= 0 {
			continue
		}
		tp := entry - 2*riskDist
		if tp >= entry {
			continue
		}
		if c, ok := tryBuild("SELL", spt.Price, "BREAK_RETEST_SELL", entry, sl, tp, nil,
			"بستن کندل زیر حمایت "+fmt.Sprintf(format, spt.Price)+" و سپس بازگشت به سطح",
			fmt.Sprintf("بستن کندل بالای %.4f", spt.Price)); ok {
			out = append(out, c)
		}
	}

	// --- Trend continuation pullback → BUY (bullish alignment) ---
	if s.Quant.Alignment == "BULLISH" {
		entry := s.Quant.EMA20
		if entry > 0 && math.Abs(entry-last.Close) <= 2*atr {
			sl := s.Quant.EMA50
			if sl <= 0 || sl >= entry {
				sl = entry - atr
			}
			riskDist := entry - sl
			if riskDist <= 0 {
				sl = entry - 0.5*atr
				riskDist = entry - sl
			}
			tp := entry + 2*riskDist
			if c, ok := tryBuild("BUY", entry, "PULLBACK_BUY", entry, sl, tp, nil,
				fmt.Sprintf("بازگشت قیمت به EMA20 در روند صعودی (%.4f)", entry),
				fmt.Sprintf("بستن کندل زیر %.4f", sl)); ok {
				out = append(out, c)
			}
		}
	}

	// --- Trend continuation pullback → SELL (bearish alignment) ---
	if s.Quant.Alignment == "BEARISH" {
		entry := s.Quant.EMA20
		if entry > 0 && math.Abs(entry-last.Close) <= 2*atr {
			sl := s.Quant.EMA50
			if sl <= 0 || sl <= entry {
				sl = entry + atr
			}
			riskDist := sl - entry
			if riskDist <= 0 {
				sl = entry + 0.5*atr
				riskDist = sl - entry
			}
			tp := entry - 2*riskDist
			if c, ok := tryBuild("SELL", entry, "PULLBACK_SELL", entry, sl, tp, nil,
				fmt.Sprintf("بازگشت قیمت به EMA20 در روند نزولی (%.4f)", entry),
				fmt.Sprintf("بستن کندل بالای %.4f", sl)); ok {
				out = append(out, c)
			}
		}
	}

	// --- Market structure reversal: uptrend breakdown → SELL ---
	if s.Quant.Structure == "HIGHER_HIGH_HIGHER_LOW" && len(s.Quant.Swings) >= 2 {
		idx := len(s.Quant.Swings) - 1
		if s.Quant.Swings[idx].Kind == "LOW" && s.Quant.Swings[idx].Price < last.Close {
			entry := s.Quant.Swings[idx].Price
			sl := entry + atr
			riskDist := sl - entry
			tp := entry - 2*riskDist
			if c, ok := tryBuild("SELL", entry, "CLOSE_BELOW", entry, sl, tp, nil,
				fmt.Sprintf("شکن ساختار صعودی و بستن زیر %.4f", entry),
				fmt.Sprintf("بستن کندل بالای %.4f", sl)); ok {
				out = append(out, c)
			}
		}
	}

	// --- Market structure reversal: downtrend breakout → BUY ---
	if s.Quant.Structure == "LOWER_HIGH_LOWER_LOW" && len(s.Quant.Swings) >= 2 {
		idx := len(s.Quant.Swings) - 1
		if s.Quant.Swings[idx].Kind == "HIGH" && s.Quant.Swings[idx].Price > last.Close {
			entry := s.Quant.Swings[idx].Price
			sl := entry - atr
			riskDist := entry - sl
			tp := entry + 2*riskDist
			if c, ok := tryBuild("BUY", entry, "CLOSE_ABOVE", entry, sl, tp, nil,
				fmt.Sprintf("شکن ساختار نزولی و بستن بالای %.4f", entry),
				fmt.Sprintf("بستن کندل زیر %.4f", sl)); ok {
				out = append(out, c)
			}
		}
	}

	return out
}

func riskValidate(s domain.Snapshot, p domain.Signal, minRR float64) domain.Decision {
	d := domain.Decision{Snapshot: s, Proposed: p, Final: "WAIT"}
	if p.Signal != "BUY" && p.Signal != "SELL" {
		return d
	}
	if p.SetupQuality != "HIGH" && p.SetupQuality != "MEDIUM" {
		return d
	}
	if p.Entry == nil || p.StopLoss == nil || p.TP1 == nil {
		return d
	}
	for _, v := range []*float64{p.Entry, p.StopLoss, p.TP1, p.TP2} {
		if v != nil && (*v <= 0 || math.IsNaN(*v) || math.IsInf(*v, 0)) {
			return d
		}
	}
	entry, sl, tp := *p.Entry, *p.StopLoss, *p.TP1
	rr := 0.0
	if p.Signal == "BUY" {
		if !(sl < entry && entry < tp) {
			return d
		}
		rr = (tp - entry) / (entry - sl)
		if p.TP2 != nil && *p.TP2 < tp {
			return d
		}
	} else {
		if !(tp < entry && entry < sl) {
			return d
		}
		rr = (entry - tp) / (sl - entry)
		if p.TP2 != nil && *p.TP2 > tp {
			return d
		}
	}
	if rr < minRR {
		return d
	}
	atr := s.Quant.ATR14
	if atr <= 0 {
		return d
	}
	if math.Abs(entry-s.Last.Close) > 2*atr {
		return d
	}
	if math.Abs(entry-sl) < 0.2*atr || math.Abs(entry-sl) > 5*atr {
		return d
	}
	if p.Signal == "BUY" && sl >= s.Last.Low && entry <= s.Last.Close {
		return d
	}
	if p.Signal == "SELL" && sl <= s.Last.High && entry >= s.Last.Close {
		return d
	}
	d.Final = p.Signal
	d.RiskReward = rr
	return d
}
func frameInterval(label string) string {
	for _, f := range catalog.Frames {
		if f.Label() == label {
			return string(f)
		}
	}
	return "5min"
}
