package backtest

import (
	"context"
	"fmt"
	"time"
	"tradebot/internal/catalog"
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
	Conditional    int       `json:"conditional"`
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
	if d.Final == "WAIT" || d.Final == "CONDITIONAL" {
		if d.Final == "CONDITIONAL" {
			o.Status = "CONDITIONAL_NOT_EVALUATED"
		}
		return o
	}
	if d.Final != "BUY" && d.Final != "SELL" {
		o.Status = "ANALYSIS_UNAVAILABLE"
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
	return RunFor(ctx, all, "XAU/USD", "5min", samples, horizon, minRR, costR, a)
}

func RunFor(ctx context.Context, all []domain.Candle, symbol string, frame catalog.Timeframe, samples, horizon int, minRR, costR float64, a Analyst) (Report, error) {
	r := Report{Limitations: []string{"Candle-only fills are simulated; SL wins same-candle TP/SL ambiguity; bid/ask unavailable."}}
	count, err := catalog.RequiredCandles(frame)
	if err != nil {
		return r, err
	}
	if samples < 1 || horizon < 1 {
		return r, fmt.Errorf("samples and horizon must be positive")
	}
	if len(all) < count+horizon {
		return r, fmt.Errorf("history needs at least %d candles", count+horizon)
	}
	max := len(all) - count - horizon + 1
	if samples > max {
		samples = max
	}
	for n := 0; n < samples; n++ {
		idx := count
		if samples > 1 {
			idx += n * (max - 1) / (samples - 1)
		}
		window := all[idx-count : idx]
		at := window[count-1].Time.Add(frame.Duration() + 30*time.Second)
		s, e := snapshot.Build(symbol, frame.Label(), window, at, frame.Duration(), 30*time.Second)
		if e != nil {
			return r, e
		}
		if !s.Quality.Fresh {
			r.Invalid++
			r.Outcomes = append(r.Outcomes, Outcome{DecisionTime: at, Signal: "ANALYSIS_UNAVAILABLE", Status: "MARKET_DATA_ERROR", Error: s.Quality.StaleReason})
			continue
		}
		p, e := a.Analyze(ctx, s)
		if e != nil {
			r.Invalid++
			r.Outcomes = append(r.Outcomes, Outcome{DecisionTime: at, Signal: "ANALYSIS_UNAVAILABLE", Status: "AI_ERROR", Error: e.Error()})
			continue
		}
		d := risk.Validate(s, p, minRR)
		if d.Final == "WAIT" && p.Signal != "WAIT" {
			d.Final = "ANALYSIS_UNAVAILABLE"
			r.Invalid++
		}
		o := Evaluate(d, all[idx:idx+horizon], horizon, costR)
		r.Outcomes = append(r.Outcomes, o)
		if d.Final != "ANALYSIS_UNAVAILABLE" {
			r.Evaluated++
		}
		switch d.Final {
		case "BUY":
			r.BUY++
		case "SELL":
			r.SELL++
		case "ANALYSIS_UNAVAILABLE":
			// Invalid model output is not a market WAIT decision.
		default:
			r.WAIT++
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
