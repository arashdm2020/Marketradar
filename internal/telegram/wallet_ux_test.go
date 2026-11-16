package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"tradebot/internal/custody"
	"tradebot/internal/domain"
	"tradebot/internal/engine"
	"tradebot/internal/profile"
)

type uxTransport struct {
	bodies   []string
	paths    []string
	editFail string
}

func (u *uxTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(r.Body)
	u.bodies = append(u.bodies, string(body))
	u.paths = append(u.paths, r.URL.Path)
	result := `{"ok":true,"result":{"message_id":71}}`
	if strings.HasSuffix(r.URL.Path, "/editMessageText") && u.editFail != "" {
		result = `{"ok":false,"description":"` + u.editFail + `"}`
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(result)), Header: make(http.Header)}, nil
}

func telegramUpdate(t *testing.T, raw string) update {
	t.Helper()
	var v update
	if e := json.NewDecoder(bytes.NewBufferString(raw)).Decode(&v); e != nil {
		t.Fatal(e)
	}
	return v
}

func walletCount(t *testing.T, s *profile.Store, id int64) int {
	t.Helper()
	var n int
	if e := s.DB.QueryRow(`SELECT count(*) FROM telegram_wallets WHERE telegram_id=?`, id).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}

func TestStartAndMenuNeverCreateWallet(t *testing.T) {
	s, e := profile.Open(t.TempDir() + "/ux.db")
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	transport := &uxTransport{}
	b := Bot{Store: s, Token: "test", HTTP: &http.Client{Transport: transport}}
	b.handle(context.Background(), telegramUpdate(t, `{"message":{"text":"/start","from":{"id":501},"chat":{"id":501,"type":"private"}}}`))
	b.handle(context.Background(), telegramUpdate(t, `{"callback_query":{"id":"c1","data":"wallet","from":{"id":501},"message":{"message_id":71,"chat":{"id":501,"type":"private"}}}}`))
	b.handle(context.Background(), telegramUpdate(t, `{"callback_query":{"id":"c2","data":"subscription","from":{"id":501},"message":{"message_id":71,"chat":{"id":501,"type":"private"}}}}`))
	if n := walletCount(t, s, 501); n != 0 {
		t.Fatalf("navigation created %d wallets", n)
	}
}

func TestRepeatedStartAlwaysSendsFreshMenuAndNeverEdits(t *testing.T) {
	s, e := profile.Open(t.TempDir() + "/fresh-menu.db")
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	if _, e = s.Ensure(504); e != nil {
		t.Fatal(e)
	}
	if e = s.SetDashboardMessage(504, 41); e != nil {
		t.Fatal(e)
	}
	transport := &uxTransport{}
	b := Bot{Store: s, Token: "test", HTTP: &http.Client{Transport: transport}}
	start := telegramUpdate(t, `{"message":{"text":"/start","from":{"id":504},"chat":{"id":504,"type":"private"}}}`)
	b.handle(context.Background(), start)
	b.handle(context.Background(), start)

	var sends, edits int
	for _, p := range transport.paths {
		if strings.HasSuffix(p, "/sendMessage") {
			sends++
		}
		if strings.HasSuffix(p, "/editMessageText") || strings.HasSuffix(p, "/editMessageReplyMarkup") {
			edits++
		}
	}
	if sends != 2 || edits != 0 {
		t.Fatalf("sendMessage=%d edits=%d; want one fresh send per /start and no edits", sends, edits)
	}
	joined := strings.Join(transport.bodies, "\n")
	if !strings.Contains(joined, "Visitor") || !strings.Contains(joined, "1 از 1") {
		t.Fatalf("menu does not show authoritative plan/quota: %s", joined)
	}
}

func TestMenuCallbackStillEditsOriginatingMessage(t *testing.T) {
	s, e := profile.Open(t.TempDir() + "/callback-menu.db")
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	transport := &uxTransport{}
	b := Bot{Store: s, Token: "test", HTTP: &http.Client{Transport: transport}}
	b.handle(context.Background(), telegramUpdate(t, `{"callback_query":{"id":"menu1","data":"menu","from":{"id":505},"message":{"message_id":87,"chat":{"id":505,"type":"private"}}}}`))

	if len(transport.paths) != 2 || !strings.HasSuffix(transport.paths[0], "/answerCallbackQuery") || !strings.HasSuffix(transport.paths[1], "/editMessageText") {
		t.Fatalf("callback methods=%v; want answerCallbackQuery then editMessageText", transport.paths)
	}
	if !strings.Contains(transport.bodies[1], `"message_id":87`) {
		t.Fatalf("callback edit did not target originating message: %s", transport.bodies[1])
	}
}

