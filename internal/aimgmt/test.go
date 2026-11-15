package aimgmt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"tradebot/internal/profile"
)

type TestResult struct {
	ID               string `json:"id"`
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	Status           string `json:"status"`
	Sample           string `json:"sample"`
	LatencyMS        int64  `json:"latency_ms"`
	HTTPStatus       int    `json:"http_status"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	Error            string `json:"error,omitempty"`
	At               string `json:"at"`
}

func (s *Store) Test(ctx context.Context, admin int64, id int, client *http.Client) (TestResult, error) {
	if admin != profile.AdminID {
		return TestResult{}, errors.New("forbidden")
	}
	c, e := s.Client(id)
	if e != nil {
		return TestResult{}, e
	}
	if client == nil {
		client = c.HTTP
		if client == nil {
			client = &http.Client{Timeout: 20 * time.Second}
		}
	}
	if _, e = s.DB.Exec(`CREATE TABLE IF NOT EXISTS ai_test_runs(id TEXT PRIMARY KEY,version INTEGER NOT NULL,provider TEXT NOT NULL,model TEXT NOT NULL,status TEXT NOT NULL,sample TEXT NOT NULL,latency_ms INTEGER NOT NULL,at_utc TEXT NOT NULL)`); e != nil {
		return TestResult{}, e
	}
	_, _ = s.DB.Exec(`DELETE FROM ai_test_runs WHERE at_utc<?`, time.Now().UTC().Add(-30*24*time.Hour).Format(time.RFC3339))
	testID := profile.NewID()
	now := time.Now().UTC()
	out := TestResult{ID: testID, Provider: c.Provider, Model: c.Model, Status: "unavailable", At: now.Format(time.RFC3339)}
	tx, e := s.DB.Begin()
	if e != nil {
		return TestResult{}, e
	}
	defer tx.Rollback()
	var count int
	if e = tx.QueryRow(`SELECT count(*) FROM ai_test_runs WHERE at_utc>?`, now.Add(-time.Hour).Format(time.RFC3339)).Scan(&count); e != nil {
		return TestResult{}, e
	}
	if count >= 3 {
		return TestResult{}, errors.New("AI test hourly limit reached")
	}
	if _, e = tx.Exec(`INSERT INTO ai_test_runs(id,version,provider,model,status,sample,latency_ms,at_utc) VALUES(?,?,?,?,'running','',0,?)`, out.ID, id, out.Provider, out.Model, out.At); e != nil {
		return TestResult{}, e
	}
	if e = tx.Commit(); e != nil {
		return TestResult{}, e
	}
	message := `Return exactly {"ok":true} as a JSON object.`
	endpoint := strings.TrimRight(c.BaseURL, "/") + "/chat/completions"
	body := map[string]any{"model": c.Model, "max_tokens": 128, "temperature": 0, "stream": false, "messages": []map[string]string{{"role": "user", "content": message}}}
	if c.Provider == "google" {
		endpoint = strings.TrimRight(c.BaseURL, "/") + "/models/" + c.Model + ":generateContent"
		body = map[string]any{"contents": []map[string]any{{"parts": []map[string]string{{"text": message}}}}, "generationConfig": map[string]any{"maxOutputTokens": 128}}
	}
	raw, _ := json.Marshal(body)
	timeout := 20 * time.Second
	if c.HTTP != nil && c.HTTP.Timeout > 0 {
		timeout = c.HTTP.Timeout
	}
	testCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, e := http.NewRequestWithContext(testCtx, "POST", endpoint, bytes.NewReader(raw))
	if e != nil {
		return out, errors.New("test request setup failed")
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Provider == "google" {
		req.Header.Set("x-goog-api-key", c.Key)
	} else {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	start := time.Now()
	resp, e := client.Do(req)
	out.LatencyMS = time.Since(start).Milliseconds()
	if e == nil {
		defer resp.Body.Close()
		out.HTTPStatus = resp.StatusCode
		sample, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if resp.StatusCode == 200 {
			out.Status = "completed"
			if c.Provider == "google" {
				var wire struct {
					Candidates []struct {
						Content struct {
							Parts []struct {
								Text string `json:"text"`
							} `json:"parts"`
						} `json:"content"`
					} `json:"candidates"`
					Usage struct {
						Prompt     int `json:"promptTokenCount"`
						Completion int `json:"candidatesTokenCount"`
					} `json:"usageMetadata"`
				}
				if json.Unmarshal(sample, &wire) == nil && len(wire.Candidates) > 0 && len(wire.Candidates[0].Content.Parts) > 0 {
					out.Sample = wire.Candidates[0].Content.Parts[0].Text
				}
				out.PromptTokens = wire.Usage.Prompt
				out.CompletionTokens = wire.Usage.Completion
			} else {
				var wire struct {
					Choices []struct {
						Message struct {
							Content string `json:"content"`
						} `json:"message"`
					} `json:"choices"`
					Usage struct {
						Prompt     int `json:"prompt_tokens"`
						Completion int `json:"completion_tokens"`
					} `json:"usage"`
				}
				if json.Unmarshal(sample, &wire) == nil && len(wire.Choices) > 0 {
					out.Sample = wire.Choices[0].Message.Content
				}
				out.PromptTokens = wire.Usage.Prompt
				out.CompletionTokens = wire.Usage.Completion
			}
		} else {
			out.Status = fmt.Sprintf("HTTP %d", resp.StatusCode)
			out.Error = "provider returned a non-success status"
		}
		runes := []rune(out.Sample)
		if len(runes) > 1000 {
			out.Sample = string(runes[:1000])
		}
	}
	if e != nil {
		out.Error = "provider transport failure"
	}
	_, saveErr := s.DB.Exec(`UPDATE ai_test_runs SET status=?,sample=?,latency_ms=? WHERE id=?`, out.Status, out.Sample, out.LatencyMS, out.ID)
	if saveErr != nil {
		return TestResult{}, saveErr
	}
	if e != nil {
		return out, nil
	}
	return out, nil
}
