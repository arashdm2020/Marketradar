package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"tradebot/internal/bnb"
	"tradebot/internal/catalog"
	"tradebot/internal/condition"
	"tradebot/internal/custody"
	"tradebot/internal/domain"
	"tradebot/internal/engine"
	"tradebot/internal/entitlement"
	"tradebot/internal/marketdata"
	"tradebot/internal/profile"
)

type Bot struct {
	Token          string
	PublicAdminURL string
	HTTP           *http.Client
	Store          *profile.Store
	PlanPrice      func() (*big.Int, error)
	Engine         engine.Engine
	Deriver        *bnb.Deriver
	Custody        *custody.Store
	Allowed        map[int64]bool
	Worker         *AsyncWorker
	Chain          bnb.Chain
}
type update struct {
	UpdateID    int64 `json:"update_id"`
	PreCheckout *struct {
		ID   string `json:"id"`
		From struct {
			ID int64 `json:"id"`
		} `json:"from"`
		Currency       string `json:"currency"`
		TotalAmount    int    `json:"total_amount"`
		InvoicePayload string `json:"invoice_payload"`
	} `json:"pre_checkout_query"`
	Message *struct {
		Text              string `json:"text"`
		SuccessfulPayment *struct {
			Currency         string `json:"currency"`
			TotalAmount      int    `json:"total_amount"`
			InvoicePayload   string `json:"invoice_payload"`
			TelegramChargeID string `json:"telegram_payment_charge_id"`
		} `json:"successful_payment"`
		Chat struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		} `json:"chat"`
		From struct {
			ID int64 `json:"id"`
		} `json:"from"`
	} `json:"message"`
	Callback *struct {
		ID, Data string
		From     struct {
			ID int64 `json:"id"`
		} `json:"from"`
		Message struct {
			MessageID int64 `json:"message_id"`
			Chat      struct {
				ID   int64  `json:"id"`
				Type string `json:"type"`
			} `json:"chat"`
		} `json:"message"`
	} `json:"callback_query"`
}
type editKey struct{}
type APIError struct{ Method, Description string }

func (e *APIError) Error() string        { return fmt.Sprintf("Telegram %s: %s", e.Method, e.Description) }
func IsDefinitiveRejection(e error) bool { var api *APIError; return errors.As(e, &api) }

