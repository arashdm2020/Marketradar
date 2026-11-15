package adminweb

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"
	"tradebot/internal/domain"
)

func (s *Server) analysisDetail(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r) {
		return
	}
	id := r.URL.Query().Get("id")
	if len(id) != 36 {
		http.Error(w, "invalid UUID", 400)
		return
	}
	var created, raw, symbol, frame, model, status string
	var e error
	e = s.Store.DB.QueryRow(`SELECT created_at,COALESCE(result_json,''),symbol,timeframe,model,status FROM request_states WHERE id=?`, id).Scan(&created, &raw, &symbol, &frame, &model, &status)
	if e == sql.ErrNoRows && s.Web != nil {
		e = s.Store.DB.QueryRow(`SELECT created_at,COALESCE(result_json,''),symbol,timeframe,model,status FROM web_analyses WHERE id=?`, id).Scan(&created, &raw, &symbol, &frame, &model, &status)
	}
	if e != nil {
		http.Error(w, "not found", 404)
		return
	}
	at, parseErr := time.Parse(time.RFC3339, created)
	if parseErr != nil || time.Since(at) > 30*24*time.Hour {
		http.Error(w, "detail expired", 410)
		return
	}
	var decision domain.Decision
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &decision)
	}
	var attempts []map[string]any
	rows, queryErr := s.Store.DB.Query(`SELECT provider,model,status,finish_reason,http_status,started_at,latency_ms,prompt_tokens,completion_tokens FROM ai_api_attempts WHERE analysis_id=? ORDER BY id`, id)
	if queryErr == nil {
		defer rows.Close()
		for rows.Next() {
			var provider, attemptModel, attemptStatus, finish, started string
			var httpStatus int
			var latency int64
			var input, output int
			if rows.Scan(&provider, &attemptModel, &attemptStatus, &finish, &httpStatus, &started, &latency, &input, &output) == nil {
				attempts = append(attempts, map[string]any{"provider": provider, "model": attemptModel, "status": attemptStatus, "finish_reason": finish, "http_status": httpStatus, "started_at": started, "latency_ms": latency, "prompt_tokens": input, "completion_tokens": output})
			}
		}
	}
	jsonOut(w, map[string]any{"analysis_id": id, "request": map[string]any{"symbol": symbol, "timeframe": frame, "model": model}, "response": map[string]any{"signal": decision.Proposed.Signal, "entry": decision.Proposed.Entry, "stop_loss": decision.Proposed.StopLoss, "take_profit_1": decision.Proposed.TP1, "take_profit_2": decision.Proposed.TP2, "setup_quality": decision.Proposed.SetupQuality, "final": decision.Final}, "api_attempts": attempts, "status": status, "latency_ms": decision.LatencyMS, "prompt_tokens": decision.PromptTokens, "completion_tokens": decision.CompletionTokens, "retention_days": 30})
}
