package catalog

import (
	"fmt"
	"time"
)

type Timeframe string

var Frames = []Timeframe{"1min", "5min", "15min", "30min", "1h", "4h", "1day", "1week"}

func ParseFrame(s string) (Timeframe, error) {
	for _, f := range Frames {
		if string(f) == s {
			return f, nil
		}
	}
	return "", fmt.Errorf("unsupported timeframe %q", s)
}
func (f Timeframe) Label() string {
	switch f {
	case "1min":
		return "M1"
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