func (b Bot) call(ctx context.Context, method string, v any, out any) error {
	raw, _ := json.Marshal(v)
	req, e := http.NewRequestWithContext(ctx, "POST", "https://api.telegram.org/bot"+b.Token+"/"+method, bytes.NewReader(raw))
	if e != nil {
		return fmt.Errorf("Telegram request setup failed")
	}
	req.Header.Set("Content-Type", "application/json")
	h := b.HTTP
	if h == nil {
		h = &http.Client{Timeout: 35 * time.Second}
	}
	resp, e := h.Do(req)
	if e != nil {
		return fmt.Errorf("Telegram transport failure")
	}
	defer resp.Body.Close()
	var wire struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if e = json.NewDecoder(resp.Body).Decode(&wire); e != nil {
		return e
	}
	if !wire.OK {
		return &APIError{Method: method, Description: wire.Description}
	}
	if out != nil {
		return json.Unmarshal(wire.Result, out)
	}
	return nil
}
func (b Bot) sendText(ctx context.Context, id int64, msg string, buttons [][]map[string]any) (int64, error) {
	return b.sendWithResult(ctx, id, msg, buttons, false)
}
func (b Bot) send(ctx context.Context, id int64, msg string, buttons [][]map[string]any) error {
	_, e := b.sendWithResult(ctx, id, msg, buttons, false)
	return e
}
func (b Bot) sendWithResult(ctx context.Context, id int64, msg string, buttons [][]map[string]any, forceNew bool) (int64, error) {
	msg = splitMessage(msg)
	v := map[string]any{"chat_id": id, "text": msg, "parse_mode": "HTML"}
	if len(buttons) > 0 {
		v["reply_markup"] = map[string]any{"inline_keyboard": buttons}
	}

	if !forceNew {
		if m, ok := ctx.Value(editKey{}).(int64); ok && m > 0 {
			v["message_id"] = m
			if e := b.call(ctx, "editMessageText", v, nil); e == nil {
				return m, nil
			} else if isMessageNotModified(e) {
				return m, nil
			}
			delete(v, "message_id")
		}
	}
	var result struct {
		MessageID int64 `json:"message_id"`
	}
	if e := b.call(ctx, "sendMessage", v, &result); e != nil {
		return 0, e
	}
	return result.MessageID, nil
}
func (b Bot) screen(ctx context.Context, id int64, msg string, buttons [][]map[string]any) error {
	if _, ok := ctx.Value(editKey{}).(int64); !ok && b.Store != nil {
		if messageID, e := b.Store.DashboardMessage(id); e == nil && messageID > 0 {
			ctx = context.WithValue(ctx, editKey{}, messageID)
		}
	}
	messageID, e := b.sendWithResult(ctx, id, msg, buttons, false)
	if e == nil && b.Store != nil && messageID > 0 {
		_ = b.Store.SetDashboardMessage(id, messageID)
	}
	return e
}
func isMessageNotModified(e error) bool {
	var api *APIError
	return errors.As(e, &api) && strings.Contains(strings.ToLower(api.Description), "message is not modified")
}
func button(label, data string) map[string]any {
	return map[string]any{"text": label, "callback_data": data}
}
func copyButton(label, value string) map[string]any {
	return map[string]any{"text": label, "copy_text": map[string]string{"text": value}}
}
func (b Bot) keyButton(label, data, kind string) map[string]any {
	_ = kind
	return button(label, data)
}
func webURL(path string) string {
	base := strings.TrimRight(os.Getenv("WEB_PUBLIC_BASE_URL"), "/")
	u, e := url.Parse(base)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	return base + path
}
func urlButton(label, target string) map[string]any {
	return map[string]any{"text": label, "url": target}
}
func (b Bot) Run(ctx context.Context) error {
	if b.Token == "" {
		return fmt.Errorf("Telegram token missing")
	}
	if b.Worker == nil {
		return fmt.Errorf("analysis worker not initialized")
	}
	b.Worker.Start(ctx)
	defer b.Worker.Stop()
	var offset int64
	var lastPollWarning time.Time
	pollHealthy := false
	go b.monitor(ctx)
	go b.runPaymentNotifications(ctx)
	for ctx.Err() == nil {
		var list []update
		e := b.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 25, "allowed_updates": []string{"message", "callback_query", "pre_checkout_query"}}, &list)
		if e != nil {
			pollHealthy = false
			if time.Since(lastPollWarning) > 30*time.Second {
				slog.Warn("telegram_poll_failed", "error", e.Error())
				lastPollWarning = time.Now()
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(2 * time.Second):
				continue
			}
		}
		if !pollHealthy {
			slog.Info("telegram_poll_connected")
			var identity struct {
				ID       int64  `json:"id"`
				Username string `json:"username"`
			}
			if e := b.call(ctx, "getMe", map[string]any{}, &identity); e == nil {
				slog.Info("telegram_identity", "bot_id", identity.ID, "username", identity.Username)
			}
			pollHealthy = true
		}
		for _, u := range list {
			offset = u.UpdateID + 1
			b.handle(ctx, u)
		}
	}
	return nil
}
func (b Bot) handle(ctx context.Context, x update) {
	if x.PreCheckout != nil {
		_ = b.call(ctx, "answerPreCheckoutQuery", map[string]any{"pre_checkout_query_id": x.PreCheckout.ID, "ok": false, "error_message": "فروش در ربات غیرفعال است."}, nil)
		return
	}
	var id, chat int64
	var text, callback string
	if x.Message != nil {
		id = x.Message.From.ID
		chat = x.Message.Chat.ID
		text = x.Message.Text
		if x.Message.Chat.Type != "private" {
			return
		}
		if x.Message.SuccessfulPayment != nil {
			_ = b.send(ctx, chat, customEmoji(EmojiAttention)+" فروش در ربات غیرفعال است. در صورت کسر وجه، با پشتیبانی تماس بگیرید.", nil)
			return
		}
	}
	if x.Callback != nil {
		ctx = context.WithValue(ctx, editKey{}, x.Callback.Message.MessageID)
		id = x.Callback.From.ID
		chat = x.Callback.Message.Chat.ID
		callback = x.Callback.Data
		if x.Callback.Message.Chat.Type != "private" {
			return
		}
		_ = b.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": x.Callback.ID}, nil)
	}
	if id == 0 || chat == 0 || len(b.Allowed) > 0 && !b.Allowed[id] && id != profile.AdminID {
		return
	}
	u, e := b.Store.Ensure(id)
	if e != nil {
		return
	}
	if text == "/admin" {
		if id != profile.AdminID {
			return
		}
		code, e := b.Store.IssueAdminCode(id)
		if e == nil {
			adminURL := strings.TrimSpace(b.PublicAdminURL)
			if adminURL == "" {
				adminURL = "http://127.0.0.1:3000/login"
			}
			_ = b.send(ctx, chat, "کد ورود پنل (اعتبار ۵ دقیقه): <code>"+code+"</code>\nآدرس پنل: "+html.EscapeString(adminURL), nil)
		}
		return
	}
	if text == "/delete_data" {
		b.send(ctx, chat, customEmoji(EmojiAttention)+" آیا داده‌های حساب شما حذف شود؟", [][]map[string]any{{button("بله، حذف شود", "delete_yes"), button("انصراف", "cancel")}})
		return
	}
	if callback == "alert:0" {
		raw, e := b.Store.Latest(id)
		if e != nil {
			return
		}
		var d domain.Decision
		if json.Unmarshal([]byte(raw), &d) != nil || len(d.ValidatedConditional) == 0 {
			return
		}
		s := d.ValidatedConditional[0]
		exp, _ := time.Parse(time.RFC3339, s.Expiration)
		if e = b.Store.AddAlert(profile.Alert{UserID: id, Symbol: d.Snapshot.Symbol, Timeframe: frameFromLabel(d.Snapshot.Timeframe), Kind: s.TriggerKind, Price: s.TriggerPrice, Entry: s.Entry, After: d.Snapshot.LastCandleTimeUTC, Expires: exp}); e == nil {
			b.send(ctx, chat, customEmoji(EmojiSuccess)+" هشدار ثبت شد؛ فقط پس از بسته‌شدن کندل و تأیید شرط اطلاع می‌دهم.", nil)
		}
		return
	}
	if callback == "delete_yes" {
		b.Store.Delete(id)
		b.send(ctx, chat, customEmoji(EmojiSuccess)+" داده‌های شما حذف شد.", nil)
		return
	}
	if text == "/cancel" || callback == "cancel" {
		u.State = "menu"
		b.Store.Save(u)
		b.menu(ctx, chat)
		return
	}
	if callback == "back" {
		if strings.HasPrefix(u.State, "style:") {
			parts := strings.Split(strings.TrimPrefix(u.State, "style:"), ":")
			if len(parts) == 2 {
				u.State = "frame:" + parts[0]
				b.Store.Save(u)
				b.frames(ctx, chat)
				return
			}
		}
		switch u.State {
		case "min":
			u.State = "markets"
		case "max":
			u.State = "min"
		case "risk":
			u.State = "max"
		case "settings:min", "settings:max", "settings:risk", "settings:markets":
			u.State = "settings"
		default:
			u.State = "menu"
		}
		b.Store.Save(u)
		if !u.Completed {
			b.onboard(ctx, chat, u, "", "")
		} else if strings.HasPrefix(u.State, "settings:") || u.State == "settings" {
			b.settings(ctx, chat, u)
		} else {
			b.menu(ctx, chat)
		}
		return
	}
	if text == "/start" {
		b.freshMenu(ctx, chat)
		return
	}
	if callback == "menu" {
		b.menu(ctx, chat)
		return
	}
	if !u.Completed && !dashboardAction(text, callback) {
		b.onboard(ctx, chat, u, text, callback)
		return
	}
	if text == "/help" || callback == "help" {
		b.help(ctx, chat)
		return
	}
	if text == "/history" {
		h, _ := b.Store.History(id)
		if len(h) == 0 {
			b.send(ctx, chat, customEmoji(EmojiAttention)+" هنوز تحلیلی ثبت نشده است.", nil)
		} else {
			b.send(ctx, chat, "تاریخچه تحلیل‌ها:\n"+html.EscapeString(strings.Join(h, "\n")), nil)
		}
		return
	}
	if text == "/settings" || callback == "settings" {
		b.settings(ctx, chat, u)
		return
	}
	if text == "/wallet" || callback == "wallet" || callback == "wallet:refresh" {
		b.walletDashboard(ctx, chat, id)
		return
	}
	if text == "/wallet_create" || callback == "wallet:create" {
		b.createWallet(ctx, chat, id)
		return
	}
	if text == "/payments" || callback == "payments" || strings.HasPrefix(callback, "payments:") {
		page := 0
		if strings.HasPrefix(callback, "payments:") {
			page, _ = strconv.Atoi(strings.TrimPrefix(callback, "payments:"))
		}
		b.paymentHistory(ctx, chat, id, page)
		return
	}
	if callback == "subscription" {
		b.subscriptionDashboard(ctx, chat)
		return
	}
	if text == "/markets" || callback == "markets" {
		u.State = "settings:markets"
		b.Store.Save(u)
		b.symbolsSettings(ctx, chat, u)
		return
	}
	if strings.HasPrefix(callback, "set:m:") {
		i, _ := strconv.Atoi(strings.TrimPrefix(callback, "set:m:"))
		if i < 0 || i >= len(catalog.Instruments) {
			return
		}
		u.Markets = catalog.Instruments[i].Symbol
		u.State = "settings"
		b.Store.Save(u)
		b.settings(ctx, chat, u)
		return
	}
	if callback == "set:min" {
		u.State = "settings:min"
		b.Store.Save(u)
		b.send(ctx, chat, "حداقل سود مورد انتظار چند درصد از ارزش ورود است؟ عدد بنویسید (مثلاً ۰٫۵).", [][]map[string]any{{button("بازگشت", "settings"), button("انصراف", "cancel")}})
		return
	}
	if callback == "set:max" {
		u.State = "settings:max"
		b.Store.Save(u)
		b.send(ctx, chat, "حداکثر سود مورد انتظار چند درصد از ارزش ورود است؟ عدد بنویسید (مثلاً ۲).", [][]map[string]any{{button("بازگشت", "settings"), button("انصراف", "cancel")}})
		return
	}
	if callback == "set:risk" {
		u.State = "settings:risk"
		b.Store.Save(u)
		b.send(ctx, chat, "حداکثر ریسک هر معامله چند درصد از سرمایه است؟ عدد بین ۰ و ۱۰ بنویسید.", [][]map[string]any{{button("بازگشت", "settings"), button("انصراف", "cancel")}})
		return
	}
	if text != "" && strings.HasPrefix(u.State, "settings:") {
		b.handleSettingsText(ctx, chat, u, text)
		return
	}
	if text == "/analyze" || callback == "analyze" {
		u.State = "choose_symbol"
		b.Store.Save(u)
		b.symbolsPage(ctx, chat, 0)
		return
	}
	if strings.HasPrefix(callback, "symbols:") {
		page, _ := strconv.Atoi(strings.TrimPrefix(callback, "symbols:"))
		b.symbolsPage(ctx, chat, page)
		return
	}
	if callback == "link" {
		target := webURL("/app/link?token=")
		if target == "" {
			_ = b.send(ctx, chat, customEmoji(EmojiAttention)+" آدرس امن وب هنوز تنظیم نشده است. اتصال حساب فعلاً در دسترس نیست.", nil)
			return
		}
		token, e := b.Store.IssueWebLink(id, time.Now())
		if e != nil {
			_ = b.send(ctx, chat, customEmoji(EmojiFailure)+" صدور پیوند اتصال ناموفق بود. دوباره تلاش کنید.", nil)
			return
		}
		_ = b.send(ctx, chat, "برای اتصال حساب، در وب وارد شوید و اتصال را تأیید کنید. اعتبار پیوند: ۱۰ دقیقه.", [][]map[string]any{{urlButton("اتصال حساب وب", target+url.QueryEscape(token))}})
		return
	}
	if callback == "check_payment" {
		b.paymentHistory(ctx, chat, id, 0)
		return
	}
	if callback == "check_subscription" {
		access, e := b.Store.Access(id, time.Now())
		if e != nil {
			_ = b.send(ctx, chat, customEmoji(EmojiFailure)+" بررسی اشتراک فعلاً ناموفق بود.", nil)
			return
		}
		if access.SubscriptionActive {
			until, _ := time.Parse(time.RFC3339, access.SubscriptionUntil)
			_ = b.send(ctx, chat, customEmoji(EmojiSuccess)+" طرح <b>"+access.Policy.Label+"</b> شما تا <b>"+until.In(time.FixedZone("تهران", 12600)).Format("2006-01-02 15:04")+"</b> فعال است.", [][]map[string]any{{b.keyButton("🔄 تحلیل جدید", "analyze", "ANALYZE")}})
		} else {
			_ = b.send(ctx, chat, customEmoji(EmojiExpiration)+" اشتراک فعال ندارید یا اعتبار آن به پایان رسیده است.", [][]map[string]any{{b.keyButton("خرید اشتراک", "buy", "SUBSCRIPTION")}})
		}
		return
	}
	if callback == "buy" {
		b.checkout(ctx, chat, id)
		return
	}
	if callback == "my_invoices" {
		b.myInvoices(ctx, chat, id)
		return
	}
	if callback == "buy:stars" {
		b.send(ctx, chat, customEmoji(EmojiAttention)+" فروش با Telegram Stars غیرفعال است.", nil)
		return
	}
	if strings.HasPrefix(callback, "s:") {
		i, _ := strconv.Atoi(strings.TrimPrefix(callback, "s:"))
		if i < 0 || i >= len(catalog.Instruments) {
			return
		}
		u.State = "frame:" + catalog.Instruments[i].Symbol
		b.Store.Save(u)
		b.frames(ctx, chat)
		return
	}
	if strings.HasPrefix(callback, "f:") {
		symbol := ""
		switch {
		case strings.HasPrefix(u.State, "frame:"):
			symbol = strings.TrimPrefix(u.State, "frame:")
		case strings.HasPrefix(u.State, "style:"):
			parts := strings.Split(strings.TrimPrefix(u.State, "style:"), ":")
			if len(parts) == 2 {
				symbol = parts[0]
			}
		case strings.HasPrefix(u.State, "riskreview:"):
			parts := strings.Split(strings.TrimPrefix(u.State, "riskreview:"), ":")
			if len(parts) == 3 {
				symbol = parts[0]
			}
		case strings.HasPrefix(u.State, "confirm:"):
			parts := strings.Split(strings.TrimPrefix(u.State, "confirm:"), ":")
			if len(parts) == 3 {
				symbol = parts[0]
			}
		}
		if symbol == "" {
			b.send(ctx, chat, customEmoji(EmojiAttention)+" این انتخاب منقضی شده است. لطفاً تایم‌فریم را دوباره انتخاب کنید.", [][]map[string]any{{button("بازگشت به تایم‌فریم‌ها", "analyze"), button("🏠 منوی اصلی", "menu")}})
			return
		}
		i, _ := strconv.Atoi(strings.TrimPrefix(callback, "f:"))
		if i < 0 || i >= len(catalog.Frames) {
			b.send(ctx, chat, customEmoji(EmojiAttention)+" تایم‌فریم انتخاب‌شده معتبر نیست.", [][]map[string]any{{button("انتخاب دوباره", "analyze")}})
			return
		}
		frame := catalog.Frames[i]
		// Selecting a new timeframe always clears a previously selected horizon.
		u.State = "style:" + symbol + ":" + string(frame)
		b.Store.Save(u)
		b.horizons(ctx, chat, frame)
		return
	}
	if strings.HasPrefix(callback, "y:") {
		parts := strings.Split(strings.TrimPrefix(u.State, "style:"), ":")
		horizon, he := catalog.ParseHorizon(strings.TrimPrefix(callback, "y:"))
		if !strings.HasPrefix(u.State, "style:") || len(parts) != 2 || he != nil || catalog.ValidateCombination(catalog.Timeframe(parts[1]), string(horizon)) != nil {
			b.send(ctx, chat, customEmoji(EmojiAttention)+" این افق معاملاتی با تایم‌فریم انتخاب‌شده سازگار نیست. لطفاً انتخاب معتبر انجام دهید.", [][]map[string]any{{button("انتخاب دوباره", "analyze"), button("🏠 منوی اصلی", "menu")}})
			return
		}
		access, ae := b.Store.Access(id, time.Now())
		if ae != nil || !access.Policy.Allows(parts[0], catalog.Timeframe(parts[1]), horizon) {
			b.send(ctx, chat, customEmoji(EmojiAttention)+" این ترکیب در طرح فعلی شما در دسترس نیست.", [][]map[string]any{{button("انتخاب دوباره", "analyze")}})
			return
		}
		u.State = "riskreview:" + strings.TrimPrefix(u.State, "style:") + ":" + string(horizon)
		b.Store.Save(u)
		b.send(ctx, chat, "<b>"+customEmoji(EmojiRisk)+" تنظیم ریسک</b>\n\n"+fmt.Sprintf("حداقل سود: <b>%.2f%%</b>\nحداکثر سود: <b>%.2f%%</b>\nسقف ریسک: <b>%.2f%%</b>", u.MinProfit, u.MaxProfit, u.MaxRisk), [][]map[string]any{{button("تأیید و ادامه", "risk:ok"), button("⚙️ ویرایش تنظیمات", "settings")}, {button("🏠 منوی اصلی", "menu")}})
		return
	}
	if callback == "risk:ok" && strings.HasPrefix(u.State, "riskreview:") {
		u.State = "confirm:" + strings.TrimPrefix(u.State, "riskreview:")
		b.Store.Save(u)
		b.send(ctx, chat, "<b>"+customEmoji(EmojiSuccess)+" تأیید نهایی تحلیل</b>\n\n"+html.EscapeString(selectionLabel(partsFromState(u.State)))+fmt.Sprintf("\n\nسود مورد انتظار: %.2f تا %.2f درصد\nسقف ریسک: %.2f درصد سرمایه", u.MinProfit, u.MaxProfit, u.MaxRisk), [][]map[string]any{{button("🚀 شروع تحلیل", "run"), button("لغو", "cancel")}, {button("🏠 منوی اصلی", "menu")}})
		return
	}
	if callback == "run" && strings.HasPrefix(u.State, "confirm:") {
		parts := strings.Split(strings.TrimPrefix(u.State, "confirm:"), ":")
		if len(parts) != 3 {
			return
		}
		if b.Worker.IsInflight(id) {
			b.send(ctx, chat, customEmoji(EmojiWait)+" تحلیل قبلی شما هنوز در حال پردازش است. لطفاً صبر کنید.", nil)
			return
		}
		if messageID, ok := ctx.Value(editKey{}).(int64); ok && messageID > 0 {
			_ = b.call(ctx, "editMessageReplyMarkup", map[string]any{
				"chat_id": chat, "message_id": messageID,
				"reply_markup": map[string]any{"inline_keyboard": [][]map[string]any{}},
			}, nil)
		}
		u.State = "running"
		b.Store.Save(u)
		req := AnalysisRequest{
			ID:        profile.NewID(),
			UserID:    id,
			ChatID:    chat,
			Symbol:    parts[0],
			Timeframe: catalog.Timeframe(parts[1]),
			Style:     parts[2],
			MinProfit: u.MinProfit,
			MaxProfit: u.MaxProfit,
			MaxRisk:   u.MaxRisk,
		}
		if messageID, ok := ctx.Value(editKey{}).(int64); ok && messageID > 0 {
			req.MessageID = messageID
		}
		cfg, ce := b.Store.ActiveConfig()
		if ce != nil {
			b.send(ctx, chat, customEmoji(EmojiFailure)+" تنظیمات تحلیل در دسترس نیست. دوباره تلاش کنید.", nil)
			return
		}
		req.ConfigVersion = cfg.Version
		req.ModelCandles = cfg.ModelCandles
		req.EngineCandles = cfg.EngineCandles
		req.HorizonCandles = cfg.HorizonCandles
		req.MaxDurationSeconds = cfg.MaxDurationSeconds
		req.ResponseTokenBudget = cfg.ResponseTokenBudget
		if _, e := b.Worker.Enqueue(ctx, req); e != nil {
			u.State = "menu"
			b.Store.Save(u)
			if e == profile.ErrQuota {
				access, _ := b.Store.Access(id, time.Now())
				b.send(ctx, chat, customEmoji(EmojiExpiration)+" سهمیهٔ روزانهٔ طرح <b>"+access.Policy.Label+"</b> مصرف شده است. سهمیه در ابتدای روز تقویمی تهران بازنشانی می‌شود.", [][]map[string]any{{b.keyButton("بررسی اشتراک", "check_subscription", "PAYMENT"), b.keyButton("خرید اشتراک", "buy", "SUBSCRIPTION")}})
			} else {
				b.send(ctx, chat, customEmoji(EmojiFailure)+" خطا در ثبت درخواست تحلیل: "+html.EscapeString(e.Error()), nil)
			}
			return
		}
	}
}
func dashboardAction(text, callback string) bool {
	if text == "/help" || text == "/wallet" || text == "/wallet_create" || text == "/payments" {
		return true
	}
	return callback == "help" || callback == "wallet" || callback == "wallet:refresh" || callback == "wallet:create" || callback == "payments" || strings.HasPrefix(callback, "payments:") || callback == "subscription" || callback == "buy" || callback == "my_invoices" || callback == "check_subscription" || callback == "check_payment"
}
func (b Bot) menu(ctx context.Context, id int64) {
	b.renderMenu(ctx, id, false)
}

