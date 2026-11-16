package adminweb

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"tradebot/internal/aimgmt"
	"tradebot/internal/custody"
	"tradebot/internal/entitlement"
	"tradebot/internal/profile"
	"tradebot/internal/treasury"
	"tradebot/internal/webapp"
)

//go:embed admin.html
var adminPage []byte

type Server struct {
	Store              *profile.Store
	Web                *webapp.Handler
	AIStore            *aimgmt.Store
	AIHTTP             *http.Client
	NextURL            string
	Treasury           *treasury.Store
	Custody            *custody.Store
	SweepWorker        *treasury.Worker
	BotToken, Username string
	sessionKey         []byte
}

func New(s *profile.Store, token, username string) *Server {
	key := make([]byte, 32)
	if master := os.Getenv("AI_CONFIG_MASTER_KEY"); master != "" {
		sum := sha256.Sum256([]byte("marketradar/admin-session/v1\x00" + master))
		copy(key, sum[:])
	} else {
		_, _ = rand.Read(key)
	}
	return &Server{Store: s, BotToken: token, Username: username, sessionKey: key}
}
func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("/", s.home)
	m.HandleFunc("/auth/telegram", s.login)
	m.HandleFunc("/auth/code", s.codeLogin)
	m.HandleFunc("/api/users", s.users)
	m.HandleFunc("/api/analysis", s.analysis)
	m.HandleFunc("/api/analysis/detail", s.analysisDetail)
	m.HandleFunc("/api/exemption", s.exemption)
	m.HandleFunc("/api/subscription/grant", s.subscriptionGrant)
	m.HandleFunc("/api/config", s.config)
	m.HandleFunc("/api/config/draft", s.configDraft)
	m.HandleFunc("/api/config/compare", s.compareConfig)
	m.HandleFunc("/api/ai/versions", s.aiVersions)
	m.HandleFunc("/api/ai/draft", s.aiDraft)
	m.HandleFunc("/api/ai/publish", s.aiPublish)
	m.HandleFunc("/api/ai/test", s.aiTest)
	m.HandleFunc("/api/ai/enabled", s.aiEnabled)
	m.HandleFunc("/api/dashboard", s.dashboard)
	m.HandleFunc("/api/session", s.session)
	m.HandleFunc("/api/operations/users", s.operationsUsers)
	m.HandleFunc("/api/operations/subscriptions", s.operationsSubscriptions)
	m.HandleFunc("/api/operations/payments", s.operationsPayments)
	m.HandleFunc("/api/operations/finance", s.operationsFinance)
	m.HandleFunc("/api/operations/wallets", s.operationsWallets)
	m.HandleFunc("/api/operations/wallet-transactions", s.operationsWalletTransactions)
	m.HandleFunc("/api/operations/analyses", s.operationsAnalyses)
	m.HandleFunc("/api/operations/system", s.operationsSystem)
	m.HandleFunc("/api/operations/audit", s.operationsAudit)
	m.HandleFunc("/api/treasury", s.treasurySummary)
	m.HandleFunc("/api/treasury/destination", s.treasuryDestination)
	m.HandleFunc("/api/treasury/review", s.treasuryReview)
	m.HandleFunc("/api/treasury/sweep", s.treasurySweep)
	m.HandleFunc("/api/treasury/config", s.treasuryConfig)
	m.HandleFunc("/api/treasury/wallets", s.treasuryWallets)
	m.HandleFunc("/api/treasury/jobs", s.treasuryJobs)
	m.HandleFunc("/api/web-plan", s.webPlan)
	m.HandleFunc("/api/web-payments", s.webPayments)
	m.HandleFunc("/api/web-users", s.webUsers)
	if s.Web != nil {
		webRoutes := s.Web.Routes()
		m.Handle("/app/", webRoutes)
		m.Handle("/app/register", webRoutes)
		m.Handle("/app/login", webRoutes)
		m.Handle("/app/logout", webRoutes)
		m.Handle("/app/link", webRoutes)
		m.Handle("/app/api/", webRoutes)
	}
	return m
}
func (s *Server) aiVersions(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r) {
		return
	}
	if s.AIStore == nil {
		http.Error(w, "AI management unavailable", 503)
		return
	}
	v, e := s.AIStore.List(profile.AdminID)
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	jsonOut(w, v)
}
func (s *Server) aiDraft(w http.ResponseWriter, r *http.Request) {
	if !s.mutation(w, r) {
		return
	}
	if s.AIStore == nil {
		http.Error(w, "AI management unavailable", 503)
		return
	}
	max, e1 := strconv.Atoi(r.FormValue("max_tokens"))
	temp, e2 := strconv.ParseFloat(r.FormValue("temperature"), 64)
	budget, e3 := strconv.Atoi(r.FormValue("reasoning_budget"))
	if e1 != nil || e2 != nil || e3 != nil {
		http.Error(w, "invalid AI settings", 400)
		return
	}
	timeout, e4 := strconv.Atoi(r.FormValue("timeout_seconds"))
	if r.FormValue("timeout_seconds") == "" {
		timeout = 150
		e4 = nil
	}
	if e4 != nil {
		http.Error(w, "invalid timeout", 400)
		return
	}
	v, e := s.AIStore.Draft(profile.AdminID, aimgmt.Version{Provider: r.FormValue("provider"), Model: r.FormValue("model"), BaseURL: r.FormValue("base_url"), TimeoutSeconds: timeout, MaxTokens: max, Temperature: temp, ReasoningBudget: budget, ReasoningEffort: r.FormValue("reasoning_effort")}, r.FormValue("api_key"))
	if e != nil {
		http.Error(w, "invalid AI settings or missing provider key", 400)
		return
	}
	jsonOut(w, v)
}
func (s *Server) aiEnabled(w http.ResponseWriter, r *http.Request) {
	if !s.mutation(w, r) {
		return
	}
	if s.AIStore == nil {
		http.Error(w, "AI management unavailable", 503)
		return
	}
	id, e := strconv.Atoi(r.FormValue("version"))
	if e != nil || s.AIStore.SetEnabled(profile.AdminID, id, r.FormValue("enabled") == "true") != nil {
		http.Error(w, "provider state rejected", 400)
		return
	}
	jsonOut(w, map[string]any{"ok": true})
}
func (s *Server) aiPublish(w http.ResponseWriter, r *http.Request) {
	if !s.mutation(w, r) {
		return
	}
	if s.AIStore == nil {
		http.Error(w, "AI management unavailable", 503)
		return
	}
	id, e := strconv.Atoi(r.FormValue("version"))
	if e != nil || s.AIStore.Publish(profile.AdminID, id) != nil {
		http.Error(w, "AI version unavailable", 400)
		return
	}
	jsonOut(w, map[string]any{"ok": true})
}
func (s *Server) aiTest(w http.ResponseWriter, r *http.Request) {
	if !s.mutation(w, r) {
		return
	}
	if s.AIStore == nil {
		http.Error(w, "AI management unavailable", 503)
		return
	}
	id, e := strconv.Atoi(r.FormValue("version"))
	if e != nil {
		http.Error(w, "invalid version", 400)
		return
	}
	result, e := s.AIStore.Test(r.Context(), profile.AdminID, id, nil)
	if e != nil {
		http.Error(w, "AI test unavailable", 503)
		return
	}
	jsonOut(w, result)
}
func (s *Server) signed(data string) string {
	mac := hmac.New(sha256.New, s.sessionKey)
	mac.Write([]byte(data))
	return hex.EncodeToString(mac.Sum(nil))
}
func (s *Server) authorized(r *http.Request) bool {
	c, e := r.Cookie("mr_admin")
	if e != nil {
		return false
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 3 || parts[0] != "8697362188" || !hmac.Equal([]byte(parts[2]), []byte(s.signed(parts[0]+"."+parts[1]))) {
		return false
	}
	ts, e := strconv.ParseInt(parts[1], 10, 64)
	return e == nil && time.Since(time.Unix(ts, 0)) < 12*time.Hour && time.Since(time.Unix(ts, 0)) >= 0
}
func (s *Server) guard(w http.ResponseWriter, r *http.Request) bool {
	if !s.authorized(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	hash := q.Get("hash")
	id, e := strconv.ParseInt(q.Get("id"), 10, 64)
	at, ae := strconv.ParseInt(q.Get("auth_date"), 10, 64)
	if e != nil || ae != nil || id != profile.AdminID || time.Since(time.Unix(at, 0)) > 5*time.Minute || time.Since(time.Unix(at, 0)) < 0 {
		http.Error(w, "forbidden", 403)
		return
	}
	keys := make([]string, 0)
	for k := range q {
		if k != "hash" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"="+q.Get(k))
	}
	secret := sha256.Sum256([]byte(s.BotToken))
	mac := hmac.New(sha256.New, secret[:])
	mac.Write([]byte(strings.Join(pairs, "\n")))
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(hash), []byte(want)) {
		http.Error(w, "forbidden", 403)
		return
	}
	value := fmt.Sprintf("%d.%d", id, time.Now().Unix())
	http.SetCookie(w, &http.Cookie{Name: "mr_admin", Value: value + "." + s.signed(value), Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil || os.Getenv("ADMIN_COOKIE_SECURE") == "true", MaxAge: 43200})
	http.Redirect(w, r, s.redirectTarget(), http.StatusSeeOther)
}
func (s *Server) codeLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	ok, e := s.Store.ConsumeAdminCode(r.FormValue("code"))
	if e != nil || !ok {
		http.Error(w, "invalid or expired code", 403)
		return
	}
	value := fmt.Sprintf("%d.%d", profile.AdminID, time.Now().Unix())
	http.SetCookie(w, &http.Cookie{Name: "mr_admin", Value: value + "." + s.signed(value), Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil || os.Getenv("ADMIN_COOKIE_SECURE") == "true", MaxAge: 43200})
	http.Redirect(w, r, s.redirectTarget(), http.StatusSeeOther)
}
func (s *Server) redirectTarget() string {
	if s.NextURL != "" {
		return s.NextURL
	}
	return "/"
}
func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		if s.NextURL != "" {
			http.Redirect(w, r, strings.TrimRight(s.NextURL, "/")+"/login", http.StatusSeeOther)
			return
		}
		t := template.Must(template.New("login").Parse(`<html lang="fa" dir="rtl"><meta charset="utf-8"><h1>پنل MarketRadar</h1><p>در چت خصوصی ربات دستور /admin را بفرستید؛ کد پنج‌دقیقه‌ای را اینجا وارد کنید.</p><form action="/auth/code" method="post"><input name="code" autocomplete="off" required><button>ورود</button></form>{{if .}}<script async src="https://telegram.org/js/telegram-widget.js?22" data-telegram-login="{{.}}" data-size="large" data-auth-url="/auth/telegram"></script>{{end}}</html>`))
		_ = t.Execute(w, s.Username)
		return
	}
	if s.NextURL != "" {
		http.Redirect(w, r, s.NextURL, http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(adminPage)
}
func jsonOut(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
func (s *Server) users(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r) {
		return
	}
	v, e := s.Store.AdminUsers(profile.AdminID)
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	jsonOut(w, v)
}
func (s *Server) analysis(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r) {
		return
	}
	v, e := s.Store.FindAnalysis(profile.AdminID, r.URL.Query().Get("id"))
	if e != nil {
		if s.Web != nil {
			webRecord, we := s.Web.Store.AdminAnalysis(r.URL.Query().Get("id"))
			if we == nil {
				jsonOut(w, webRecord)
				return
			}
		}
		http.Error(w, "not found", 404)
		return
	}
	jsonOut(w, v)
}
func (s *Server) mutation(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return false
	}
	if !s.guard(w, r) {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		http.Error(w, "origin required", 403)
		return false
	}
	u, e := url.Parse(origin)
	if e != nil || u.Host != r.Host {
		http.Error(w, "origin rejected", 403)
		return false
	}
	return true
}
func (s *Server) exemption(w http.ResponseWriter, r *http.Request) {
	if !s.mutation(w, r) {
		return
	}
	id, e := strconv.ParseInt(r.FormValue("user_id"), 10, 64)
	if e != nil {
		http.Error(w, "invalid user", 400)
		return
	}
	on := r.FormValue("exempt") == "true"
	if e = s.Store.SetExempt(profile.AdminID, id, on); e != nil {
		http.Error(w, "update failed", 400)
		return
	}
	jsonOut(w, map[string]any{"ok": true})
}
func (s *Server) subscriptionGrant(w http.ResponseWriter, r *http.Request) {
	if !s.mutation(w, r) {
		return
	}
	id, e := strconv.ParseInt(r.FormValue("user_id"), 10, 64)
	days, e2 := strconv.Atoi(r.FormValue("days"))
	channel := r.FormValue("channel")
	planRaw := strings.ToLower(strings.TrimSpace(r.FormValue("plan")))
	if planRaw == "" || planRaw == "public" {
		planRaw = "standard"
	}
	plan := entitlement.Normalize(planRaw)
	if e != nil || e2 != nil || days < 0 || days > 365 || channel != "telegram" && channel != "web" || planRaw != "standard" && planRaw != "premium" {
		http.Error(w, "invalid grant", 400)
		return
	}
	tx, e := s.Store.DB.Begin()
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	defer tx.Rollback()
	until := ""
	if days > 0 {
		until = time.Now().UTC().AddDate(0, 0, days).Format(time.RFC3339)
	}
	if channel == "telegram" {
		_, e = tx.Exec(`INSERT INTO entitlements(user_id,subscription_until,plan_id) VALUES(?,?,?) ON CONFLICT(user_id) DO UPDATE SET subscription_until=excluded.subscription_until,plan_id=excluded.plan_id`, id, until, string(plan))
	} else {
		_, e = tx.Exec(`UPDATE web_users SET subscription_until=?,plan_id=? WHERE id=?`, until, string(plan), id)
	}
	if e == nil {
		_, e = tx.Exec(`INSERT INTO admin_audit(admin_id,target_id,action,detail,at_utc) VALUES(?,?, 'subscription_grant',?,?)`, profile.AdminID, id, fmt.Sprintf("channel=%s days=%d plan=%s", channel, days, plan), time.Now().UTC().Format(time.RFC3339))
	}
	if e != nil || tx.Commit() != nil {
		http.Error(w, "grant failed", 400)
		return
	}
	jsonOut(w, map[string]any{"ok": true, "subscription_until": until})
}
func (s *Server) config(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		if !s.guard(w, r) {
			return
		}
		v, e := s.Store.ActiveConfig()
		if e != nil {
			http.Error(w, "database error", 500)
			return
		}
		jsonOut(w, v)
		return
	}
	if !s.mutation(w, r) {
		return
	}
	version, e := strconv.Atoi(r.FormValue("version"))
	if e != nil || s.Store.SetConfig(profile.AdminID, version) != nil {
		http.Error(w, "invalid version", 400)
		return
	}
	jsonOut(w, map[string]any{"ok": true})
}
func (s *Server) configDraft(w http.ResponseWriter, r *http.Request) {
	if !s.mutation(w, r) {
		return
	}
	engine, e1 := strconv.Atoi(r.FormValue("engine_candles"))
	model, e2 := strconv.Atoi(r.FormValue("model_candles"))
	horizon, e3 := strconv.Atoi(r.FormValue("horizon_candles"))
	maxDuration, e4 := strconv.Atoi(r.FormValue("max_duration_seconds"))
	if r.FormValue("max_duration_seconds") == "" {
		maxDuration = 180
		e4 = nil
	}
	responseTokens, e5 := strconv.Atoi(r.FormValue("response_token_budget"))
	if r.FormValue("response_token_budget") == "" {
		responseTokens = 4096
		e5 = nil
	}
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil {
		http.Error(w, "invalid numeric configuration", 400)
		return
	}
	c, e := s.Store.DraftConfig(profile.AdminID, engine, model, horizon, maxDuration, responseTokens)
	if e != nil {
		http.Error(w, "invalid analysis limits", 400)
		return
	}
	jsonOut(w, c)
}
func (s *Server) webPlan(w http.ResponseWriter, r *http.Request) {
	if s.Web == nil {
		http.Error(w, "web unavailable", 503)
		return
	}
	if r.Method == "GET" {
		if !s.guard(w, r) {
			return
		}
		price, e := s.Web.Store.Price()
		if e != nil {
			jsonOut(w, map[string]any{"configured": false})
			return
		}
		jsonOut(w, map[string]any{"configured": true, "price_wei": price.String()})
		return
	}
	if !s.mutation(w, r) {
		return
	}
	if e := s.Web.Store.SetPrice(profile.AdminID, r.FormValue("price_bnb")); e != nil {
		http.Error(w, "invalid price", 400)
		return
	}
	jsonOut(w, map[string]any{"ok": true})
}
func (s *Server) webPayments(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r) {
		return
	}
	if s.Web == nil {
		http.Error(w, "web unavailable", 503)
		return
	}
	v, e := s.Web.Store.ReviewPayments()
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	jsonOut(w, v)
}
func (s *Server) webUsers(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r) {
		return
	}
	if s.Web == nil {
		http.Error(w, "web unavailable", 503)
		return
	}
	v, e := s.Web.Store.AdminUsers()
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	jsonOut(w, v)
}