func TestHorizonKeyboardUsesCompatibilityPolicy(t *testing.T) {
	s, e := profile.Open(t.TempDir() + "/horizon-menu.db")
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	u, _ := s.Ensure(506)
	u.Completed = true
	u.State = "frame:XAU/USD"
	_ = s.Save(u)
	transport := &uxTransport{}
	b := Bot{Store: s, Token: "test", HTTP: &http.Client{Transport: transport}}
	b.handle(context.Background(), telegramUpdate(t, `{"callback_query":{"id":"frame1","data":"f:0","from":{"id":506},"message":{"message_id":90,"chat":{"id":506,"type":"private"}}}}`))
	joined := strings.Join(transport.bodies, "\n")
	if !strings.Contains(joined, `"callback_data":"y:scalp"`) || strings.Contains(joined, `"callback_data":"y:day"`) || strings.Contains(joined, `"callback_data":"y:swing"`) {
		t.Fatalf("M1 horizon keyboard violates policy: %s", joined)
	}
}

func TestStaleIncompatibleHorizonCallbackRejected(t *testing.T) {
	s, e := profile.Open(t.TempDir() + "/stale-horizon.db")
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	u, _ := s.Ensure(507)
	u.Completed = true
	u.State = "style:XAU/USD:1min"
	_ = s.Save(u)
	transport := &uxTransport{}
	b := Bot{Store: s, Token: "test", HTTP: &http.Client{Transport: transport}}
	b.handle(context.Background(), telegramUpdate(t, `{"callback_query":{"id":"h1","data":"y:swing","from":{"id":507},"message":{"message_id":91,"chat":{"id":507,"type":"private"}}}}`))
	u, _ = s.Get(507)
	if u.State != "style:XAU/USD:1min" {
		t.Fatalf("invalid callback changed state to %s", u.State)
	}
	if !strings.Contains(strings.Join(transport.bodies, "\n"), "سازگار نیست") {
		t.Fatal("clear invalid-selection message missing")
	}
}

func TestChangingTimeframeClearsSelectedHorizon(t *testing.T) {
	s, e := profile.Open(t.TempDir() + "/switch-frame.db")
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	u, _ := s.Ensure(508)
	u.Completed = true
	u.State = "riskreview:XAU/USD:5min:scalp"
	_ = s.Save(u)
	transport := &uxTransport{}
	b := Bot{Store: s, Token: "test", HTTP: &http.Client{Transport: transport}}
	b.handle(context.Background(), telegramUpdate(t, `{"callback_query":{"id":"f2","data":"f:3","from":{"id":508},"message":{"message_id":92,"chat":{"id":508,"type":"private"}}}}`))
	u, _ = s.Get(508)
	if u.State != "style:XAU/USD:30min" {
		t.Fatalf("old horizon was not invalidated: %s", u.State)
	}
	joined := strings.Join(transport.bodies, "\n")
	if !strings.Contains(joined, `"callback_data":"y:day"`) || strings.Contains(joined, `"callback_data":"y:scalp"`) {
		t.Fatalf("M30 options invalid: %s", joined)
	}
}

func TestExplicitWalletCreationIsIdempotentAndOffersCopy(t *testing.T) {
	s, e := profile.Open(t.TempDir() + "/wallet.db")
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	c, e := custody.New(s.DB)
	if e != nil {
		t.Fatal(e)
	}
	transport := &uxTransport{}
	b := Bot{Store: s, Custody: c, Token: "test", HTTP: &http.Client{Transport: transport}}
	event := telegramUpdate(t, `{"message":{"text":"/wallet_create","from":{"id":502},"chat":{"id":502,"type":"private"}}}`)
	b.handle(context.Background(), event)
	b.handle(context.Background(), event)
	if n := walletCount(t, s, 502); n != 1 {
		t.Fatalf("explicit creation made %d wallets", n)
	}
	address, e := s.Wallet(502)
	if e != nil {
		t.Fatal(e)
	}
	if has, e := c.HasKey(address); e != nil || !has {
		t.Fatalf("wallet key mismatch: has=%v err=%v", has, e)
	}
	if !strings.Contains(strings.Join(transport.bodies, "\n"), `"copy_text":{"text":"`+address+`"}`) {
		t.Fatal("copy_text button missing")
	}
	if !strings.Contains(strings.Join(transport.bodies, "\n"), emojiRegistry[EmojiBNB].ID) {
		t.Fatal("wallet template missing BNB custom emoji")
	}
}

