package marketdata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"
	"tradebot/internal/domain"
)

type Client struct {
	HTTP         *http.Client
	BaseURL, Key string
}
type response struct {
	Status  string                                              `json:"status"`
	Message string                                              `json:"message"`
	Values  []struct{ Datetime, Open, High, Low, Close string } `json:"values"`
}

func (c Client) Fetch(ctx context.Context, symbol, interval string, count int) ([]domain.Candle, error) {
	if c.Key == "" {
		return nil, fmt.Errorf("Twelve Data key missing")
	}
	base := c.BaseURL
	if base == "" {
		base = "https://api.twelvedata.com/time_series"
	}
	u, e := url.Parse(base)
	if e != nil {
		return nil, e
	}
	q := u.Query()
	q.Set("symbol", symbol)
	q.Set("interval", interval)
	q.Set("outputsize", strconv.Itoa(count))
	q.Set("timezone", "UTC")
	q.Set("apikey", c.Key)
	u.RawQuery = q.Encode()
	h := c.HTTP
	if h == nil {
		h = &http.Client{Timeout: 20 * time.Second}
	}
	var body []byte
	for attempt := 0; attempt < 4; attempt++ {
		req, e := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
		if e != nil {
			return nil, e
		}
		resp, e := h.Do(req)
		if e == nil {
			body, e = io.ReadAll(io.LimitReader(resp.Body, 4<<20))
			resp.Body.Close()
			if e == nil && resp.StatusCode == 200 {
				break
			}
			if resp.StatusCode != 429 && resp.StatusCode < 500 {
				return nil, fmt.Errorf("Twelve Data HTTP %d: %s", resp.StatusCode, safeMessage(body))
			}
		}
		if attempt == 3 {
			return nil, fmt.Errorf("Twelve Data unavailable after retries")
		}
		delay := time.Duration(1<<attempt)*time.Second + time.Duration(rand.Intn(300))*time.Millisecond
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
	var raw response
	if e = json.Unmarshal(body, &raw); e != nil {
		return nil, fmt.Errorf("Twelve Data JSON: %w", e)
	}
	if raw.Status == "error" {
		return nil, fmt.Errorf("Twelve Data: %s", raw.Message)
	}
	out := make([]domain.Candle, 0, len(raw.Values))
	for _, v := range raw.Values {
		t, e := time.ParseInLocation("2006-01-02 15:04:05", v.Datetime, time.UTC)
		if e != nil {
			return nil, fmt.Errorf("candle time: %w", e)
		}
		parse := func(s string) (float64, error) { return strconv.ParseFloat(s, 64) }
		o, e := parse(v.Open)
		if e != nil {
			return nil, e
		}
		hi, e := parse(v.High)
		if e != nil {
			return nil, e
		}
		lo, e := parse(v.Low)
		if e != nil {
			return nil, e
		}
		cl, e := parse(v.Close)
		if e != nil {
			return nil, e
		}
		out = append(out, domain.Candle{Time: t, Open: o, High: hi, Low: lo, Close: cl})
	}
	return Normalize(out)
}
func safeMessage(b []byte) string {
	var v struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(b, &v) == nil && v.Message != "" {
		return v.Message
	}
	return "provider request rejected"
}
func Normalize(in []domain.Candle) ([]domain.Candle, error) {
	out := append([]domain.Candle(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	for i, c := range out {
		if e := c.Validate(); e != nil {
			return nil, fmt.Errorf("candle %d: %w", i, e)
		}
		if i > 0 && !c.Time.After(out[i-1].Time) {
			return nil, fmt.Errorf("duplicate candle %s", c.Time)
		}
	}
	return out, nil
}
func Closed(in []domain.Candle, at time.Time, interval, buffer time.Duration) []domain.Candle {
	out := make([]domain.Candle, 0, len(in))
	for _, c := range in {
		if !c.Time.Add(interval).Add(buffer).After(at) {
			out = append(out, c)
		}
	}
	return out
}

// FX gold normally closes Friday 22:00 UTC through Sunday 22:00 UTC; DST and holidays remain provider-dependent.
func SessionOpen(t time.Time) bool {
	w := t.UTC().Weekday()
	h := t.UTC().Hour()
	return !(w == time.Saturday || w == time.Sunday && h < 22 || w == time.Friday && h >= 22)
}
func Check(in []domain.Candle, at time.Time, interval, buffer time.Duration) domain.Quality {
	q := domain.Quality{Fresh: true}
	if len(in) == 0 {
		return domain.Quality{StaleReason: "no candles"}
	}
	for i := 1; i < len(in); i++ {
		for t := in[i-1].Time.Add(interval); t.Before(in[i].Time); t = t.Add(interval) {
			if SessionOpen(t) {
				q.Gaps = append(q.Gaps, t.Format(time.RFC3339))
				if len(q.Gaps) > 20 {
					break
				}
			}
		}
	}
	last := in[len(in)-1].Time
	if SessionOpen(at.Add(-interval)) && at.Sub(last) > 2*interval+buffer {
		q.Fresh = false
		q.StaleReason = "latest candle is stale"
	}
	if len(q.Gaps) > 0 {
		q.Fresh = false
	}
	return q
}
