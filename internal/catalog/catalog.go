package catalog

import (
	"fmt"
	"time"
)

type Timeframe string

type Horizon string

const (
	HorizonScalp    Horizon = "scalp"
	HorizonIntraday Horizon = "day"
	HorizonSwing    Horizon = "swing"
	HorizonPosition Horizon = "position"
)

// Append new frames so historic index-based Telegram callbacks keep meaning.
var Frames = []Timeframe{"1min", "5min", "15min", "30min", "1h", "4h", "1day", "1week", "3min"}

// HorizonMatrix is product policy, not a universal trading rule. Keep every
// UI and backend admission check based on this single authoritative matrix.
var HorizonMatrix = map[Timeframe][]Horizon{
	"1min":  {HorizonScalp},
	"3min":  {HorizonScalp},
	"5min":  {HorizonScalp, HorizonIntraday},
	"15min": {HorizonScalp, HorizonIntraday},
	"30min": {HorizonIntraday},
	"1h":    {HorizonIntraday, HorizonSwing},
	"4h":    {HorizonIntraday, HorizonSwing, HorizonPosition},
	"1day":  {HorizonSwing, HorizonPosition},
	"1week": {HorizonPosition},
}

func ParseHorizon(s string) (Horizon, error) {
	h := Horizon(s)
	for _, candidate := range []Horizon{HorizonScalp, HorizonIntraday, HorizonSwing, HorizonPosition} {
		if h == candidate {
			return h, nil
		}
	}
	return "", fmt.Errorf("unsupported trading horizon %q", s)
}

func (h Horizon) LabelFA() string {
	return map[Horizon]string{HorizonScalp: "اسکالپ", HorizonIntraday: "روزانه", HorizonSwing: "میان‌مدت", HorizonPosition: "بلندمدت"}[h]
}

func AllowedHorizons(frame Timeframe) []Horizon {
	return append([]Horizon(nil), HorizonMatrix[frame]...)
}

func Compatible(frame Timeframe, horizon Horizon) bool {
	for _, allowed := range HorizonMatrix[frame] {
		if allowed == horizon {
			return true
		}
	}
	return false
}

func ValidateCombination(frame Timeframe, horizon string) error {
	if _, err := ParseFrame(string(frame)); err != nil {
		return err
	}
	h, err := ParseHorizon(horizon)
	if err != nil {
		return err
	}
	if !Compatible(frame, h) {
		return fmt.Errorf("trading horizon %q is incompatible with timeframe %q", horizon, frame)
	}
	return nil
}

// PredictionCandles converts the selected holding horizon into an actual
// forward-candle budget while retaining the administrator's intraday base.
func PredictionCandles(h Horizon, intradayBase int) int {
	if intradayBase < 1 {
		intradayBase = 12
	}
	v := intradayBase
	switch h {
	case HorizonScalp:
		v = (intradayBase + 1) / 2
	case HorizonSwing:
		v = intradayBase * 2
	case HorizonPosition:
		v = intradayBase * 4
	}
	if v < 1 {
		return 1
	}
	if v > 96 {
		return 96
	}
	return v
}

func ParseFrame(s string) (Timeframe, error) {
	for _, f := range Frames {
		if string(f) == s {
			return f, nil
		}
	}
	return "", fmt.Errorf("unsupported timeframe %q", s)
}

// RequiredCandles is the authoritative AI and replay history policy.
func RequiredCandles(frame Timeframe) (int, error) {
	if _, err := ParseFrame(string(frame)); err != nil {
		return 0, err
	}
	if frame.Duration() <= 15*time.Minute {
		return 200, nil
	}
	return 250, nil
}
func (f Timeframe) Label() string {
	switch f {
	case "1min":
		return "M1"
	case "3min":
		return "M3"
	case "5min":
		return "M5"
	case "15min":
		return "M15"
	case "30min":
		return "M30"
	case "1h":
		return "H1"
	case "4h":
		return "H4"
	case "1day":
		return "D1"
	case "1week":
		return "W1"
	}
	return string(f)
}
func (f Timeframe) Duration() time.Duration {
	switch f {
	case "1min":
		return time.Minute
	case "3min":
		return 3 * time.Minute
	case "5min":
		return 5 * time.Minute
	case "15min":
		return 15 * time.Minute
	case "30min":
		return 30 * time.Minute
	case "1h":
		return time.Hour
	case "4h":
		return 4 * time.Hour
	case "1day":
		return 24 * time.Hour
	case "1week":
		return 7 * 24 * time.Hour
	}
	return 0
}

type Instrument struct {
	Symbol, Name, Asset, Provider string
	Precision                     int
	Enabled                       bool
}

var Instruments = []Instrument{{"EUR/USD", "یورو / دلار", "forex", "EUR/USD", 5, true}, {"GBP/USD", "پوند / دلار", "forex", "GBP/USD", 5, true}, {"USD/JPY", "دلار / ین", "forex", "USD/JPY", 3, true}, {"USD/CHF", "دلار / فرانک", "forex", "USD/CHF", 5, true}, {"AUD/USD", "دلار استرالیا / دلار", "forex", "AUD/USD", 5, true}, {"XAU/USD", "طلا / دلار", "gold", "XAU/USD", 2, true}, {"WTI/USD", "نفت خام WTI نقدی", "oil", "WTI/USD", 2, true}}

func Find(s string) (Instrument, bool) {
	for _, v := range Instruments {
		if v.Symbol == s {
			return v, true
		}
	}
	return Instrument{}, false
}
