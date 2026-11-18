package domain

import (
	"fmt"
	"math"
	"time"
)

type Candle struct {
	Time  time.Time `json:"time_utc"`
	Open  float64   `json:"open"`
	High  float64   `json:"high"`
	Low   float64   `json:"low"`
	Close float64   `json:"close"`
}

func (c Candle) Validate() error {
	if c.Time.IsZero() || !c.Time.Equal(c.Time.UTC()) {
		return fmt.Errorf("invalid UTC timestamp")
	}
	for _, v := range []float64{c.Open, c.High, c.Low, c.Close} {
		if v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("invalid OHLC")
		}
	}
	if c.High < math.Max(c.Open, c.Close) || c.Low > math.Min(c.Open, c.Close) || c.High < c.Low {
		return fmt.Errorf("inconsistent OHLC")
	}
	return nil
}

type Level struct {
	Price float64   `json:"price"`
	Time  time.Time `json:"confirmed_at_utc"`
	Kind  string    `json:"kind"`
}
type Quality struct {
	Fresh       bool     `json:"fresh"`
	Gaps        []string `json:"gaps,omitempty"`
	StaleReason string   `json:"stale_reason,omitempty"`
}
type Quant struct {
	EMA20, EMA50, EMA200, RSI14, ATR14, ATRPercent, RecentReturn float64
	Alignment, Momentum, Structure                               string
	Swings, Supports, Resistances                                []Level
}
type Snapshot struct {
	Symbol, Timeframe                  string
	AnalysisTimeUTC, LastCandleTimeUTC time.Time
	Candles                            []Candle
	Last                               Candle
	Quant                              Quant
	Quality                            Quality
}
type Signal struct {
	Symbol            string             `json:"symbol"`
	Timeframe         string             `json:"timeframe"`
	MarketRegime      string             `json:"market_regime"`
	Trend             string             `json:"trend"`
	Signal            string             `json:"signal"`
	EntryTrigger      string             `json:"entry_trigger,omitempty"`
	TriggerKind       string             `json:"trigger_kind,omitempty"`
	TriggerPrice      *float64           `json:"trigger_price,omitempty"`
	Entry             *float64           `json:"entry"`
	StopLoss          *float64           `json:"stop_loss"`
	TP1               *float64           `json:"take_profit_1"`
	TP2               *float64           `json:"take_profit_2"`
	Reasoning         []string           `json:"reasoning"`
	SupportLevels     []float64          `json:"support_levels"`
	ResistanceLevels  []float64          `json:"resistance_levels"`
	Invalidation      string             `json:"invalidation"`
	SetupQuality      string             `json:"setup_quality"`
	LastCandleTimeUTC string             `json:"last_candle_time_utc"`
	ConditionalSetups []ConditionalSetup `json:"conditional_setups,omitempty"`
}
type ConditionalSetup struct {
	Direction    string   `json:"direction"`
	Trigger      string   `json:"trigger"`
	TriggerKind  string   `json:"trigger_kind"`
	EntryKind    string   `json:"entry_kind,omitempty"`
	TriggerPrice float64  `json:"trigger_price"`
	Entry        float64  `json:"entry"`
	StopLoss     float64  `json:"stop_loss"`
	TP1          float64  `json:"take_profit_1"`
	TP2          *float64 `json:"take_profit_2"`
	Invalidation string   `json:"invalidation"`
	Expiration   string   `json:"expiration"`
}
type Decision struct {
	Snapshot             Snapshot           `json:"snapshot"`
	Proposed             Signal             `json:"proposed"`
	Final                string             `json:"final"`
	FailureClass         string             `json:"failure_class,omitempty"`
	Reasons              []string           `json:"reasons,omitempty"`
	RiskReward           float64            `json:"risk_reward,omitempty"`
	IndicativeOnly       bool               `json:"indicative_only"`
	Model                string             `json:"model"`
	PromptVersion        string             `json:"prompt_version"`
	LatencyMS            int64              `json:"latency_ms"`
	PromptTokens         int                `json:"prompt_tokens,omitempty"`
	CompletionTokens     int                `json:"completion_tokens,omitempty"`
	ValidatedConditional []ConditionalSetup `json:"validated_conditional,omitempty"`
}
