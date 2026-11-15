package adminweb

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"
	"tradebot/internal/domain"
)

type metricDay struct {
	Day              string   `json:"day"`
	Success          int      `json:"success"`
	Failure          int      `json:"failure"`
	InputTokens      int      `json:"input_tokens"`
	OutputTokens     int      `json:"output_tokens"`
	AvgLatencyMS     *float64 `json:"avg_latency_ms"`
	EstimatedCostBNB *string  `json:"estimated_cost_bnb"`
}
type dashboardData struct {
	Analyses         int         `json:"analyses"`
	TelegramAnalyses int         `json:"telegram_analyses"`
	WebAnalyses      int         `json:"web_analyses"`
	APIAttempts      *int        `json:"api_attempts"`
	Success          int         `json:"success"`
	Failure          int         `json:"failure"`
	APISuccess       *int        `json:"api_success"`
	APIErrors        *int        `json:"api_errors"`
	AvgLatencyMS     *float64    `json:"avg_latency_ms"`
	P50LatencyMS     *int64      `json:"p50_latency_ms"`
	P95LatencyMS     *int64      `json:"p95_latency_ms"`
	InputTokens      *int        `json:"input_tokens"`
	OutputTokens     *int        `json:"output_tokens"`
	EstimatedCostBNB *string     `json:"estimated_cost_bnb"`
	Series           []metricDay `json:"series"`
	Note             string      `json:"note"`
	FreshAt          string      `json:"fresh_at"`
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r) {
		return
	}
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	for _, v := range []string{from, to} {
		if v != "" {
			if _, e := time.Parse("2006-01-02", v); e != nil {
				http.Error(w, "invalid date", 400)
				return
			}
		}
	}
	model := r.URL.Query().Get("model")
	if len(model) > 120 {
		http.Error(w, "invalid model", 400)
		return
	}
	channel := r.URL.Query().Get("channel")
	if channel != "" && channel != "telegram" && channel != "web" {
		http.Error(w, "invalid channel", 400)
		return
	}
	out := dashboardData{Note: "هزینهٔ تخمینی بدون جدول قیمت ارائه‌دهنده در دسترس نیست.", FreshAt: time.Now().UTC().Format(time.RFC3339)}
	days := map[string]*metricDay{}
	latencyByDay := map[string]int64{}
	latencyCountByDay := map[string]int{}
	var latencyTotal int64
	var latencyCount int
	input, output := 0, 0
	read := func(table, kind string) {
		rows, e := s.Store.DB.Query(`SELECT created_at,status,model,result_json FROM `+table+` WHERE (?='' OR substr(created_at,1,10)>=?) AND (?='' OR substr(created_at,1,10)<=?) AND (?='' OR model=?)`, from, from, to, to, model, model)
		if e != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			var created, status, m string
			var raw sql.NullString
			if rows.Scan(&created, &status, &m, &raw) != nil {
				continue
			}
			out.Analyses++
			if kind == "telegram" {
				out.TelegramAnalyses++
			} else {
				out.WebAnalyses++
			}
			day := created
			if len(day) > 10 {
				day = day[:10]
			}
			d := days[day]
			if d == nil {
				d = &metricDay{Day: day}
				days[day] = d
			}
			if status == "delivered" || status == "completed" {
				out.Success++
				d.Success++
			} else if status == "failed" || status == "analysis_unavailable" || status == "delivery_failed" {
				out.Failure++
				d.Failure++
			}
			if raw.Valid && raw.String != "" {
				var decision domain.Decision
				if json.Unmarshal([]byte(raw.String), &decision) == nil && decision.Model != "" {
					latencyTotal += decision.LatencyMS
					latencyCount++
					latencyByDay[day] += decision.LatencyMS
					latencyCountByDay[day]++
					input += decision.PromptTokens
					output += decision.CompletionTokens
					d.InputTokens += decision.PromptTokens
					d.OutputTokens += decision.CompletionTokens
				}
			}
		}
	}
	if channel != "web" {
		read("request_states", "telegram")
	}
	if channel != "telegram" {
		read("web_analyses", "web")
	}
	if latencyCount > 0 {
		avg := float64(latencyTotal) / float64(latencyCount)
		out.AvgLatencyMS = &avg
		out.InputTokens = &input
		out.OutputTokens = &output
	}
	rows, e := s.Store.DB.Query(`SELECT status,latency_ms FROM ai_api_attempts WHERE (?='' OR substr(started_at,1,10)>=?) AND (?='' OR substr(started_at,1,10)<=?) AND (?='' OR model=?) AND (?='' OR channel=?)`, from, from, to, to, model, model, channel, channel)
	if e == nil {
		defer rows.Close()
		attempts, success, fail := 0, 0, 0
		var latencies []int
		for rows.Next() {
			var status string
			var latency int
			if rows.Scan(&status, &latency) != nil {
				continue
			}
			attempts++
			if latency > 0 {
				latencies = append(latencies, latency)
			}
			if status == "completed" {
				success++
			} else {
				fail++
			}
		}
		out.APIAttempts = &attempts
		out.APISuccess = &success
		out.APIErrors = &fail
		if len(latencies) > 0 {
			sort.Ints(latencies)
			p50 := int64(latencies[(len(latencies)-1)*50/100])
			p95 := int64(latencies[(len(latencies)-1)*95/100])
			out.P50LatencyMS = &p50
			out.P95LatencyMS = &p95
		}
	}
	for _, d := range days {
		if latencyCountByDay[d.Day] > 0 {
			avg := float64(latencyByDay[d.Day]) / float64(latencyCountByDay[d.Day])
			d.AvgLatencyMS = &avg
		}
		out.Series = append(out.Series, *d)
	}
	sort.Slice(out.Series, func(i, j int) bool { return strings.Compare(out.Series[i].Day, out.Series[j].Day) < 0 })
	jsonOut(w, out)
}
