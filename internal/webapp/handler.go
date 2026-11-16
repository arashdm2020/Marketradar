package webapp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"tradebot/internal/domain"
	"tradebot/internal/profile"
	"tradebot/internal/telegram"
)

//go:embed user.html
var userPage []byte

type Handler struct {
	Store    *Store
	Billing  *Billing
	Analysis *AnalysisService
	Profile  *profile.Store
}

type telegramLoginPayload struct {
	ID              int64  `json:"id"`
	FirstName       string `json:"first_name"`
	LastName        string `json:"last_name"`
	Username        string `json:"username"`
	PhotoURL        string `json:"photo_url"`
	AuthDate        int64  `json:"auth_date"`
	Hash            string `json:"hash"`
	Nonce           string `json:"nonce"`
	AllowsWriteToPM string `json:"allows_write_to_pm"`
}

func verifyTelegramLogin(v telegramLoginPayload, botToken string, now time.Time) bool {
	if botToken == "" || v.ID <= 0 || v.Hash == "" || now.Sub(time.Unix(v.AuthDate, 0)) > 5*time.Minute || time.Unix(v.AuthDate, 0).After(now.Add(time.Minute)) {
		return false
	}
	// Telegram's Login Widget signature covers only fields returned by Telegram;
	// the browser state nonce is independently one-time consumed in SQLite.
	data := map[string]string{"id": strconv.FormatInt(v.ID, 10), "first_name": v.FirstName, "auth_date": strconv.FormatInt(v.AuthDate, 10)}
	for key, value := range map[string]string{"last_name": v.LastName, "username": v.Username, "photo_url": v.PhotoURL, "allows_write_to_pm": v.AllowsWriteToPM} {
		if value != "" {
			data[key] = value
		}
	}
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+data[k])
	}
	secret := sha256.Sum256([]byte(botToken))
	mac := hmac.New(sha256.New, secret[:])
	_, _ = mac.Write([]byte(strings.Join(parts, "\n")))
	want, e := hex.DecodeString(v.Hash)
	return e == nil && hmac.Equal(mac.Sum(nil), want)
}

func (h *Handler) Routes() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("/app/", h.home)
	m.HandleFunc("/app/register", h.register)
	m.HandleFunc("/app/login", h.login)
	m.HandleFunc("/app/logout", h.logout)
	m.HandleFunc("/app/api/logout", h.logoutAPI)
	m.HandleFunc("/app/api/me", h.me)
	m.HandleFunc("/app/api/telegram/nonce", h.telegramNonce)
	m.HandleFunc("/app/api/telegram/callback", h.telegramCallback)
	m.HandleFunc("/app/api/invoice", h.invoice)
	m.HandleFunc("/app/api/analyze", h.analyze)
	m.HandleFunc("/app/api/analysis", h.analysis)
	m.HandleFunc("/app/api/analyses", h.analyses)
	m.HandleFunc("/app/link", h.link)
	return m
}

func (h *Handler) telegramNonce(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !sameOrigin(w, r) {
		return
	}
	if h.Profile == nil {
		http.Error(w, "Telegram login unavailable", 503)
		return
	}
	nonce, err := h.Profile.IssueTelegramLoginNonce(time.Now())
	if err != nil {
		http.Error(w, "Telegram login unavailable", 503)
		return
	}
	out(w, map[string]string{"nonce": nonce})
}