// freshMenu deliberately bypasses the stored dashboard message. An explicit
// /start is a request for a new conversation entry, while callback navigation
// should continue editing the message that originated the callback.
func (b Bot) freshMenu(ctx context.Context, id int64) {
	b.renderMenu(ctx, id, true)
}

func (b Bot) renderMenu(ctx context.Context, id int64, forceNew bool) {
	access, _ := b.Store.Access(id, time.Now())
	status := access.Policy.Label
	quota := fmt.Sprintf("%d از %d باقی‌مانده", access.Remaining, access.Allowance)
	if access.Allowance < 0 {
		quota = "نامحدود"
	}
	wallet := "ساخته نشده"
	if _, e := b.Store.Wallet(id); e == nil {
		wallet = "آماده"
	}
	msg := "<b>" + customEmoji(EmojiMarketAnalysis) + " مارکت‌رادار</b>\nدستیار تحلیل و اشتراک بازارهای مالی\n\n" +
		customEmoji(EmojiSuccess) + " طرح دسترسی: <b>" + html.EscapeString(status) + "</b>\n" +
		customEmoji(EmojiWait) + " سهمیهٔ امروز: <b>" + quota + "</b>\n" +
		customEmoji(EmojiBNB) + " کیف‌پول BSC: <b>" + wallet + "</b>\n\n" +
		"یک گزینه را انتخاب کنید:"
	buttons := [][]map[string]any{
		{button("📈 تحلیل بازار", "analyze"), button("💎 اشتراک من", "subscription")},
		{button("👛 کیف‌پول من", "wallet"), button("💳 پرداخت‌ها", "payments")},
		{button("⚙️ تنظیمات", "settings"), button("❓ راهنما", "help")},
	}
	if !forceNew {
		_ = b.screen(ctx, id, msg, buttons)
		return
	}
	messageID, e := b.sendWithResult(ctx, id, msg, buttons, true)
	if e == nil && b.Store != nil && messageID > 0 {
		_ = b.Store.SetDashboardMessage(id, messageID)
	}
}
func partsFromState(s string) []string { return strings.Split(strings.TrimPrefix(s, "confirm:"), ":") }
func selectionLabel(parts []string) string {
	if len(parts) != 3 {
		return "انتخاب نامعتبر"
	}
	name := parts[0]
	if v, ok := catalog.Find(name); ok {
		name = strings.ReplaceAll(v.Name, " / ", "/")
	}
	frame := map[string]string{"1min": "۱ دقیقه", "5min": "۵ دقیقه", "15min": "۱۵ دقیقه", "30min": "۳۰ دقیقه", "1h": "۱ ساعت", "4h": "۴ ساعت", "1day": "روزانه", "1week": "هفتگی"}[parts[1]]
	style := map[string]string{"scalp": "اسکالپ", "day": "روزانه", "swing": "میان‌مدت", "position": "بلندمدت"}[parts[2]]
	return name + " · " + frame + " · " + style
}
func (b Bot) symbols(ctx context.Context, id int64) {
	b.symbolsPage(ctx, id, 0)
}
func (b Bot) symbolsPage(ctx context.Context, id int64, page int) {
	const size = 6
	pages := (len(catalog.Instruments) + size - 1) / size
	if page < 0 {
		page = 0
	}
	if page >= pages {
		page = pages - 1
	}
	start, end := page*size, (page+1)*size
	if end > len(catalog.Instruments) {
		end = len(catalog.Instruments)
	}
	var rows [][]map[string]any
	for i := start; i < end; i += 2 {
		row := []map[string]any{button(catalog.Instruments[i].Name, fmt.Sprintf("s:%d", i))}
		if i+1 < end {
			row = append(row, button(catalog.Instruments[i+1].Name, fmt.Sprintf("s:%d", i+1)))
		}
		rows = append(rows, row)
	}
	nav := []map[string]any{}
	if page > 0 {
		nav = append(nav, button("قبلی", fmt.Sprintf("symbols:%d", page-1)))
	}
	if page+1 < pages {
		nav = append(nav, button("بعدی", fmt.Sprintf("symbols:%d", page+1)))
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, []map[string]any{button("🏠 منوی اصلی", "menu")})
	_ = b.screen(ctx, id, fmt.Sprintf("<b>%s انتخاب بازار</b>\n\nصفحه %d از %d", customEmoji(EmojiMarketAnalysis), page+1, pages), rows)
}
func (b Bot) frames(ctx context.Context, id int64) {
	var rows [][]map[string]any
	for i := 0; i < 8; i += 4 {
		var row []map[string]any
		for j := i; j < i+4; j++ {
			row = append(row, button(catalog.Frames[j].Label(), fmt.Sprintf("f:%d", j)))
		}
		rows = append(rows, row)
	}
	b.send(ctx, id, "<b>"+customEmoji(EmojiTimeframe)+" انتخاب تایم‌فریم</b>", rows)
}

