package backtest

import (
	"context"
	"fmt"
	"time"
	"tradebot/internal/domain"
	"tradebot/internal/risk"
	"tradebot/internal/snapshot"
)

type Analyst interface {
	Analyze(context.Context, domain.Snapshot) (domain.Signal, error)
}
type Outcome struct {
	DecisionTime   time.Time `json:"decision_time_utc"`
	Signal         string    `json:"signal"`
	Status         string    `json:"status"`
	Activated      bool      `json:"activated"`
	R              float64   `json:"r"`
	HoldingCandles int       `json:"holding_candles"`
	Ambiguous      bool      `json:"ambiguous"`
	Error          string    `json:"error,omitempty"`
}
type Report struct {
	Samples        int       `json:"samples"`
	Evaluated      int       `json:"evaluated"`
	BUY            int       `json:"buy"`
	SELL           int       `json:"sell"`
	WAIT           int       `json:"wait"`
	Invalid        int       `json:"invalid"`
	Activated      int       `json:"activated"`
	Wins           int       `json:"wins"`
	Losses         int       `json:"losses"`
	AverageR       float64   `json:"average_r"`
	WinRate        float64   `json:"win_rate"`
	ActivationRate float64   `json:"activation_rate"`
	Outcomes       []Outcome `json:"outcomes"`
	Limitations    []string  `json:"limitations"`
}

func Evaluate(d domain.Decision, future []domain.Candle, horizon int, costR float64) Outcome {
	o := Outcome{DecisionTime: d.Snapshot.AnalysisTimeUTC, Signal: d.Final, Status: "WAIT"}
	if d.Final == "WAIT" {
		return o
	}
	if len(future) < horizon {
		horizon = len(future)
	}
	p := d.Proposed
	entry, sl, tp := *p.Entry, *p.StopLoss, *p.TP1
	risk := 0.0
	if d.Final == "BUY" {
		risk = entry - sl
	} else {
		risk = sl - entry
	}
	for i := 0; i < horizon; i++ {
		c := future[i]
		if !o.Activated {
			if c.Low > entry || c.High < entry {
				continue
			}
			o.Activated = true
		}
		hitSL, hitTP := false, false
		if d.Final == "BUY" {
			hitSL = c.Low <= sl
			hitTP = c.High >= tp
		} else {
			hitSL = c.High >= sl
			hitTP = c.Low <= tp
		}
		o.HoldingCandles = i + 1
		if hitSL {
			o.Status = "SL"
			o.R = -1 - costR
			o.Ambiguous = hitTP
			return o
		}
		if hitTP {
			o.Status = "TP1"
			o.R = d.RiskReward - costR
			return o
		}
	}
	if !o.Activated {
		o.Status = "NOT_TRIGGERED"
	} else {
		o.Status = "UNRESOLVED"
		last := future[horizon-1].Close
		if d.Final == "BUY" {
			o.R = (last-entry)/risk - costR
		} else {
			o.R = (entry-last)/risk - costR
		}
	}
	return o
}
func Run(ctx context.Context, all []domain.Candle, samples, horizon int, minRR, costR float64, a Analyst) (Report, error) {
	r := Report{Limitations: []string{"Candle-only fills are simulated; SL wins same-candle TP/SL ambiguity; bid/ask unavailable."}}
	if len(all) < 500+horizon+1 {
		return r, fmt.Errorf("history needs at least %d candles", 500+horizon+1)
	}
	max := len(all) - 500 - horizon
	if samples > max {
		samples = max
	}
	for n := 0; n < samples; n++ {
		idx := 500 + n*max/samples
		window := all[idx-500 : idx]
		at := window[499].Time.Add(5*time.Minute + 30*time.Second)
		s, e := snapshot.Build("XAU/USD", "M5", window, at, 5*time.Minute, 30*time.Second)
		if e != nil {
			return r, e
		}
		p, e := a.Analyze(ctx, s)
		if e != nil {
			r.Invalid++
			r.Outcomes = append(r.Outcomes, Outcome{DecisionTime: at, Signal: "WAIT", Status: "AI_ERROR", Error: e.Error()})
			continue
		}
		d := risk.Validate(s, p, minRR)
		o := Evaluate(d, all[idx:idx+horizon], horizon, costR)
		r.Outcomes = append(r.Outcomes, o)
		r.Evaluated++
		switch d.Final {
		case "BUY":
			r.BUY++
		case "SELL":
			r.SELL++
		default:
			r.WAIT++
			if p.Signal != "WAIT" {
				r.Invalid++
			}
		}
		if o.Activated {
			r.Activated++
			r.AverageR += o.R
			if o.Status == "TP1" {
				r.Wins++
			}
			if o.Status == "SL" {
				r.Losses++
			}
		}
	}
	r.Samples = samples
	if r.Activated > 0 {
		r.AverageR /= float64(r.Activated)
		r.WinRate = float64(r.Wins) / float64(r.Activated)
	}
	if r.Evaluated > 0 {
		r.ActivationRate = float64(r.Activated) / float64(r.Evaluated)
	}
	return r, nil
}
