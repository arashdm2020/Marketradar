package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
	"tradebot/internal/profile"
)

type captureTransport struct{ bodies []string }

func (c *captureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(r.Body)
	c.bodies = append(c.bodies, string(body))
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":99}}`)), Header: make(http.Header)}, nil
}
func TestBotQuotaRoutesToPurchaseAndExemptCanContinue(t *testing.T) {
	store, e := profile.Open(t.TempDir() + "/b.db")
	if e != nil {
		t.Fatal(e)
	}
	defer store.DB.Close()
	u, _ := store.Ensure(99)
	u.Completed = true
	u.State = "confirm:XAU/USD:5min:day"
	_ = store.Save(u)
	if e = store.Reserve("previous", 99, time.Now()); e != nil {
		t.Fatal(e)
	}
	_ = store.SetUse("previous", "consumed")
	transport := &captureTransport{}
	worker := NewAsyncWorker(nil, store, &mockEngine{})
	b := Bot{Store: store, Worker: worker, Token: "test", HTTP: &http.Client{Transport: transport}}
	worker.SetBot(&b)
	var event update
	if e = json.NewDecoder(bytes.NewBufferString(`{"callback_query":{"id":"cb","data":"run","from":{"id":99},"message":{"message_id":1,"chat":{"id":99,"type":"private"}}}}`)).Decode(&event); e != nil {
		t.Fatal(e)
	}
	b.handle(context.Background(), event)
	joined := strings.Join(transport.bodies, "\n")
	if !strings.Contains(joined, "buy") || !strings.Contains(joined, "link") || !strings.Contains(joined, "check_subscription") || !strings.Contains(joined, "سهمیهٔ تحلیل رایگان امروز") {
		t.Fatal("user was not routed to purchase")
	}
	if e = store.SetExempt(profile.AdminID, 99, true); e != nil {
		t.Fatal(e)
	}
	u, _ = store.Get(99)
	u.State = "confirm:XAU/USD:5min:day"
	_ = store.Save(u)
	b.handle(context.Background(), event)
	if !worker.IsInflight(99) {
		t.Fatal("exempt user could not queue another analysis")
	}
}
func TestStarsCheckoutAlwaysDisabled(t *testing.T) {
	t.Setenv("STARS_MONTHLY_PRICE", "500")
	store, e := profile.Open(t.TempDir() + "/s.db")
	if e != nil {
		t.Fatal(e)
	}
	defer store.DB.Close()
	u, _ := store.Ensure(101)
	u.Completed = true
	_ = store.Save(u)
	transport := &captureTransport{}
	b := Bot{Store: store, Token: "test", HTTP: &http.Client{Transport: transport}}
	var event update
	_ = json.NewDecoder(strings.NewReader(`{"callback_query":{"id":"cb","data":"buy:stars","from":{"id":101},"message":{"message_id":1,"chat":{"id":101,"type":"private"}}}}`)).Decode(&event)
	b.handle(context.Background(), event)
	joined := strings.Join(transport.bodies, "\n")
	if strings.Contains(joined, "sendInvoice") || !strings.Contains(joined, "غیرفعال") {
		t.Fatal("Stars sale was not disabled")
	}
}