func (b Bot) horizons(ctx context.Context, id int64, frame catalog.Timeframe) {
	allowed := catalog.AllowedHorizons(frame)
	rows := make([][]map[string]any, 0, (len(allowed)+1)/2+1)
	for i := 0; i < len(allowed); i += 2 {
		row := []map[string]any{button(allowed[i].LabelFA(), "y:"+string(allowed[i]))}
		if i+1 < len(allowed) {
			row = append(row, button(allowed[i+1].LabelFA(), "y:"+string(allowed[i+1])))
		}
		rows = append(rows, row)
	}
	rows = append(rows, []map[string]any{button("بازگشت", "back"), button("انصراف", "cancel")})
	b.send(ctx, id, "<b>افق معاملاتی</b>\n\nفقط گزینه‌های سازگار با "+frame.Label()+" نمایش داده می‌شوند.", rows)
}
func (b Bot) onboard(ctx context.Context, id int64, u profile.User, text, cb string) {
	if text == "/start" && u.State == "menu" {
		u.State = "markets"
	}
	switch u.State {
	case "markets":
		if strings.HasPrefix(cb, "m:") {
			i, _ := strconv.Atoi(strings.TrimPrefix(cb, "m:"))
			if i >= 0 && i < len(catalog.Instruments) {
				u.Markets = catalog.Instruments[i].Symbol
				u.State = "min"
				b.Store.Save(u)
			}
		} else {
			b.symbolsOnboard(ctx, id)
			return
		}
		fallthrough
	case "min":
		if strings.HasPrefix(cb, "p:") {
			text = strings.TrimPrefix(cb, "p:")
		}
		if u.State == "min" && text != "" {
			v, e := strconv.ParseFloat(text, 64)
			if e == nil && v > 0 && v <= 100 {
				u.MinProfit = v
				u.State = "max"
				b.Store.Save(u)
				text = ""
				cb = ""
			} else {
				b.send(ctx, id, "حداقل سود را به صورت درصد مثبت بنویسید.", nil)
				return
			}
		}
		if u.State == "min" {
			b.send(ctx, id, "حداقل سود مورد انتظار چند درصد از ارزش ورود است؟ عدد دلخواه را هم می‌توانی بنویسی.", [][]map[string]any{{button("۰٫۵٪", "p:0.5"), button("۱٪", "p:1"), button("۲٪", "p:2")}, {button("بازگشت", "back"), button("انصراف", "cancel")}})
			return
		}
		fallthrough
	case "max":
		if strings.HasPrefix(cb, "p:") {
			text = strings.TrimPrefix(cb, "p:")
		}
		if u.State == "max" && text != "" {
			v, e := strconv.ParseFloat(text, 64)
			if e == nil && v >= u.MinProfit && v <= 100 {
				u.MaxProfit = v
				u.State = "risk"
				b.Store.Save(u)
				text = ""
				cb = ""
			} else {
				b.send(ctx, id, "حداکثر سود باید برابر یا بیشتر از حداقل باشد.", nil)
				return
			}
		}
		if u.State == "max" {
			b.send(ctx, id, "حداکثر سود مورد انتظار چند درصد از ارزش ورود است؟", [][]map[string]any{{button("۱٪", "p:1"), button("۲٪", "p:2"), button("۵٪", "p:5")}, {button("بازگشت", "back"), button("انصراف", "cancel")}})
			return
		}
		fallthrough
	case "risk":
		if strings.HasPrefix(cb, "p:") {
			text = strings.TrimPrefix(cb, "p:")
		}
		if u.State == "risk" {
			if text != "" {
				v, e := strconv.ParseFloat(text, 64)
				if e != nil || v <= 0 || v > 10 {
					b.send(ctx, id, "ریسک معتبر بین ۰ و ۱۰ درصد سرمایه است.", nil)
					return
				}
				u.MaxRisk = v
			} else if cb == "risk_default" {
				u.MaxRisk = 1
			} else {
				b.send(ctx, id, "حداکثر ریسک هر معامله چند درصد از سرمایه است؟", [][]map[string]any{{button("۰٫۵٪", "p:0.5"), button("۱٪", "p:1"), button("۲٪", "p:2")}, {button("بازگشت", "back"), button("انصراف", "cancel")}})
				return
			}
			u.Completed = true
			u.State = "menu"
			b.Store.Save(u)
			b.send(ctx, id, fmt.Sprintf("پروفایل ذخیره شد. بازار: %s | سود: %.2f تا %.2f درصد ارزش ورود | ریسک: %.2f درصد سرمایه", html.EscapeString(u.Markets), u.MinProfit, u.MaxProfit, u.MaxRisk), nil)
			b.menu(ctx, id)
		}
	}
}
func (b Bot) symbolsOnboard(ctx context.Context, id int64) {
	var rows [][]map[string]any
	for i, v := range catalog.Instruments {
		rows = append(rows, []map[string]any{button(v.Name, fmt.Sprintf("m:%d", i))})
	}
	b.send(ctx, id, "بازار دلخواه خود را انتخاب کنید.", rows)
}
func (b Bot) settings(ctx context.Context, id int64, u profile.User) {
	inst, _ := catalog.Find(u.Markets)
	var marketName string
	if inst.Name != "" {
		marketName = inst.Name + " | " + inst.Symbol
	} else {
		marketName = u.Markets
	}
	msg := fmt.Sprintf("⚙️ تنظیمات فعلی:\n📊 بازار: %s\n💰 حداقل سود: %.2f%%\n💰 حداکثر سود: %.2f%%\n🛡 حداکثر ریسک: %.2f%%", html.EscapeString(marketName), u.MinProfit, u.MaxProfit, u.MaxRisk)
	rows := [][]map[string]any{
		{button("تغییر بازار", "set:markets"), button("تغییر حداقل سود", "set:min")},
		{button("تغییر حداکثر سود", "set:max"), button("تغییر ریسک", "set:risk")},
		{button("منوی اصلی", "menu")},
	}
	b.send(ctx, id, msg, rows)
}
func (b Bot) symbolsSettings(ctx context.Context, id int64, u profile.User) {
	var rows [][]map[string]any
	for i, v := range catalog.Instruments {
		label := v.Name + " | " + v.Symbol
		if v.Symbol == u.Markets {
			label = "✅ " + label
		}
		rows = append(rows, []map[string]any{button(label, fmt.Sprintf("set:m:%d", i))})
	}
	rows = append(rows, []map[string]any{button("بازگشت", "settings"), button("انصراف", "cancel")})
	b.send(ctx, id, "بازار معامله را انتخاب کنید.", rows)
}
func (b Bot) handleSettingsText(ctx context.Context, id int64, u profile.User, text string) {
	state := u.State
	v, e := strconv.ParseFloat(text, 64)
	if e != nil || v <= 0 {
		b.send(ctx, id, customEmoji(EmojiFailure)+" عدد معتبر نیست. لطفاً دوباره بنویسید.", nil)
		return
	}
	switch state {
	case "settings:min":
		if v > u.MaxProfit && u.MaxProfit > 0 {
			b.send(ctx, id, "حداقل نمی‌تواند بیشتر از حداکثر باشد.", nil)
			return
		}
		u.MinProfit = v
	case "settings:max":
		if u.MinProfit > 0 && v < u.MinProfit {
			b.send(ctx, id, "حداکثر نمی‌تواند کمتر از حداقل باشد.", nil)
			return
		}
		u.MaxProfit = v
	case "settings:risk":
		if v > 10 {
			b.send(ctx, id, "ریسک باید بین ۰ و ۱۰ باشد.", nil)
			return
		}
		u.MaxRisk = v
	default:
		u.State = "settings"
		b.Store.Save(u)
		b.settings(ctx, id, u)
		return
	}
	u.State = "settings"
	b.Store.Save(u)
	b.send(ctx, id, fmt.Sprintf("%s <b>تنظیمات به‌روزرسانی شد</b>\n\nبازار: %s\nحداقل سود: %.2f%%\nحداکثر سود: %.2f%%\nریسک: %.2f%%", customEmoji(EmojiSuccess), html.EscapeString(u.Markets), u.MinProfit, u.MaxProfit, u.MaxRisk), [][]map[string]any{{button("بازگشت به تنظیمات", "settings"), button("منوی اصلی", "menu")}})
}
func (b Bot) help(ctx context.Context, id int64) {
	msg := "<b>❓ راهنمای مارکت‌رادار</b>\n\n" +
		"📈 <b>تحلیل بازار:</b> انتخاب بازار، تایم‌فریم و ریسک\n" +
		"👛 <b>کیف‌پول:</b> نمایش آدرس و موجودی واقعی BNB\n" +
		"💳 <b>پرداخت‌ها:</b> فاکتورها و وضعیت تأیید شبکه\n" +
		"💎 <b>اشتراک:</b> مشاهده اعتبار یا تمدید\n\n" +
		"این سامانه سفارش معاملاتی ارسال نمی‌کند."
	_ = b.screen(ctx, id, msg, [][]map[string]any{{button("🏠 منوی اصلی", "menu")}})
}

