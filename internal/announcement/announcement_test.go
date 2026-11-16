package announcement

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tradebot/internal/profile"
)

func fixture(t *testing.T) Store {
	t.Helper()
	p, err := profile.Open(t.TempDir() + "/campaign.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.DB.Close() })
	for _, id := range []int64{profile.AdminID, 12345, 99999} {
		if _, err := p.Ensure(id); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []int64{profile.AdminID, 12345} {
		if err := p.SetDashboardMessage(id, 1); err != nil {
			t.Fatal(err)
		}
	}
	return Store{DB: p.DB}
}

func TestMessageSequenceAndEntities(t *testing.T) {
	m := Messages()
	if m[0] != emoji(emojiIDs[0]) || m[1] != emoji(emojiIDs[1]) {
		t.Fatal("standalone message contains extra text")
	}
	if strings.Contains(strings.Join(m[:], ""), "🚀") || strings.Contains(m[2], "[CUSTOM_EMOJI]") {
		t.Fatal("unrendered emoji marker")
	}
	english, turkish, persian := strings.Index(m[2], "English"), strings.Index(m[2], "Türkçe"), strings.Index(m[2], "فارسی")
	if english < 0 || turkish <= english || persian <= turkish || strings.Count(m[2], emoji(emojiIDs[2])) != 3 {
		t.Fatal("multilingual order or emoji count incorrect")
	}
	if strings.Count(m[2], "<b>") != 6 {
		t.Fatal("bold headings and quotas missing")
	}
}

func TestFrozenRecipientsAdminTestAndIdempotentBroadcast(t *testing.T) {
	s := fixture(t)
	ctx := context.Background()
	summary, err := s.Prepare(ctx)
	if err != nil || summary.Recipients != 2 || summary.Pending != 6 {
		t.Fatalf("prepare %+v %v", summary, err)
	}
	if err := (&profile.Store{DB: s.DB}).SetDashboardMessage(99999, 2); err != nil {
		t.Fatal(err)
	}
	summary, err = s.Prepare(ctx)
	if err != nil || summary.Recipients != 2 {
		t.Fatalf("recipient snapshot changed %+v %v", summary, err)
	}
	var calls []string
	send := func(_ context.Context, id int64, sequence int) (Delivery, error) {
		calls = append(calls, fmt.Sprintf("%d:%d", id, sequence))
		return Delivery{MessageID: int64(sequence), EmojiOK: true}, nil
	}
	result, err := s.Run(ctx, true, send)
	if err != nil || result.Accepted != 3 || !result.AdminEmojiOK {
		t.Fatalf("admin %+v %v", result, err)
	}
	if len(calls) != 3 || calls[0] != fmt.Sprintf("%d:1", profile.AdminID) || calls[1] != fmt.Sprintf("%d:2", profile.AdminID) || calls[2] != fmt.Sprintf("%d:3", profile.AdminID) {
		t.Fatalf("wrong sequence %v", calls)
	}
	_, err = s.Run(ctx, true, send)
	if err != nil || len(calls) != 3 {
		t.Fatalf("admin duplicate %v %v", calls, err)
	}
	result, err = s.Run(ctx, false, send)
	if err != nil || result.Accepted != 3 || result.Summary.Accepted != 6 {
		t.Fatalf("broadcast %+v %v", result, err)
	}
	_, err = s.Run(ctx, false, send)
	if err != nil || len(calls) != 6 {
		t.Fatalf("broadcast duplicate %v %v", calls, err)
	}
}

func TestUnknownDeliveryAndRateLimitAreSafe(t *testing.T) {
	s := fixture(t)
	ctx := context.Background()
	if _, err := s.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	_, err := s.Run(ctx, true, func(context.Context, int64, int) (Delivery, error) {
		return Delivery{}, &SendError{Class: "delivery_unknown"}
	})
	if err == nil {
		t.Fatal("unknown delivery accepted")
	}
	state, _ := s.state(ctx, profile.AdminID, 1)
	if state.Status != "unknown" {
		t.Fatalf("state %s", state.Status)
	}
	called := false
	_, err = s.Run(ctx, true, func(context.Context, int64, int) (Delivery, error) { called = true; return Delivery{}, nil })
	if err == nil || called {
		t.Fatal("ambiguous delivery retried")
	}
	if _, err = s.DB.Exec(`UPDATE announcement_deliveries SET status='pending' WHERE campaign_id=? AND telegram_id=? AND sequence=1`, CampaignID, profile.AdminID); err != nil {
		t.Fatal(err)
	}
	_, err = s.Run(ctx, true, func(context.Context, int64, int) (Delivery, error) {
		return Delivery{}, &SendError{Class: "rate_limited", RetryAfter: time.Hour}
	})
	if err == nil {
		t.Fatal("rate limit ignored")
	}
	state, _ = s.state(ctx, profile.AdminID, 1)
	if state.Status != "pending" || state.RetryAt == "" {
		t.Fatalf("rate-limit state %+v", state)
	}
	called = false
	_, err = s.Run(ctx, true, func(context.Context, int64, int) (Delivery, error) { called = true; return Delivery{}, nil })
	if err == nil || called {
		t.Fatal("RetryAfter ignored")
	}
}

func TestClientChecksCustomEmojiEntitiesAndClassifiesErrors(t *testing.T) {
	response := `{"ok":true,"result":{"message_id":19,"entities":[{"type":"custom_emoji","custom_emoji_id":"5382357040008021292"}]}}`
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, response)
	}))
	defer server.Close()
	client := Client{Token: "test", BaseURL: server.URL, HTTP: server.Client()}
	d, err := client.Send(context.Background(), 123, 1)
	if err != nil || !d.EmojiOK || d.MessageID != 19 || got["parse_mode"] != "HTML" {
		t.Fatalf("delivery %+v %v payload=%v", d, err, got)
	}
	response = `{"ok":false,"error_code":429,"parameters":{"retry_after":17}}`
	_, err = client.Send(context.Background(), 123, 1)
	var api *SendError
	if !errors.As(err, &api) || api.Class != "rate_limited" || api.RetryAfter != 17*time.Second {
		t.Fatalf("rate limit %v", err)
	}
	response = `{"ok":false,"error_code":403,"description":"bot was blocked by the user"}`
	_, err = client.Send(context.Background(), 123, 1)
	if !errors.As(err, &api) || api.Class != "recipient_rejected" {
		t.Fatalf("blocked user %v", err)
	}
}
