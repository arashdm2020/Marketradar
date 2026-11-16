package profile

import (
	"database/sql"
	"errors"
	"time"
)

const AdminID int64 = 8697362188

type AdminUser struct {
	ID                 int64  `json:"id"`
	FirstName          string `json:"first_name"`
	LastName           string `json:"last_name"`
	DisplayName        string `json:"display_name"`
	ProvidedName       string `json:"provided_display_name"`
	Username           string `json:"username"`
	PhoneNumber        string `json:"phone_number"`
	PhoneVerified      bool   `json:"phone_verified"`
	PhotoURL           string `json:"photo_url"`
	Plan               string `json:"plan"`
	SubscriptionUntil  string `json:"subscription_until"`
	SubscriptionActive bool   `json:"subscription_active"`
	Exempt             bool   `json:"exempt"`
	FreeUsedToday      bool   `json:"free_used_today"`
	FreeRemaining      int    `json:"free_remaining"`
	DailyAllowance     int    `json:"daily_allowance"`
	DailyUsed          int    `json:"daily_used"`
	DailyRemaining     int    `json:"daily_remaining"`
	LegacyUnlimited    bool   `json:"legacy_unlimited"`
}

func (s *Store) AdminUsers(admin int64) ([]AdminUser, error) {
	if admin != AdminID {
		return nil, errors.New("forbidden")
	}
	rows, e := s.DB.Query(`SELECT id FROM users ORDER BY id`)
	if e != nil {
		return nil, e
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, e
		}
		ids = append(ids, id)
	}
	if e = rows.Close(); e != nil {
		return nil, e
	}
	out := make([]AdminUser, 0, len(ids))
	for _, id := range ids {
		a, accessErr := s.Access(id, time.Now())
		if accessErr != nil {
			return nil, accessErr
		}
		u := AdminUser{ID: id, Plan: string(a.Policy.ID), SubscriptionUntil: a.SubscriptionUntil, SubscriptionActive: a.SubscriptionActive, Exempt: a.Exempt, DailyAllowance: a.Allowance, DailyUsed: a.Used, DailyRemaining: a.Remaining, LegacyUnlimited: a.LegacyUnlimited}
		p, profileErr := s.TelegramProfile(id)
		if profileErr != nil {
			return nil, profileErr
		}
		u.FirstName, u.LastName, u.DisplayName = p.FirstName, p.LastName, p.DisplayName()
		u.ProvidedName = p.ProvidedName
		u.Username, u.PhoneNumber, u.PhoneVerified, u.PhotoURL = p.Username, p.PhoneNumber, p.PhoneVerified, p.PhotoURL
		u.FreeUsedToday = a.Policy.ID == "visitor" && a.Used > 0
		if a.Policy.ID == "visitor" {
			u.FreeRemaining = a.Remaining
		}
		out = append(out, u)
	}
	return out, nil
}

type AdminAnalysis struct {
	ID             string `json:"analysis_id"`
	UserID         int64  `json:"user_id"`
	Symbol         string `json:"symbol"`
	Timeframe      string `json:"timeframe"`
	Status         string `json:"status"`
	Model          string `json:"model"`
	PromptVersion  string `json:"prompt_version"`
	EngineCandles  int    `json:"engine_candles"`
	ModelCandles   int    `json:"model_candles"`
	HorizonCandles int    `json:"horizon_candles"`
	ConfigVersion  int    `json:"config_version"`
	CreatedAt      string `json:"created_at"`
	DeliveredAt    string `json:"delivered_at"`
}

func (s *Store) FindAnalysis(admin int64, id string) (AdminAnalysis, error) {
	if admin != AdminID {
		return AdminAnalysis{}, errors.New("forbidden")
	}
	var a AdminAnalysis
	var result sql.NullString
	e := s.DB.QueryRow(`SELECT r.id,r.user_id,r.symbol,r.timeframe,r.status,r.created_at,CASE WHEN r.status='delivered' THEN r.updated_at ELSE '' END,r.result_json,r.model,r.prompt_version,r.config_version,r.engine_candles,r.model_candles,r.horizon_candles FROM request_states r WHERE r.id=?`, id).Scan(&a.ID, &a.UserID, &a.Symbol, &a.Timeframe, &a.Status, &a.CreatedAt, &a.DeliveredAt, &result, &a.Model, &a.PromptVersion, &a.ConfigVersion, &a.EngineCandles, &a.ModelCandles, &a.HorizonCandles)
	if e != nil {
		return a, e
	}
	return a, nil
}