func (b Bot) subscriptionDashboard(ctx context.Context, id int64) {
	access, e := b.Store.Access(id, time.Now())
	if e != nil {
		_ = b.screen(ctx, id, "<b>"+customEmoji(EmojiFailure)+" اشتراک من</b>\n\n"+customEmoji(EmojiAttention)+" دریافت وضعیت اشتراک ناموفق بود.", [][]map[string]any{{button("تلاش دوباره", "subscription"), button("🏠 خانه", "menu")}})
		return
	}
	remaining := fmt.Sprintf("%d از %d", access.Remaining, access.Allowance)
	if access.Allowance < 0 {
		remaining = "نامحدود"
	}
	status := customEmoji(EmojiExpiration) + " طرح: <b>Visitor</b>\nسهمیهٔ امروز: <b>" + remaining + "</b>"
	if access.SubscriptionActive {
		until, _ := time.Parse(time.RFC3339, access.SubscriptionUntil)
		status = customEmoji(EmojiSuccess) + " طرح <b>" + access.Policy.Label + "</b> فعال است.\nسهمیهٔ امروز: <b>" + remaining + "</b>\n\n" + customEmoji(EmojiExpiration) + " اعتبار تا: <b>" + until.In(time.FixedZone("تهران", 12600)).Format("2006/01/02 15:04") + "</b>"
	}
	rows := [][]map[string]any{{button("🔄 بررسی مجدد", "subscription"), button("💳 پرداخت‌ها", "payments")}}
	if !access.SubscriptionActive {
		rows = append([][]map[string]any{{button("🛒 خرید یا تمدید", "buy")}}, rows...)
	}
	rows = append(rows, []map[string]any{button("🏠 منوی اصلی", "menu")})
	_ = b.screen(ctx, id, "<b>"+customEmoji(EmojiSuccess)+" اشتراک من</b>\n\n"+status, rows)
}

