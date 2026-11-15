package webapp

import (
	_ "embed"
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
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

func (h *Handler) Routes() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("/app/", h.home)
	m.HandleFunc("/app/register", h.register)
	m.HandleFunc("/app/login", h.login)
	m.HandleFunc("/app/logout", h.logout)
	m.HandleFunc("/app/api/me", h.me)
	m.HandleFunc("/app/api/invoice", h.invoice)
	m.HandleFunc("/app/api/analyze", h.analyze)
	m.HandleFunc("/app/api/analysis", h.analysis)
	m.HandleFunc("/app/api/analyses", h.analyses)
	m.HandleFunc("/app/link", h.link)
	return m
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
	http.SetCookie(w, &http.Cookie{Name: "mr_web", Value: token, Path: "/app", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil, MaxAge: 43200})
}
func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(w, r) {
		return
	}
	if _, e := h.Store.Register(r.FormValue("email"), r.FormValue("password")); e != nil {
		http.Error(w, "registration failed", 400)
		return
	}
	token, e := h.Store.Login(r.FormValue("email"), r.FormValue("password"))
	if e != nil {
		http.Error(w, "login failed", 500)
		return
	}
	h.setSession(w, token, r)
	http.Redirect(w, r, safeNext(r.FormValue("next")), 303)
}
func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(w, r) {
		return
	}
	token, e := h.Store.Login(r.FormValue("email"), r.FormValue("password"))
	if e != nil {
		http.Error(w, "invalid credentials", 401)
		return
	}
	h.setSession(w, token, r)
	http.Redirect(w, r, safeNext(r.FormValue("next")), 303)
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
	http.SetCookie(w, &http.Cookie{Name: "mr_web", Value: "", Path: "/app", MaxAge: -1, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/app/", 303)
}
func (h *Handler) home(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, e := h.user(r); e != nil {
		t := template.Must(template.New("login").Parse(`<html lang="fa" dir="rtl"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>MarketRadar</title><style>body{font:16px sans-serif;max-width:860px;margin:5vh auto;padding:1rem;background:#f3f6fb;color:#17233a}main{display:grid;grid-template-columns:repeat(auto-fit,minmax(270px,1fr));gap:1rem}section{background:white;padding:1.5rem;border-radius:16px;box-shadow:0 8px 28px #1232}input,button{box-sizing:border-box;width:100%;padding:.75rem;margin:.4rem 0;border-radius:9px}input{border:1px solid #c4cfdd}button{background:#165bc5;color:white;border:0;cursor:pointer}</style><h1>MarketRadar</h1><p>مسیر دسترسی: اشتراک ← فاکتور ← تأیید پرداخت ← تحلیل</p><main><section><h2>ساخت حساب</h2><form method="post" action="/app/register"><input type="hidden" name="next" value="{{.}}"><input type="email" name="email" placeholder="ایمیل" required><input type="password" name="password" minlength="12" placeholder="رمز عبور حداقل ۱۲ نویسه" required><button>ثبت‌نام</button></form></section><section><h2>ورود</h2><form method="post" action="/app/login"><input type="hidden" name="next" value="{{.}}"><input type="email" name="email" placeholder="ایمیل" required><input type="password" name="password" required><button>ورود</button></form></section></main></html>`))
		_ = t.Execute(w, safeNext(r.URL.Query().Get("next")))
		return
	}
	_, _ = w.Write(userPage)
}
func out(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	id, ok := h.require(w, r)
	if !ok {
		return
	}
	until, e := h.Store.Subscription(id)
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	price, e := h.Store.Price()
	ready := h.Billing != nil && h.Billing.Ready()
	v := map[string]any{"subscription_active": until.After(time.Now()), "subscription_until": until, "billing_ready": ready, "invoice_available": false}
	if e == nil {
		v["price_wei"] = price.String()
		if ready && h.Billing.TestInvoicesEnabled {
			var email string
			if h.Store.DB.QueryRow(`SELECT email FROM web_users WHERE id=?`, id).Scan(&email) == nil && normalizeEmail(email) == normalizeEmail(h.Billing.TestEmail) {
				v["invoice_available"] = true
			}
		}
	}
	out(w, v)
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
