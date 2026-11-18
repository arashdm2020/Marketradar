package marketdata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"tradebot/internal/catalog"
	"tradebot/internal/domain"
)

func TestRequiredClosedCandlesByTimeframe(t *testing.T) {
	for _, tc := range []struct {
		frame catalog.Timeframe
		want  int
	}{{"1min", 200}, {"3min", 200}, {"5min", 200}, {"15min", 200}, {"30min", 250}, {"1h", 250}, {"4h", 250}, {"1day", 250}} {
		count, err := catalog.RequiredCandles(tc.frame)
		if err != nil || count != tc.want {
			t.Fatalf("%s count=%d err=%v", tc.frame, count, err)
		}
		at := time.Date(2026, 11, 18, 12, 0, 31, 0, time.UTC)
		start := at.Truncate(tc.frame.Duration()).Add(-time.Duration(count) * tc.frame.Duration())
		bars := make([]domain.Candle, count+1)
		for i := range bars {
			bars[i] = candle(start.Add(time.Duration(i) * tc.frame.Duration()))
		}
		bars[11].High = 100000 // legitimate volatility is retained
		selected, err := SelectClosed(bars, at, tc.frame.Duration(), 30*time.Second, count)
		if err != nil || len(selected) != count || !selected[0].Time.Equal(start) || selected[11].High != 100000 {
			t.Fatalf("%s selected=%d err=%v", tc.frame, len(selected), err)
		}
		if !selected[len(selected)-1].Time.Before(bars[len(bars)-1].Time) {
			t.Fatal("unfinished bar included")
		}
	}
}

func TestSelectionRejectsDuplicateMisalignedInvalidAndInsufficient(t *testing.T) {
	at := time.Date(2026, 11, 18, 12, 0, 31, 0, time.UTC)
	bars := []domain.Candle{candle(at.Add(-10 * time.Minute).Truncate(5 * time.Minute)), candle(at.Add(-5 * time.Minute).Truncate(5 * time.Minute))}
	if _, err := SelectClosed(bars, at, 5*time.Minute, 30*time.Second, 3); err == nil || !strings.Contains(err.Error(), "insufficient") {
		t.Fatalf("insufficient: %v", err)
	}
	if _, err := SelectClosed(append(bars, bars[0]), at, 5*time.Minute, 30*time.Second, 2); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate: %v", err)
	}
	misaligned := append([]domain.Candle(nil), bars...)
	misaligned[0].Time = misaligned[0].Time.Add(time.Minute)
	if _, err := SelectClosed(misaligned, at, 5*time.Minute, 30*time.Second, 2); err == nil || !strings.Contains(err.Error(), "aligned") {
		t.Fatalf("alignment: %v", err)
	}
	invalid := append([]domain.Candle(nil), bars...)
	invalid[0].Low = 102
	if _, err := SelectClosed(invalid, at, 5*time.Minute, 30*time.Second, 2); err == nil {
		t.Fatal("invalid OHLC accepted")
	}
	gap := []domain.Candle{bars[0], candle(bars[0].Time.Add(10 * time.Minute))}
	if q := Check(gap, at, 5*time.Minute, 30*time.Second); len(q.Gaps) == 0 {
		t.Fatal("missing candle not detected")
	}
}

func TestFetchClosedRequestsMoreOnlyWhenFirstFullPageIsUnconfirmed(t *testing.T) {
	at := time.Date(2026, 11, 18, 12, 0, 31, 0, time.UTC)
	var requested []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		size, _ := strconv.Atoi(r.URL.Query().Get("outputsize"))
		requested = append(requested, size)
		last := at.Truncate(5 * time.Minute).Add(10 * time.Minute)
		values := make([]map[string]string, 0, size)
		for i := 0; i < size; i++ {
			values = append(values, map[string]string{"datetime": last.Add(-time.Duration(i) * 5 * time.Minute).Format("2006-01-02 15:04:05"), "open": "100", "high": "101", "low": "99", "close": "100"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"values": values})
	}))
	defer server.Close()
	client := Client{Key: "test", BaseURL: server.URL, HTTP: server.Client()}
	selected, err := client.FetchClosed(context.Background(), "XAU/USD", "5min", 200, at, 30*time.Second)
	if err != nil || len(selected) != 200 || len(requested) != 2 || requested[0] != 202 || requested[1] != 216 {
		t.Fatalf("selected=%d requests=%v err=%v", len(selected), requested, err)
	}
	if selected[199].Time.Add(5*time.Minute + 30*time.Second).After(at) {
		t.Fatal("unclosed last bar")
	}
}

func TestAggregate3MinUsesOnlyCompleteMinuteGroups(t *testing.T) {
	at := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	bars := []domain.Candle{candle(at), candle(at.Add(time.Minute)), candle(at.Add(2 * time.Minute)), candle(at.Add(3 * time.Minute)), candle(at.Add(5 * time.Minute))}
	bars[1].High = 9999
	out, err := Aggregate3Min(bars)
	if err != nil || len(out) != 1 || out[0].High != 9999 || !out[0].Time.Equal(at) {
		t.Fatalf("M3 %+v %v", out, err)
	}
}

func TestProviderShortPageFailsWithoutPadding(t *testing.T) {
	at := time.Date(2026, 11, 18, 12, 0, 31, 0, time.UTC)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var values []map[string]string
		for i := 0; i < 100; i++ {
			values = append(values, map[string]string{"datetime": at.Truncate(5 * time.Minute).Add(-time.Duration(i+1) * 5 * time.Minute).Format("2006-01-02 15:04:05"), "open": "100", "high": "101", "low": "99", "close": "100"})
		}
		fmt.Fprint(w, `{"values":`)
		_ = json.NewEncoder(w).Encode(values)
		fmt.Fprint(w, "}")
	}))
	defer server.Close()
	client := Client{Key: "test", BaseURL: server.URL, HTTP: server.Client()}
	_, err := client.FetchClosed(context.Background(), "XAU/USD", "5min", 200, at, 30*time.Second)
	if err == nil || !strings.Contains(err.Error(), "insufficient") || requests != 1 {
		t.Fatalf("error=%v requests=%d", err, requests)
	}
}