func (b Bot) createWallet(ctx context.Context, chat, id int64) {
	if b.Custody == nil {
		_ = b.screen(ctx, chat, "<b>"+customEmoji(EmojiBNB)+" ساخت کیف‌پول</b>\n\n"+customEmoji(EmojiAttention)+" سرویس نگهداری کیف‌پول در دسترس نیست.", [][]map[string]any{{button("🏠 منوی اصلی", "menu")}})
		return
	}
	address, e := b.Store.EnsureCustodialWallet(id)
	if e != nil {
		_ = b.screen(ctx, chat, "<b>"+customEmoji(EmojiBNB)+" ساخت کیف‌پول</b>\n\n"+customEmoji(EmojiFailure)+" ساخت کیف‌پول انجام نشد. دوباره تلاش کنید.", [][]map[string]any{{button("تلاش دوباره", "wallet:create"), button("🏠 خانه", "menu")}})
		return
	}
	has, keyErr := b.Custody.HasKey(address)
	if keyErr != nil || !has {
		_ = b.screen(ctx, chat, "<b>👛 کیف‌پول موجود</b>\n\nآدرس قدیمی شما حفظ شده است، اما کلید متناظر آن در دسترس نیست و کیف‌پول قابل امضا نیست. با پشتیبانی تماس بگیرید.\n\n<code>"+html.EscapeString(address)+"</code>", [][]map[string]any{{button("🏠 منوی اصلی", "menu")}})
		return
	}
	b.walletDashboard(ctx, chat, id)
}

