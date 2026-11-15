package adminweb

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"
	"tradebot/internal/domain"
	"tradebot/internal/profile"
)

func (s *Server) compareConfig(w http.ResponseWriter, r *http.Request) {
	if !s.mutation(w, r) {
		return
	}
	if s.AIStore == nil {
		http.Error(w, "AI management unavailable", 503)
		return
	}
	id := r.FormValue("analysis_id")
	version, e := strconv.Atoi(r.FormValue("version"))
	if e != nil || len(id) != 36 {
		http.Error(w, "invalid comparison", 400)
		return
	}
	var raw string
	e = s.Store.DB.QueryRow(`SELECT COALESCE(result_json,'') FROM request_states WHERE id=?`, id).Scan(&raw)
	if e == sql.ErrNoRows {
		e = s.Store.DB.QueryRow(`SELECT COALESCE(result_json,'') FROM web_analyses WHERE id=?`, id).Scan(&raw)
	}
	if e != nil || raw == "" {
		http.Error(w, "source snapshot unavailable", 404)
		return
	}
	var decision domain.Decision
	if json.Unmarshal([]byte(raw), &decision) != nil || len(decision.Snapshot.Candles) < 250 {
		http.Error(w, "source snapshot invalid", 422)
		return
	}
	var candidate profile.AnalysisConfig
	if e = s.Store.DB.QueryRow(`SELECT version,engine_candles,model_candles,horizon_candles FROM analysis_config WHERE version=? AND active=0`, version).Scan(&candidate.Version, &candidate.EngineCandles, &candidate.ModelCandles, &candidate.HorizonCandles); e != nil {
		http.Error(w, "draft unavailable", 404)
		return
	}
	if candidate.EngineCandles != len(decision.Snapshot.Candles) {
		http.Error(w, "same-snapshot comparison requires equal engine candle count", 422)
		return
	}
	baseline, e := s.Store.ActiveConfig()
	if e != nil || baseline.EngineCandles != candidate.EngineCandles {
		http.Error(w, "baseline candle count differs", 422)
		return
	}
	if _, e = s.Store.DB.Exec(`CREATE TABLE IF NOT EXISTS analysis_comparisons(id TEXT PRIMARY KEY,source_id TEXT NOT NULL,baseline_version INTEGER NOT NULL,draft_version INTEGER NOT NULL,baseline_signal TEXT NOT NULL,candidate_signal TEXT NOT NULL,created_at TEXT NOT NULL)`); e != nil {
		http.Error(w, "database error", 500)
		return
	}
	var n int
	_ = s.Store.DB.QueryRow(`SELECT count(*) FROM analysis_comparisons WHERE created_at>?`, time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)).Scan(&n)
	if n >= 1 {
		http.Error(w, "comparison hourly limit reached", 429)
		return
	}
	model, e := s.AIStore.Client(0)
	if e != nil {
		http.Error(w, "AI unavailable", 503)
		return
	}
	if s.AIHTTP != nil {
		model.HTTP = s.AIHTTP
	}
	comparisonID := profile.NewID()
	_, e = s.Store.DB.Exec(`INSERT INTO analysis_comparisons(id,source_id,baseline_version,draft_version,baseline_signal,candidate_signal,created_at) VALUES(?,?,?,?,?,?,?)`, comparisonID, id, baseline.Version, candidate.Version, "pending", "pending", time.Now().UTC().Format(time.RFC3339))
	if e != nil {
		http.Error(w, "comparison save failed", 500)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	first := model
	first.ModelCandles = baseline.ModelCandles
	second := model
	second.ModelCandles = candidate.ModelCandles
	a, e := first.AnalyzeWithContext(ctx, decision.Snapshot, map[string]any{"comparison_only": true, "horizon_candles": baseline.HorizonCandles})
	if e != nil {
		http.Error(w, "baseline inference failed", 502)
		return
	}
	b, e := second.AnalyzeWithContext(ctx, decision.Snapshot, map[string]any{"comparison_only": true, "horizon_candles": candidate.HorizonCandles})
	if e != nil {
		http.Error(w, "candidate inference failed", 502)
		return
	}
	_, e = s.Store.DB.Exec(`UPDATE analysis_comparisons SET baseline_signal=?,candidate_signal=? WHERE id=?`, a.Signal.Signal, b.Signal.Signal, comparisonID)
	if e != nil {
		http.Error(w, "comparison save failed", 500)
		return
	}
	jsonOut(w, map[string]any{"comparison_id": comparisonID, "source_analysis_id": id, "model": model.Model, "baseline": map[string]any{"version": baseline.Version, "model_candles": baseline.ModelCandles, "signal": a.Signal.Signal, "latency_ms": a.LatencyMS, "input_tokens": a.PromptTokens, "output_tokens": a.CompletionTokens}, "candidate": map[string]any{"version": candidate.Version, "model_candles": candidate.ModelCandles, "signal": b.Signal.Signal, "latency_ms": b.LatencyMS, "input_tokens": b.PromptTokens, "output_tokens": b.CompletionTokens}, "user_report_changed": false})
}
