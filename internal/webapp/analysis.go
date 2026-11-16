package webapp

import (
	"context"
	"encoding/json"
	"errors"
	"time"
	"tradebot/internal/ai"
	"tradebot/internal/catalog"
	"tradebot/internal/domain"
	"tradebot/internal/engine"
	"tradebot/internal/profile"
)

type Analyzer interface {
	Analyze(context.Context, engine.Request) (domain.Decision, error)
}
type AnalysisService struct {
	Store   *Store
	Config  *profile.Store
	Engine  Analyzer
	Context context.Context
}
type AnalysisRecord struct {
	ID            string          `json:"analysis_id"`
	Symbol        string          `json:"symbol"`
	Timeframe     string          `json:"timeframe"`
	Style         string          `json:"style"`
	Status        string          `json:"status"`
	ConfigVersion int             `json:"config_version"`
	EngineCandles int             `json:"engine_candles"`
	ModelCandles  int             `json:"model_candles"`
	Model         string          `json:"model"`
	PromptVersion string          `json:"prompt_version"`
	Result        json.RawMessage `json:"result,omitempty"`
	Error         string          `json:"error,omitempty"`
}

func (s *AnalysisService) Submit(user int64, symbol, frame, style string) (string, error) {
	telegramID, e := s.Config.TelegramForWebUser(user)
	if e != nil {
		return "", errors.New("verified Telegram link required")
	}
	inst, ok := catalog.Find(symbol)
	if !ok || !inst.Enabled {
		return "", errors.New("unsupported symbol")
	}
	tf, e := catalog.ParseFrame(frame)
	if e != nil {
		return "", e
	}
	if e = catalog.ValidateCombination(tf, style); e != nil {
		return "", e
	}
	horizon, e := catalog.ParseHorizon(style)
	if e != nil {
		return "", e
	}
	access, e := s.Config.Access(telegramID, time.Now())
	if e != nil {
		return "", e
	}
	if !access.Policy.Allows(symbol, tf, horizon) {
		return "", errors.New("analysis selection is not allowed by entitlement policy")
	}
	cfg, e := s.Config.ActiveConfig()
	if e != nil {
		return "", e
	}
	cfg.HorizonCandles = catalog.PredictionCandles(horizon, cfg.HorizonCandles)
	id := profile.NewID()
	if id == "" {
		return "", errors.New("random unavailable")
	}
	if e = s.Config.Reserve(id, telegramID, time.Now()); e != nil {
		return "", e
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, _ = s.Store.DB.Exec(`UPDATE web_analyses SET status='analysis_unavailable',error='درخواست قبلی پس از توقف سرویس تکمیل نشد.' WHERE user_id=? AND status IN ('queued','running') AND updated_at<?`, user, time.Now().UTC().Add(-5*time.Minute).Format(time.RFC3339))
	_, e = s.Store.DB.Exec(`INSERT INTO web_analyses(id,user_id,symbol,timeframe,style,status,config_version,horizon_candles,created_at,updated_at) VALUES(?,?,?,?,?,'queued',?,?,?,?)`, id, user, symbol, frame, style, cfg.Version, cfg.HorizonCandles, now, now)
	if e != nil {
		_ = s.Config.SetUse(id, "released")
		return "", errors.New("analysis already running")
	}
	ctx := s.Context
	if ctx == nil {
		ctx = context.Background()
	}
	go s.run(ctx, id, user, telegramID, symbol, tf, style, cfg)
	return id, nil
}
func (s *AnalysisService) run(parent context.Context, id string, user, _ int64, symbol string, frame catalog.Timeframe, style string, cfg profile.AnalysisConfig) {
	timeout := time.Duration(cfg.MaxDurationSeconds) * time.Second
	if timeout < 30*time.Second || timeout > 10*time.Minute {
		timeout = 4 * time.Minute
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	ctx = ai.WithAnalysisID(ctx, id, "web")
	_, _ = s.Store.DB.Exec(`UPDATE web_analyses SET status='running',updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339), id)
	d, e := s.Engine.Analyze(ctx, engine.Request{Symbol: symbol, Timeframe: frame, Style: style, Language: "fa", EngineCandles: cfg.EngineCandles, ModelCandles: cfg.ModelCandles, HorizonCandles: cfg.HorizonCandles, ResponseTokenBudget: cfg.ResponseTokenBudget})
	status := "completed"
	reason := ""
	if e != nil || d.Final == "ANALYSIS_UNAVAILABLE" {
		status = "analysis_unavailable"
		reason = "تحلیل به دلیل خطای فنی یا دادهٔ بازار تکمیل نشد."
	}
	if status == "completed" {
		_ = s.Config.SetUse(id, "consumed")
	} else {
		_ = s.Config.SetUse(id, "released")
	}
	raw, _ := json.Marshal(d)
	n := len(d.Snapshot.Candles)
	sent := cfg.ModelCandles
	if sent > n {
		sent = n
	}
	if n == 0 {
		sent = 0
	}
	_, _ = s.Store.DB.Exec(`UPDATE web_analyses SET status=?,engine_candles=?,model_candles=?,model=?,prompt_version=?,result_json=?,error=?,updated_at=? WHERE id=?`, status, n, sent, d.Model, d.PromptVersion, string(raw), reason, time.Now().UTC().Format(time.RFC3339), id)
}
func (s *Store) Analysis(user int64, id string) (AnalysisRecord, error) {
	var a AnalysisRecord
	var raw string
	e := s.DB.QueryRow(`SELECT id,symbol,timeframe,style,status,config_version,engine_candles,model_candles,model,prompt_version,result_json,error FROM web_analyses WHERE user_id=? AND id=?`, user, id).Scan(&a.ID, &a.Symbol, &a.Timeframe, &a.Style, &a.Status, &a.ConfigVersion, &a.EngineCandles, &a.ModelCandles, &a.Model, &a.PromptVersion, &raw, &a.Error)
	if raw != "" {
		a.Result = json.RawMessage(raw)
	}
	return a, e
}
func (s *Store) RecentAnalyses(user int64) ([]AnalysisRecord, error) {
	rows, e := s.DB.Query(`SELECT id,symbol,timeframe,style,status,config_version,engine_candles,model_candles,model,prompt_version,result_json,error FROM web_analyses WHERE user_id=? ORDER BY created_at DESC LIMIT 20`, user)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []AnalysisRecord
	for rows.Next() {
		var a AnalysisRecord
		var raw string
		if e = rows.Scan(&a.ID, &a.Symbol, &a.Timeframe, &a.Style, &a.Status, &a.ConfigVersion, &a.EngineCandles, &a.ModelCandles, &a.Model, &a.PromptVersion, &raw, &a.Error); e != nil {
			return nil, e
		}
		if raw != "" {
			a.Result = json.RawMessage(raw)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

type AdminAnalysis struct {
	AnalysisRecord
	UserID int64 `json:"user_id"`
}

func (s *Store) AdminAnalysis(id string) (AdminAnalysis, error) {
	var a AdminAnalysis
	var raw string
	e := s.DB.QueryRow(`SELECT id,user_id,symbol,timeframe,style,status,config_version,engine_candles,model_candles,model,prompt_version,result_json,error FROM web_analyses WHERE id=?`, id).Scan(&a.ID, &a.UserID, &a.Symbol, &a.Timeframe, &a.Style, &a.Status, &a.ConfigVersion, &a.EngineCandles, &a.ModelCandles, &a.Model, &a.PromptVersion, &raw, &a.Error)
	if raw != "" {
		a.Result = json.RawMessage(raw)
	}
	return a, e
}