func (b Bot) walletDashboard(ctx context.Context, chat, id int64) {
	address, e := b.Store.Wallet(id)
	if e != nil {
		_ = b.screen(ctx, chat, "<b>"+customEmoji(EmojiBNB)+" کیف‌پول من</b>\n\n"+customEmoji(EmojiAttention)+" هنوز کیف‌پولی برای حساب شما ساخته نشده است. ساخت کیف‌پول فقط با تأیید صریح شما انجام می‌شود.", [][]map[string]any{{button("➕ ساخت کیف‌پول", "wallet:create")}, {button("🏠 منوی اصلی", "menu")}})
		return
	}
	balance := "در دسترس نیست"
	network := "BSC Mainnet · Chain ID 56"
	if b.Chain != nil {
		readCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		chainID, chainErr := b.Chain.ChainID(readCtx)
		amount, balanceErr := b.Chain.Balance(readCtx, address)
		if chainErr == nil && chainID == 56 && balanceErr == nil {
			balance = formatBNB(amount.String()) + " BNB"
		} else if chainErr == nil && chainID != 56 {
			network = "⚠️ شبکه RPC نادرست"
		}
	}
	msg := "<b>" + customEmoji(EmojiBNB) + " کیف‌پول من</b>\n\n" +
		"🌐 شبکه: <b>" + network + "</b>\n" +
		customEmoji(EmojiBNB) + " موجودی: <b>" + balance + "</b>\n\n" +
		"آدرس واریز اختصاصی:\n<code>" + html.EscapeString(address) + "</code>\n\n" +
		"کلید خصوصی هیچ‌گاه در ربات نمایش داده نمی‌شود."
	rows := [][]map[string]any{{copyButton("📋 کپی آدرس", address), button("🔄 تازه‌سازی", "wallet:refresh")}, {button("💳 پرداخت‌ها", "payments"), button("🏠 خانه", "menu")}}
	if e = b.screen(ctx, chat, msg, rows); e != nil {
		fallback := [][]map[string]any{{button("🔄 تازه‌سازی", "wallet:refresh"), button("💳 پرداخت‌ها", "payments")}, {button("🏠 منوی اصلی", "menu")}}
		_ = b.screen(ctx, chat, msg+"\n\nبرای کپی، روی آدرس بالا لمس طولانی کنید.", fallback)
	}
}

func (b Bot) paymentHistory(ctx context.Context, chat, id int64, page int) {
	const size = 5
	if page < 0 {
		page = 0
	}
	invs, total, e := b.Store.PageTelegramInvoices(id, size, page*size)
	if e != nil {
		_ = b.screen(ctx, chat, "<b>"+customEmoji(EmojiInvoice)+" پرداخت‌ها</b>\n\n"+customEmoji(EmojiAttention)+" دریافت تاریخچه ناموفق بود.", [][]map[string]any{{button("تلاش دوباره", "payments"), button("🏠 خانه", "menu")}})
		return
	}
	pages := (total + size - 1) / size
	if pages == 0 {
		pages = 1
	}
	if len(invs) == 0 && page > 0 {
		b.paymentHistory(ctx, chat, id, pages-1)
		return
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "<b>%s پرداخت‌ها</b>\nصفحه %d از %d\n", customEmoji(EmojiInvoice), page+1, pages)
	if len(invs) == 0 {
		sb.WriteString("\nهنوز پرداخت یا فاکتوری ثبت نشده است.")
	}
	statusFA := map[string]string{"pending": customEmoji(EmojiAttention) + " در انتظار پرداخت و تأیید", "paid": customEmoji(EmojiSuccess) + " تأییدشده", "expired": customEmoji(EmojiExpiration) + " منقضی", "cancelled": customEmoji(EmojiCancellation) + " لغوشده"}
	for _, inv := range invs {
		status := statusFA[inv.Status]
		if status == "" {
			status = html.EscapeString(inv.Status)
		}
		fmt.Fprintf(&sb, "\n\n<b>%s BNB</b>\nوضعیت: %s\nتاریخ: %s\nفاکتور: <code>%s</code>", formatBNB(inv.PriceWei), status, inv.CreatedAt.In(time.FixedZone("تهران", 12600)).Format("2006/01/02 15:04"), html.EscapeString(inv.ID))
		if inv.TxHash != "" {
			fmt.Fprintf(&sb, "\nتراکنش: <code>%s</code>", html.EscapeString(inv.TxHash))
		}
		if inv.Status == "paid" {
			sb.WriteString("\nاعتبار اعطاشده: <b>یک ماه طرح Standard</b>")
		}
	}
	var nav []map[string]any
	if page > 0 {
		nav = append(nav, button("قبلی", fmt.Sprintf("payments:%d", page-1)))
	}
	if (page+1)*size < total {
		nav = append(nav, button("بعدی", fmt.Sprintf("payments:%d", page+1)))
	}
	rows := [][]map[string]any{}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, []map[string]any{button("🛒 خرید یا تمدید", "buy"), button("🏠 خانه", "menu")})
	_ = b.screen(ctx, chat, sb.String(), rows)
}

