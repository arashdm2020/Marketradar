package webapp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"tradebot/internal/bnb"
	"tradebot/internal/catalog"
	"tradebot/internal/profile"
	"tradebot/internal/telegram"
)

type telegramDeliveryStub struct{ sent atomic.Int32 }

func (x *telegramDeliveryStub) RoundTrip(*http.Request) (*http.Response, error) {
	x.sent.Add(1)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":42}}`)), Header: make(http.Header)}, nil
}

func TestQuotaLinkInvoicePaymentAndExpiry(t *testing.T) {
	p, web := testStores(t)
	defer p.DB.Close()
	const telegramID int64 = 75001
	if _, e := p.Ensure(telegramID); e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	if e := p.Reserve("first", telegramID, now); e != nil {
		t.Fatal(e)
	}
	if e := p.SetUse("first", "consumed"); e != nil {
		t.Fatal(e)
	}
	if e := p.Reserve("second", telegramID, now); !errors.Is(e, profile.ErrQuota) {
		t.Fatalf("quota: %v", e)
	}
	webUser, e := web.Register("linked@example.com", "long-linked-password")
	if e != nil {
		t.Fatal(e)
	}
	token, e := p.IssueWebLink(telegramID, now)
	if e != nil {
		t.Fatal(e)
	}
	if e := p.ConsumeWebLink(token, webUser, now.Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	if e := p.ConsumeWebLink(token, webUser, now.Add(time.Minute)); e == nil {
		t.Fatal("replayed token accepted")
	}
	expired, e := p.IssueWebLink(telegramID, now)
	if e != nil {
		t.Fatal(e)
	}
	if e := p.ConsumeWebLink(expired, webUser, now.Add(11*time.Minute)); e == nil {
		t.Fatal("expired token accepted")
	}
	if e := p.Reserve("third", telegramID, now); !errors.Is(e, profile.ErrQuota) {
		t.Fatalf("link without payment bypassed quota: %v", e)
	}
	chain := &fakeChain{head: 300, chainID: 97, blocks: map[uint64]bnb.Block{}, receipts: map[string]bnb.Receipt{}}
	b := &Billing{Store: web, Deriver: testDeriver(t), Chain: chain, ExpectedChainID: 97, Confirmations: 2, ReferenceVerified: true, TestInvoicesEnabled: true, TestEmail: "linked@example.com"}
	if e := web.SetPrice(profile.AdminID, "0.01"); e != nil {
		t.Fatal(e)
	}
	inv, e := b.CreateInvoice(context.Background(), webUser, now)
	if e != nil {
		t.Fatal(e)
	}
	addPayment(chain, 301, inv.CreatedAt.Add(time.Minute), inv.Address, "0x2386f26fc10000", 52)
	chain.head = 302
	if _, e := b.ScanOnce(context.Background()); e != nil {
		t.Fatal(e)
	}
	_, before, e := p.WebSubscription(telegramID)
	if e != nil || !before.After(now) {
		t.Fatalf("payment not linked: %v %v", before, e)
	}
	transport := &telegramDeliveryStub{}
	worker := telegram.NewAsyncWorker(nil, p, fakeAnalyzer{})
	bot := &telegram.Bot{Token: "test", HTTP: &http.Client{Transport: transport}, Store: p, Worker: worker}
	worker.SetBot(bot)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	worker.Start(ctx)
	requestID := profile.NewID()
	queued, e := worker.Enqueue(ctx, telegram.AnalysisRequest{ID: requestID, UserID: telegramID, ChatID: telegramID, Symbol: "XAU/USD", Timeframe: catalog.Timeframe("5min"), Style: "scalp", ModelCandles: 500, ConfigVersion: 1})
	if e != nil || !queued {
		t.Fatalf("paid bot analysis not queued: %v", e)
	}
	var delivered bool
	for i := 0; i < 100; i++ {
		var status string
		if p.DB.QueryRow(`SELECT status FROM request_states WHERE id=?`, requestID).Scan(&status) == nil && status == "delivered" {
			delivered = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !delivered || transport.sent.Load() < 2 {
		t.Fatalf("paid bot report not delivered: delivered=%v calls=%d", delivered, transport.sent.Load())
	}
	cancel()
	worker.Stop()
	if _, e := b.ScanOnce(context.Background()); e != nil {
		t.Fatal(e)
	}
	_, after, e := p.WebSubscription(telegramID)
	if e != nil || !before.Equal(after) {
		t.Fatalf("duplicate renewal: %v to %v, %v", before, after, e)
	}
	if e := p.Reserve("expired-free", telegramID, after.Add(time.Second)); e != nil {
		t.Fatalf("free quota after expiry: %v", e)
	}
	if e := p.SetUse("expired-free", "consumed"); e != nil {
		t.Fatal(e)
	}
	if e := p.Reserve("expired", telegramID, after.Add(time.Second)); !errors.Is(e, profile.ErrQuota) {
		t.Fatalf("expired subscription bypass: %v", e)
	}
}

func TestWebLinkRequiresLoginAndConfirmation(t *testing.T) {
	p, web := testStores(t)
	defer p.DB.Close()
	const telegramID int64 = 75002
	_, e := p.Ensure(telegramID)
	if e != nil {
		t.Fatal(e)
	}
	state, e := p.IssueTelegramLoginNonce(time.Now())
	if e != nil {
		t.Fatal(e)
	}
	webID, e := p.TelegramLoginOnce(state, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", telegramID, "Confirm", "confirm", time.Now())
	if e != nil {
		t.Fatal(e)
	}
	session, e := web.CreateSessionForUser(webID, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	token, e := p.IssueWebLink(telegramID, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	h := (&Handler{Store: web, Profile: p}).Routes()
	r := httptest.NewRequest("GET", "/app/link?token="+url.QueryEscape(token), nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 303 {
		t.Fatalf("unauthenticated link status %d", w.Code)
	}
	r = httptest.NewRequest("GET", "/app/link?token="+url.QueryEscape(token), nil)
	r.AddCookie(&http.Cookie{Name: "mr_web", Value: session})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "تأیید اتصال این حساب") {
		t.Fatalf("confirmation status %d", w.Code)
	}
	var used int
	if e = p.DB.QueryRow(`SELECT used FROM web_link_tokens WHERE telegram_id=? ORDER BY rowid DESC LIMIT 1`, telegramID).Scan(&used); e != nil || used != 0 {
		t.Fatal("GET consumed the confirmation token")
	}
	form := url.Values{"token": {token}}
	r = httptest.NewRequest("POST", "/app/link", strings.NewReader(form.Encode()))
	r.Host = "example.test"
	r.Header.Set("Origin", "http://example.test")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: "mr_web", Value: session})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 303 {
		t.Fatalf("confirmed link status %d: %s", w.Code, w.Body.String())
	}
	linked, _, e := p.WebSubscription(telegramID)
	if e != nil || !linked {
		t.Fatal("confirmed account not linked")
	}
}

func TestUserPanelShowsInvoiceFieldsAndDisabledBilling(t *testing.T) {
	p, web := testStores(t)
	defer p.DB.Close()
	const telegramID int64 = 75003
	_, e := p.Ensure(telegramID)
	if e != nil {
		t.Fatal(e)
	}
	state, e := p.IssueTelegramLoginNonce(time.Now())
	if e != nil {
		t.Fatal(e)
	}
	webID, e := p.TelegramLoginOnce(state, "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210", telegramID, "Panel", "panel", time.Now())
	if e != nil {
		t.Fatal(e)
	}
	session, e := web.CreateSessionForUser(webID, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	h := (&Handler{Store: web, Profile: p, Billing: &Billing{Store: web}}).Routes()
	r := httptest.NewRequest("GET", "/app/", nil)
	r.AddCookie(&http.Cookie{Name: "mr_web", Value: session})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	for _, text := range []string{"آدرس اختصاصی", "مبلغ دقیق", "شبکه", "مهلت", "وضعیت", "کپی آدرس", "کپی مبلغ"} {
		if !strings.Contains(w.Body.String(), text) {
			t.Fatalf("missing %s", text)
		}
	}
	r = httptest.NewRequest("GET", "/app/api/me", nil)
	r.AddCookie(&http.Cookie{Name: "mr_web", Value: session})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), `"invoice_available":false`) {
		t.Fatal("public billing not disabled")
	}
}