func (h *Handler) telegramCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != "POST" || !sameOrigin(w, r) {
		return
	}
	if h.Profile == nil {
		http.Error(w, "Telegram login unavailable", 503)
		return
	}
	var payload telegramLoginPayload
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&payload) != nil || payload.ID <= 0 || payload.Nonce == "" || !verifyTelegramLogin(payload, os.Getenv("TELEGRAM_BOT_TOKEN"), time.Now()) {
		http.Error(w, "Telegram authentication failed", 401)
		return
	}
	webID, e := h.Profile.TelegramLoginOnce(payload.Nonce, payload.Hash, payload.ID, payload.FirstName, payload.Username, time.Now())
	if e != nil {
		http.Error(w, "Telegram login expired or already used", 401)
		return
	}
	token, e := h.Store.CreateSessionForUser(webID, time.Now())
	if e != nil {
		http.Error(w, "session unavailable", 500)
		return
	}
	h.setSession(w, token, r)
	out(w, map[string]any{"ok": true})
}
func (h *Handler) user(r *http.Request) (int64, error) {
	c, e := r.Cookie("mr_web")
	if e != nil {
		return 0, e
	}
	return h.Store.Session(c.Value)
}
func (h *Handler) require(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, e := h.user(r)
	if e != nil {
		http.Error(w, "login required", 401)
		return 0, false
	}
	return id, true
}
func sameOrigin(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return false
	}
	origin := r.Header.Get("Origin")
	u, e := url.Parse(origin)
	if origin == "" || e != nil || u.Host != r.Host {
		http.Error(w, "origin rejected", 403)
		return false
	}
	return true
}
func (h *Handler) setSession(w http.ResponseWriter, token string, r *http.Request) {
	secure := r.TLS != nil || strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
	http.SetCookie(w, &http.Cookie{Name: "mr_web", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure, MaxAge: 43200})
}
func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "use Telegram authentication", http.StatusGone)
}
func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "use Telegram authentication", http.StatusGone)
}
func safeNext(v string) string {
	if len(v) == len("/app/link?token=")+36 && len(v) >= len("/app/link?token=") && v[:len("/app/link?token=")] == "/app/link?token=" {
		return v
	}
	return "/app/"
}
func (h *Handler) link(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if h.Profile == nil {
		http.Error(w, "اتصال حساب در دسترس نیست", 503)
		return
	}
	id, e := h.user(r)
	if e != nil {
		http.Redirect(w, r, "/app/?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
		return
	}
	if r.Method == "POST" {
		if !sameOrigin(w, r) {
			return
		}
		if e := h.Profile.ConsumeWebLink(r.FormValue("token"), id, time.Now()); e != nil {
			http.Error(w, "پیوند منقضی یا مصرف شده است؛ دوباره از ربات درخواست کنید.", 409)
			return
		}
		http.Redirect(w, r, "/app/?linked=1", http.StatusSeeOther)
		return
	}
	token := r.URL.Query().Get("token")
	if len(token) != 36 {
		http.Error(w, "پیوند نامعتبر است", 400)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<html lang="fa" dir="rtl"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>تأیید اتصال حساب</title><style>body{font:17px sans-serif;background:#f4f7fb;color:#17233a;max-width:600px;margin:10vh auto;padding:1rem}section{background:white;padding:2rem;border-radius:18px}button{background:#165bc5;color:white;border:0;padding:.8rem 1.4rem;border-radius:10px;cursor:pointer}</style><section><h1>اتصال حساب وب به تلگرام</h1><p>پس از تأیید، وضعیت اشتراک این حساب وب در ربات شما بررسی می‌شود. این پیوند یک‌بارمصرف است و ۱۰ دقیقه اعتبار دارد.</p><form method="post" action="/app/link"><input type="hidden" name="token" value="` + template.HTMLEscapeString(token) + `"><button>تأیید اتصال این حساب</button></form></section></html>`))
}
func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(w, r) {
		return
	}
	if c, e := r.Cookie("mr_web"); e == nil {
		_ = h.Store.Logout(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "mr_web", Value: "", Path: "/", MaxAge: -1, SameSite: http.SameSiteLaxMode, HttpOnly: true, Secure: r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")})
	http.Redirect(w, r, "/app/", 303)
}
func (h *Handler) logoutAPI(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(w, r) {
		return
	}
	if c, e := r.Cookie("mr_web"); e == nil {
		_ = h.Store.Logout(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "mr_web", Value: "", Path: "/", MaxAge: -1, SameSite: http.SameSiteLaxMode, HttpOnly: true, Secure: r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")})
	out(w, map[string]bool{"ok": true})
}
func (h *Handler) home(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, e := h.user(r); e != nil {
		loginURL := strings.TrimSpace(os.Getenv("WEB_PUBLIC_URL"))
		if loginURL == "" {
			loginURL = "/login"
		}
		if parsed, err := url.Parse(loginURL); err != nil || parsed.IsAbs() && parsed.Scheme != "https" || !parsed.IsAbs() && !strings.HasPrefix(parsed.Path, "/") {
			loginURL = "/login"
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`<html lang="fa" dir="rtl"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>ورود به MarketRadar</title><body style="font:16px sans-serif;max-width:600px;margin:12vh auto;padding:1.5rem;background:#0b1322;color:#f8fafc"><main style="padding:2rem;border:1px solid #26364d;border-radius:20px;background:#121e30"><h1>ورود به MarketRadar</h1><p>ورود به حساب فقط با تأیید هویت تلگرام انجام می‌شود.</p><a style="display:inline-block;padding:.8rem 1.2rem;border-radius:10px;background:#f28c38;color:#101827;text-decoration:none" href="` + template.HTMLEscapeString(loginURL) + `">ادامه با تلگرام</a></main></body></html>`))
		return
	}
	_, _ = w.Write(userPage)
}
func out(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, ok := h.require(w, r)
	if !ok {
		return
	}
	if h.Profile == nil {
		http.Error(w, "profile unavailable", 503)
		return
	}
	tgID, e := h.Profile.TelegramForWebUser(id)
	if e != nil {
		http.Error(w, "Telegram account not linked", 403)
		return
	}
	access, e := h.Profile.Access(tgID, time.Now())
	if e != nil {
		http.Error(w, "access status unavailable", 500)
		return
	}
	var firstName, username string
	if e = h.Store.DB.QueryRow(`SELECT first_name,username FROM web_users WHERE id=?`, id).Scan(&firstName, &username); e != nil {
		http.Error(w, "profile unavailable", 500)
		return
	}
	address := ""
	if wallet, walletErr := h.Profile.WalletRecordForTelegram(tgID); walletErr == nil {
		address = wallet.Address
	}
	balance := ""
	if address != "" && h.Billing != nil && h.Billing.Chain != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		if chainID, chainErr := h.Billing.Chain.ChainID(ctx); chainErr == nil && chainID == h.Billing.ExpectedChainID {
			if amount, balanceErr := h.Billing.Chain.Balance(ctx, address); balanceErr == nil {
				balance = formatBNB(amount)
			}
		}
		cancel()
	}
	price, priceErr := h.Store.Price()
	ready := h.Billing != nil && h.Billing.Ready()
	network := "نامشخص"
	chainID := uint64(0)
	if h.Billing != nil {
		chainID = h.Billing.ExpectedChainID
	}
	switch chainID {
	case 56:
		network = "BNB Smart Chain Mainnet"
	case 97:
		network = "BNB Smart Chain Testnet"
	}
	invoiceAvailable := false
	if ready && h.Billing.TestInvoicesEnabled {
		var email string
		invoiceAvailable = h.Store.DB.QueryRow(`SELECT email FROM web_users WHERE id=?`, id).Scan(&email) == nil && normalizeEmail(email) == normalizeEmail(h.Billing.TestEmail)
	}
	v := map[string]any{
		"profile":       map[string]any{"telegram_id": tgID, "first_name": firstName, "username": username},
		"subscription":  map[string]any{"plan": access.Policy.Label, "active": access.SubscriptionActive, "until": access.SubscriptionUntil},
		"quota":         map[string]any{"daily_allowance": access.Allowance, "used": access.Used, "remaining": access.Remaining, "exempt": access.Exempt},
		"wallet":        map[string]any{"address": address, "network": network, "chain_id": chainID, "balance_bnb": balance},
		"billing_ready": ready, "invoice_available": invoiceAvailable,
	}
	if priceErr == nil {
		v["price_wei"] = price.String()
	}
	out(w, v)
}

func formatBNB(wei *big.Int) string {
	if wei == nil {
		return ""
	}
	denom := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	whole, frac := new(big.Int), new(big.Int)
	whole.QuoRem(wei, denom, frac)
	if frac.Sign() == 0 {
		return whole.String()
	}
	decimals := frac.String()
	decimals = strings.Repeat("0", 18-len(decimals)) + decimals
	return whole.String() + "." + strings.TrimRight(decimals, "0")
}
func (h *Handler) invoice(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		id, ok := h.require(w, r)
		if !ok {
			return
		}
		v, e := h.Store.Invoice(id, r.URL.Query().Get("id"))
		if e != nil {
			http.Error(w, "not found", 404)
			return
		}
		out(w, v)
		return
	}
	if !sameOrigin(w, r) {
		return
	}
	id, ok := h.require(w, r)
	if !ok {
		return
	}
	v, e := h.Billing.CreateInvoice(r.Context(), id, time.Now())
	if e != nil {
		http.Error(w, "invoice unavailable: "+e.Error(), 503)
		return
	}
	out(w, v)
}
func (h *Handler) analyze(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(w, r) {
		return
	}
	id, ok := h.require(w, r)
	if !ok {
		return
	}
	analysisID, e := h.Analysis.Submit(id, r.FormValue("symbol"), r.FormValue("timeframe"), r.FormValue("style"))
	if e != nil {
		http.Error(w, e.Error(), 403)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(202)
	out(w, map[string]string{"analysis_id": analysisID})
}
func (h *Handler) analysis(w http.ResponseWriter, r *http.Request) {
	id, ok := h.require(w, r)
	if !ok {
		return
	}
	record, e := h.Store.Analysis(id, r.URL.Query().Get("id"))
	if e != nil {
		http.Error(w, "not found", 404)
		return
	}
	var v any = record
	if record.Status == "completed" {
		var d domain.Decision
		if json.Unmarshal(record.Result, &d) == nil {
			v = struct {
				AnalysisRecord
				ReportHTML string `json:"report_html"`
			}{record, "<b>شماره پیگیری تحلیل:</b> <code>" + record.ID + "</code><br>" + telegram.Render(d, record.Symbol, record.Timeframe, record.Style, profile.User{Timezone: "Asia/Tehran"})}
		}
	}
	out(w, v)
}
func (h *Handler) analyses(w http.ResponseWriter, r *http.Request) {
	id, ok := h.require(w, r)
	if !ok {
		return
	}
	v, e := h.Store.RecentAnalyses(id)
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	out(w, v)
}