func (b Bot) checkout(ctx context.Context, chat, id int64) {
	if b.Custody == nil {
		_ = b.send(ctx, chat, customEmoji(EmojiAttention)+" سیستم پرداخت فعال نیست. بعداً دوباره تلاش کنید.", nil)
		return
	}
	if b.PlanPrice == nil {
		_ = b.send(ctx, chat, customEmoji(EmojiAttention)+" قیمت اشتراک در دسترس نیست. بعداً دوباره تلاش کنید.", nil)
		return
	}
	address, e := b.Store.Wallet(id)
	if e != nil {
		_ = b.screen(ctx, chat, "<b>"+customEmoji(EmojiInvoice)+" خرید اشتراک</b>\n\n"+customEmoji(EmojiAttention)+" برای دریافت فاکتور ابتدا باید کیف‌پول واریز اختصاصی خود را بسازید.", [][]map[string]any{{button("➕ ساخت کیف‌پول", "wallet:create")}, {button("🏠 منوی اصلی", "menu")}})
		return
	}
	if has, keyErr := b.Custody.HasKey(address); keyErr != nil || !has {
		_ = b.send(ctx, chat, customEmoji(EmojiFailure)+" کیف پول موجود قابل امضا نیست؛ با پشتیبانی تماس بگیرید.", nil)
		return
	}
	price, e := b.PlanPrice()
	if e != nil {
		_ = b.send(ctx, chat, customEmoji(EmojiAttention)+" قیمت اشتراک هنوز تعیین نشده است.", nil)
		return
	}
	invs, e := b.Store.ListTelegramInvoices(id)
	if e != nil {
		_ = b.send(ctx, chat, customEmoji(EmojiFailure)+" دریافت فاکتورها ناموفق بود. دوباره تلاش کنید.", nil)
		return
	}
	now := time.Now().UTC()
	var inv profile.TelegramInvoice
	for _, v := range invs {
		if v.Status == "pending" && now.Before(v.ExpiresAt) {
			inv = v
			break
		}
	}
	if inv.ID == "" {
		expires := now.Add(15 * time.Minute)
		if b.Chain == nil {
			_ = b.send(ctx, chat, customEmoji(EmojiFailure)+" پایش شبکه در دسترس نیست؛ فاکتور ساخته نشد.", nil)
			return
		}
		readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		head, headErr := b.Chain.Head(readCtx)
		cancel()
		if headErr != nil {
			_ = b.send(ctx, chat, customEmoji(EmojiFailure)+" ارتباط شبکه موقتاً در دسترس نیست؛ فاکتور ساخته نشد.", nil)
			return
		}
		inv, e = b.Store.CreateTelegramInvoice(id, address, price.String(), 56, expires, head+1)
		if e != nil {
			_ = b.send(ctx, chat, customEmoji(EmojiFailure)+" ساخت فاکتور ناموفق بود. دوباره تلاش کنید.", nil)
			return
		}
	}
	amount := formatBNB(inv.PriceWei)
	expiresTehran := inv.ExpiresAt.In(time.FixedZone("تهران", 12600)).Format("2006-01-02 15:04")
	msg := fmt.Sprintf("<b>%s فاکتور یک‌ماههٔ Standard</b>\n\nسهمیه: <b>%d تحلیل موفق در روز</b>\n%s <b>مبلغ دقیق:</b> %s BNB\n🌐 <b>شبکه:</b> BSC Mainnet\n%s <b>مهلت:</b> %s\n\n%s <b>آدرس واریز:</b>\n<code>%s</code>\n\nشماره فاکتور:\n<code>%s</code>", customEmoji(EmojiInvoice), entitlement.Defaults[entitlement.Standard].DailyAllowance, customEmoji(EmojiBNB), amount, customEmoji(EmojiExpiration), expiresTehran, customEmoji(EmojiEntryPoint), html.EscapeString(inv.Address), html.EscapeString(inv.ID))
	rows := [][]map[string]any{
		{copyButton("📋 کپی آدرس", inv.Address), button("🔄 بررسی پرداخت", "check_payment")},
		{button("💳 پرداخت‌ها", "payments"), button("🏠 خانه", "menu")},
	}
	if e = b.screen(ctx, chat, msg, rows); e != nil {
		fallback := [][]map[string]any{{button("🔄 بررسی پرداخت", "check_payment"), button("💳 پرداخت‌ها", "payments")}, {button("🏠 منوی اصلی", "menu")}}
		_ = b.screen(ctx, chat, msg+"\n\nبرای کپی، روی آدرس بالا لمس طولانی کنید.", fallback)
	}
}
func (b Bot) myInvoices(ctx context.Context, chat, id int64) {
	b.paymentHistory(ctx, chat, id, 0)
}
func formatBNB(wei string) string {
	n, ok := new(big.Int).SetString(wei, 10)
	if !ok {
		return wei
	}
	bnb := new(big.Float).Quo(new(big.Float).SetInt(n), big.NewFloat(1e18))
	return bnb.Text('f', 8)
}
func frameFromLabel(label string) string {
	for _, f := range catalog.Frames {
		if f.Label() == label {
			return string(f)
		}
	}
	return ""
}
func (b Bot) monitor(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.checkAlerts(ctx)
		}
	}
}
func (b Bot) checkAlerts(ctx context.Context) {
	alerts, e := b.Store.ActiveAlerts()
	if e != nil {
		return
	}
	groups := map[string][]profile.Alert{}
	for _, a := range alerts {
		groups[a.Symbol+"|"+a.Timeframe] = append(groups[a.Symbol+"|"+a.Timeframe], a)
	}
	for _, items := range groups {
		a := items[0]
		f, e := catalog.ParseFrame(a.Timeframe)
		if e != nil {
			continue
		}
		candles, e := b.Engine.Market.Fetch(ctx, a.Symbol, a.Timeframe, 3)
		if e != nil {
			continue
		}
		candles = marketdata.Closed(candles, time.Now().UTC(), f.Duration(), b.Engine.Buffer)
		for _, alert := range items {
			if !alert.Expires.IsZero() && time.Now().After(alert.Expires) {
				b.Store.CloseAlert(alert.ID)
				_ = b.send(ctx, alert.UserID, customEmoji(EmojiExpiration)+" هشدار این سناریو منقضی شد. برای ارزیابی تازه، تحلیل جدید بگیرید.", nil)
				continue
			}
			for _, c := range candles {
				if !c.Time.After(alert.After) {
					continue
				}
				stage, hit := condition.Step(alert.Kind, alert.Price, alert.Entry, alert.Stage, c)
				if stage != alert.Stage {
					alert.Stage = stage
					_ = b.Store.AdvanceAlert(alert.ID, stage, c.Time)
				}
				if hit {
					if b.send(ctx, alert.UserID, fmt.Sprintf("%s <b>شرط قیمت در %s تأیید شد</b>\n%s کندل بسته‌شده: %s\n\nاین هشدار سفارش معامله نیست؛ پیش از هر تصمیم تحلیل را به‌روز کنید.", customEmoji(EmojiSuccess), html.EscapeString(alert.Symbol), customEmoji(EmojiLastClosedCandle), c.Time.Format(time.RFC3339)), nil) == nil {
						b.Store.CloseAlert(alert.ID)
					}
					break
				}
			}
		}
	}
}
