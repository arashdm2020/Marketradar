package engine

import (
	"context"
	"fmt"
	"log/slog"
	"time"
	"tradebot/internal/ai"
	"tradebot/internal/catalog"
	"tradebot/internal/condition"
	"tradebot/internal/domain"
	"tradebot/internal/marketdata"
	"tradebot/internal/risk"
	"tradebot/internal/scenario"
	"tradebot/internal/snapshot"
)

type Request struct {
	Symbol                        string
	Timeframe                     catalog.Timeframe
	Style                         string
	MinProfit, MaxProfit, MaxRisk float64
	Language                      string
	ModelCandles                  int
	EngineCandles                 int
	HorizonCandles                int
	ResponseTokenBudget           int
}
type Engine struct {
	Market    marketdata.Client
	AI        ai.Client
	ResolveAI func() (ai.Client, error)
	MinRR     float64
	Buffer    time.Duration
}

func (e Engine) Analyze(ctx context.Context, r Request) (domain.Decision, error) {
	inst, ok := catalog.Find(r.Symbol)
	if !ok || !inst.Enabled {
		return domain.Decision{}, fmt.Errorf("instrument unavailable")
	}
	if _, err := catalog.ParseFrame(string(r.Timeframe)); err != nil {
		return domain.Decision{}, err
	}
	if err := catalog.ValidateCombination(r.Timeframe, r.Style); err != nil {
		return domain.Decision{}, err
	}
	count, err := catalog.RequiredCandles(r.Timeframe)
	if err != nil {
		return domain.Decision{}, err
	}
	if r.EngineCandles != 0 && r.EngineCandles != count || r.ModelCandles != 0 && r.ModelCandles != count {
		slog.Info("candle_policy_override", "symbol", r.Symbol, "timeframe", r.Timeframe, "configured_engine_candles", r.EngineCandles, "configured_model_candles", r.ModelCandles, "effective_engine_candles", count, "effective_model_candles", count)
	}
	now := time.Now().UTC()
	// Historical admin window settings remain versioned, but the current
	// timeframe policy fixes both engine and model history to count bars.
	candles, err := e.Market.FetchClosed(ctx, inst.Provider, string(r.Timeframe), count, now, e.Buffer)
	if err != nil {
		return domain.Decision{}, err
	}
	s, err := snapshot.Build(r.Symbol, r.Timeframe.Label(), candles, now, r.Timeframe.Duration(), e.Buffer)
	if err != nil {
		return domain.Decision{}, err
	}
	if !s.Quality.Fresh {
		return domain.Decision{Snapshot: s, Final: "ANALYSIS_UNAVAILABLE", Reasons: []string{"market data stale or gapped"}, IndicativeOnly: true}, nil
	}
	model := e.AI
	if e.ResolveAI != nil {
		if current, resolveErr := e.ResolveAI(); resolveErr == nil {
			model = current
		} else {
			return domain.Decision{Snapshot: s, Final: "ANALYSIS_UNAVAILABLE", Reasons: []string{"AI configuration unavailable"}, IndicativeOnly: true}, nil
		}
	}
	model.ModelCandles = count
	if r.ResponseTokenBudget > 0 {
		model.MaxTokens = r.ResponseTokenBudget
	}
	a, err := model.AnalyzeWithContext(ctx, s, map[string]any{"style": r.Style, "min_profit_target_pct_of_entry_notional": r.MinProfit, "max_profit_target_pct_of_entry_notional": r.MaxProfit, "max_account_risk_pct": r.MaxRisk, "language": r.Language, "prediction_horizon_candles": r.HorizonCandles})
	if err != nil {
		return domain.Decision{Snapshot: s, Final: "ANALYSIS_UNAVAILABLE", FailureClass: string(ai.Classify(err)), Reasons: []string{err.Error()}, IndicativeOnly: true}, nil
	}
	d := risk.Validate(s, a.Signal, e.MinRR)
	if d.Final == "WAIT" {
		for _, reason := range d.Reasons {
			if reason != "AI chose WAIT" {
				d.Final = "ANALYSIS_UNAVAILABLE"
				break
			}
		}
	}
	for _, setup := range a.Signal.ConditionalSetups {
		if setup.Trigger == "" || setup.Invalidation == "" || setup.Expiration == "" || setup.TriggerPrice <= 0 || setup.TriggerKind != "CLOSE_ABOVE" && setup.TriggerKind != "CLOSE_BELOW" {
			continue
		}
		grounded := false
		for _, l := range append(append([]domain.Level{}, s.Quant.Supports...), s.Quant.Resistances...) {
			if setup.TriggerPrice == l.Price {
				grounded = true
				break
			}
		}
		if !grounded {
			continue
		}
		entry, sl, tp := setup.Entry, setup.StopLoss, setup.TP1
		candidate := a.Signal
		candidate.Signal = setup.Direction
		candidate.Entry = &entry
		candidate.StopLoss = &sl
		candidate.TP1 = &tp
		candidate.TP2 = setup.TP2
		candidate.SetupQuality = "MEDIUM"
		candidate.EntryTrigger = setup.Trigger
		candidate.TriggerKind = setup.TriggerKind
		candidate.TriggerPrice = &setup.TriggerPrice
		candidate.Invalidation = setup.Invalidation
		candidate.ConditionalSetups = nil
		if risk.ValidateConditional(s, candidate, e.MinRR).Final == setup.Direction {
			d.ValidatedConditional = append(d.ValidatedConditional, setup)
		}
	}
	for _, s2 := range scenario.Generate(s, e.MinRR, r.Style) {
		d.ValidatedConditional = append(d.ValidatedConditional, s2)
	}
	for i := range d.ValidatedConditional {
		d.ValidatedConditional[i].Expiration = condition.Expiry(s.AnalysisTimeUTC, r.Timeframe, r.Style).Format(time.RFC3339)
	}
	if d.Final == "WAIT" && len(d.ValidatedConditional) > 0 {
		d.Final = "CONDITIONAL"
	}
	d.Model = model.Model
	d.PromptVersion = ai.PromptVersion
	d.LatencyMS = a.LatencyMS
	d.PromptTokens = a.PromptTokens
	d.CompletionTokens = a.CompletionTokens
	return d, nil
}