func TestMessageNotModifiedDoesNotCreateDuplicate(t *testing.T) {
	s, e := profile.Open(t.TempDir() + "/edit.db")
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	_, _ = s.Ensure(503)
	_ = s.SetDashboardMessage(503, 44)
	transport := &uxTransport{editFail: "Bad Request: message is not modified"}
	b := Bot{Store: s, Token: "test", HTTP: &http.Client{Transport: transport}}
	if e = b.screen(context.Background(), 503, "بدون تغییر", nil); e != nil {
		t.Fatal(e)
	}
	if len(transport.bodies) != 1 {
		t.Fatalf("message duplicated; requests=%d", len(transport.bodies))
	}
}

func TestInvalidEditFallsBackToNewMessage(t *testing.T) {
	s, e := profile.Open(t.TempDir() + "/fallback.db")
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	_, _ = s.Ensure(504)
	_ = s.SetDashboardMessage(504, 45)
	transport := &uxTransport{editFail: "Bad Request: message to edit not found"}
	b := Bot{Store: s, Token: "test", HTTP: &http.Client{Transport: transport}}
	if e = b.screen(context.Background(), 504, "صفحه تازه", nil); e != nil {
		t.Fatal(e)
	}
	if len(transport.bodies) != 2 {
		t.Fatalf("expected edit then send fallback, requests=%d", len(transport.bodies))
	}
	var stored int64
	if e = s.DB.QueryRow(`SELECT message_id FROM telegram_ui_state WHERE telegram_id=504`).Scan(&stored); e != nil || stored != 71 {
		t.Fatalf("fallback message not persisted: %d %v", stored, e)
	}
}

func TestSendAndEditPreserveCustomEmojiHTML(t *testing.T) {
	transport := &uxTransport{}
	b := Bot{Token: "test", HTTP: &http.Client{Transport: transport}}
	msg := customEmoji(EmojiSuccess) + " <b>موفق</b>"
	if _, e := b.sendText(context.Background(), 900, msg, nil); e != nil {
		t.Fatal(e)
	}
	ctx := context.WithValue(context.Background(), editKey{}, int64(71))
	if _, e := b.sendText(ctx, 900, msg+"!", [][]map[string]any{{button("✅ ادامه", "menu")}}); e != nil {
		t.Fatal(e)
	}
	if len(transport.bodies) != 2 {
		t.Fatalf("requests=%d", len(transport.bodies))
	}
	for _, body := range transport.bodies {
		if !strings.Contains(body, `"parse_mode":"HTML"`) || !strings.Contains(body, emojiRegistry[EmojiSuccess].ID) {
			t.Fatalf("formatting lost: %s", body)
		}
		if strings.Contains(body, `&lt;tg-emoji`) {
			t.Fatal("emoji markup escaped")
		}
	}
}

func TestPaymentPaginationIsIsolatedByUser(t *testing.T) {
	s, e := profile.Open(t.TempDir() + "/payments.db")
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	_, _ = s.Ensure(601)
	_, _ = s.Ensure(602)
	for i := 0; i < 7; i++ {
		var inv profile.TelegramInvoice
		if inv, e = s.CreateTelegramInvoice(601, "0x1111111111111111111111111111111111111111", "1", 56, timeNowPlusHour()); e != nil {
			t.Fatal(e)
		}
		if i < 6 {
			_, e = s.DB.Exec(`UPDATE telegram_invoices SET status='expired' WHERE id=?`, inv.ID)
			if e != nil {
				t.Fatal(e)
			}
		}
	}
	if _, e = s.CreateTelegramInvoice(602, "0x2222222222222222222222222222222222222222", "2", 56, timeNowPlusHour()); e != nil {
		t.Fatal(e)
	}
	page, total, e := s.PageTelegramInvoices(601, 5, 0)
	if e != nil || len(page) != 5 || total != 7 {
		t.Fatalf("page=%d total=%d err=%v", len(page), total, e)
	}
	other, totalOther, e := s.PageTelegramInvoices(602, 5, 0)
	if e != nil || len(other) != 1 || totalOther != 1 || other[0].TelegramID != 602 {
		t.Fatal("payment history leaked across users")
	}
}