type AnalysisConfig struct {
	Version             int  `json:"version"`
	EngineCandles       int  `json:"engine_candles"`
	ModelCandles        int  `json:"model_candles"`
	HorizonCandles      int  `json:"horizon_candles"`
	MaxDurationSeconds  int  `json:"max_duration_seconds"`
	ResponseTokenBudget int  `json:"response_token_budget"`
	Active              bool `json:"active"`
}

func (s *Store) ActiveConfig() (AnalysisConfig, error) {
	var c AnalysisConfig
	var active int
	e := s.DB.QueryRow(`SELECT version,engine_candles,model_candles,horizon_candles,max_duration_seconds,response_token_budget,active FROM analysis_config WHERE active=1 LIMIT 1`).Scan(&c.Version, &c.EngineCandles, &c.ModelCandles, &c.HorizonCandles, &c.MaxDurationSeconds, &c.ResponseTokenBudget, &active)
	c.Active = active == 1
	return c, e
}

func (s *Store) DraftConfig(admin int64, engineCandles, modelCandles, horizon, maxDuration, responseTokens int) (AnalysisConfig, error) {
	if admin != AdminID {
		return AnalysisConfig{}, errors.New("forbidden")
	}
	if engineCandles < 250 || engineCandles > 1000 || modelCandles < 50 || modelCandles > 500 || modelCandles > engineCandles || horizon < 1 || horizon > 96 || maxDuration < 30 || maxDuration > 600 || responseTokens < 128 || responseTokens > 8192 {
		return AnalysisConfig{}, errors.New("invalid analysis limits")
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return AnalysisConfig{}, e
	}
	defer tx.Rollback()
	var version int
	if e = tx.QueryRow(`SELECT COALESCE(MAX(version),0)+1 FROM analysis_config`).Scan(&version); e != nil {
		return AnalysisConfig{}, e
	}
	if _, e = tx.Exec(`INSERT INTO analysis_config(version,engine_candles,model_candles,horizon_candles,max_duration_seconds,response_token_budget,active) VALUES(?,?,?,?,?,?,0)`, version, engineCandles, modelCandles, horizon, maxDuration, responseTokens); e != nil {
		return AnalysisConfig{}, e
	}
	if _, e = tx.Exec(`INSERT INTO admin_audit(admin_id,action,detail,at_utc) VALUES(?,'analysis_config_draft',?,?)`, admin, version, time.Now().UTC().Format(time.RFC3339)); e != nil {
		return AnalysisConfig{}, e
	}
	if e = tx.Commit(); e != nil {
		return AnalysisConfig{}, e
	}
	return AnalysisConfig{Version: version, EngineCandles: engineCandles, ModelCandles: modelCandles, HorizonCandles: horizon, MaxDurationSeconds: maxDuration, ResponseTokenBudget: responseTokens}, nil
}

func (s *Store) SetConfig(admin int64, version int) error {
	if admin != AdminID {
		return errors.New("forbidden")
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var n int
	if e = tx.QueryRow(`SELECT count(*) FROM analysis_config WHERE version=?`, version).Scan(&n); e != nil {
		return e
	}
	if n != 1 {
		return sql.ErrNoRows
	}
	if _, e = tx.Exec(`UPDATE analysis_config SET active=CASE WHEN version=? THEN 1 ELSE 0 END`, version); e != nil {
		return e
	}
	if _, e = tx.Exec(`INSERT INTO admin_audit(admin_id,action,detail,at_utc) VALUES(?,'analysis_config',?,?)`, admin, version, time.Now().UTC().Format(time.RFC3339)); e != nil {
		return e
	}
	return tx.Commit()
}