func TestPaymentTemplateUsesInvoiceEmoji(t *testing.T) {
	s, e := profile.Open(t.TempDir() + "/payment-template.db")
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	_, _ = s.Ensure(603)
	if _, e = s.CreateTelegramInvoice(603, "0x3333333333333333333333333333333333333333", "1000000000000000", 56, timeNowPlusHour()); e != nil {
		t.Fatal(e)
	}
	transport := &uxTransport{}
	b := Bot{Store: s, Token: "test", HTTP: &http.Client{Transport: transport}}
	b.paymentHistory(context.Background(), 603, 603, 0)
	joined := strings.Join(transport.bodies, "\n")
	if !strings.Contains(joined, emojiRegistry[EmojiInvoice].ID) || !strings.Contains(joined, "0.00100000 BNB") {
		t.Fatal("payment template missing semantic formatting")
	}
}

func TestRunRemovesConfirmationKeyboard(t *testing.T) {
	s, e := profile.Open(t.TempDir() + "/run.db")
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	u, _ := s.Ensure(701)
	u.Completed = true
	u.State = "confirm:XAU/USD:5min:scalp"
	u.MinProfit, u.MaxProfit, u.MaxRisk = .5, 2, 1
	if e = s.Save(u); e != nil {
		t.Fatal(e)
	}
	transport := &uxTransport{}
	worker := NewAsyncWorker(nil, s, &mockEngine{})
	b := Bot{Store: s, Worker: worker, Token: "test", HTTP: &http.Client{Transport: transport}}
	worker.SetBot(&b)
	b.handle(context.Background(), telegramUpdate(t, `{"callback_query":{"id":"run1","data":"run","from":{"id":701},"message":{"message_id":88,"chat":{"id":701,"type":"private"}}}}`))
	found := false
	for _, p := range transport.paths {
		if strings.HasSuffix(p, "/editMessageReplyMarkup") {
			found = true
		}
	}
	if !found {
		t.Fatal("confirmation keyboard was not removed")
	}
}

func TestSuccessfulAnalysisEditsOriginalMessageOnly(t *testing.T) {
	s, e := profile.Open(t.TempDir() + "/single-message.db")
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	_, _ = s.Ensure(702)
	requestID := profile.NewID()
	if e = s.CreateRequestState(requestID, "702", "702", "XAU/USD", "5min", "scalp"); e != nil {
		t.Fatal(e)
	}
	transport := &uxTransport{}
	b := Bot{Store: s, Token: "test", HTTP: &http.Client{Transport: transport}}
	worker := NewAsyncWorker(&b, s, &mockEngine{})
	worker.process(context.Background(), AnalysisRequest{ID: requestID, UserID: 702, ChatID: 702, MessageID: 99, Symbol: "XAU/USD", Timeframe: "5min", Style: "scalp", MaxRisk: 1})
	var edits, sends int
	for _, p := range transport.paths {
		if strings.HasSuffix(p, "/editMessageText") {
			edits++
		}
		if strings.HasSuffix(p, "/sendMessage") {
			sends++
		}
	}
	if edits != 2 || sends != 0 {
		t.Fatalf("edits=%d sends=%d; want progress+result edits and no new message", edits, sends)
	}
	if !strings.Contains(transport.bodies[len(transport.bodies)-1], requestID) {
		t.Fatal("final edit lost tracking id")
	}
}

func TestFailedAnalysisEditsOriginalWithRetry(t *testing.T) {
	s, e := profile.Open(t.TempDir() + "/single-error.db")
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	_, _ = s.Ensure(703)
	requestID := profile.NewID()
	if e = s.CreateRequestState(requestID, "703", "703", "XAU/USD", "5min", "day"); e != nil {
		t.Fatal(e)
	}
	transport := &uxTransport{}
	b := Bot{Store: s, Token: "test", HTTP: &http.Client{Transport: transport}}
	engine := &mockEngine{analyzeFn: func(context.Context, engine.Request) (domain.Decision, error) {
		return domain.Decision{}, errors.New("provider unavailable")
	}}
	worker := NewAsyncWorker(&b, s, engine)
	worker.process(context.Background(), AnalysisRequest{ID: requestID, UserID: 703, ChatID: 703, MessageID: 100, Symbol: "XAU/USD", Timeframe: "5min", Style: "day"})
	for _, p := range transport.paths {
		if strings.HasSuffix(p, "/sendMessage") {
			t.Fatal("failure created a second message")
		}
	}
	last := transport.bodies[len(transport.bodies)-1]
	if !strings.Contains(last, "تلاش دوباره") || !strings.Contains(last, "analysis") && !strings.Contains(last, "analyze") {
		t.Fatal("retry action missing")
	}
}

func timeNowPlusHour() time.Time { return time.Now().Add(time.Hour) }
