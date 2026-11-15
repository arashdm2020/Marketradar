package adminweb

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
	"tradebot/internal/aimgmt"
	"tradebot/internal/profile"
	"tradebot/internal/webapp"
)

func TestAdminAuthenticationAndLookup(t *testing.T) {
	store, e := profile.Open(t.TempDir() + "/a.db")
	if e != nil {
		t.Fatal(e)
	}
	defer store.DB.Close()
	_, _ = store.Ensure(22)
	_ = store.CreateRequestState("track-22", "22", "22", "AUD/USD", "30min", "day")
	s := New(store, "test-token", "bot")
	h := s.Handler()
	req := httptest.NewRequest("GET", "/api/analysis?id=track-22", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatalf("unauthenticated: %d", w.Code)
	}
	q := url.Values{"id": {"8697362188"}, "auth_date": {strconv.FormatInt(time.Now().Unix(), 10)}, "first_name": {"Admin"}}
	secret := sha256.Sum256([]byte("test-token"))
	mac := hmac.New(sha256.New, secret[:])
	mac.Write([]byte("auth_date=" + q.Get("auth_date") + "\nfirst_name=Admin\nid=8697362188"))
	q.Set("hash", hex.EncodeToString(mac.Sum(nil)))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/auth/telegram?"+q.Encode(), nil))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("login: %d", w.Code)
	}
	cookie := w.Result().Cookies()[0]
	aiStore, e := aimgmt.New(store.DB, strings.Repeat("cd", 32))
	if e != nil {
		t.Fatal(e)
	}
	if e = aiStore.Seed(aimgmt.Version{Provider: "nvidia", Model: "nvidia/test", MaxTokens: 256, Temperature: 1, ReasoningBudget: 128, ReasoningEffort: "medium"}, "test-key"); e != nil {
		t.Fatal(e)
	}
	s.AIStore = aiStore
	h = s.Handler()
	unauth := httptest.NewRecorder()
	h.ServeHTTP(unauth, httptest.NewRequest("GET", "/api/ai/versions", nil))
	if unauth.Code != 403 {
		t.Fatal("AI versions exposed")
	}
	authorized := httptest.NewRequest("GET", "/api/ai/versions", nil)
	authorized.AddCookie(cookie)
	viewAI := httptest.NewRecorder()
	h.ServeHTTP(viewAI, authorized)
	if viewAI.Code != 200 || strings.Contains(viewAI.Body.String(), "test-key") {
		t.Fatal("AI versions unavailable or key leaked")
	}
	dashboardReq := httptest.NewRequest("GET", "/api/dashboard?channel=telegram", nil)
	dashboardReq.AddCookie(cookie)
	dashboardView := httptest.NewRecorder()
	h.ServeHTTP(dashboardView, dashboardReq)
	if dashboardView.Code != 200 || !strings.Contains(dashboardView.Body.String(), `"analyses":1`) {
		t.Fatal("dashboard counts unavailable")
	}
	page := httptest.NewRequest("GET", "/", nil)
	page.AddCookie(cookie)
	view := httptest.NewRecorder()
	h.ServeHTTP(view, page)
	if view.Code != 200 || !strings.Contains(view.Body.String(), "مبلغ BNB") || !strings.Contains(view.Body.String(), "نمایش JSON کامل") {
		t.Fatal("admin panel layout unavailable")
	}
	req = httptest.NewRequest("GET", "/api/analysis?id=track-22", nil)
	req.AddCookie(cookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("lookup: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "track-22") {
		t.Fatal("tracking number absent")
	}
	form := url.Values{"user_id": {"22"}, "exempt": {"true"}}
	req = httptest.NewRequest("POST", "/api/exemption", strings.NewReader(form.Encode()))
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("exemption update: %d %s", w.Code, w.Body.String())
	}
	users, e := store.AdminUsers(profile.AdminID)
	if e != nil || len(users) != 1 || !users[0].Exempt {
		t.Fatalf("exemption was not persisted: %+v %v", users, e)
	}
	webStore, e := webapp.NewStore(store.DB)
	if e != nil {
		t.Fatal(e)
	}
	s.Web = &webapp.Handler{Store: webStore}
	h = s.Handler()
	form = url.Values{"price_bnb": {"0.015"}}
	req = httptest.NewRequest("POST", "/api/web-plan", strings.NewReader(form.Encode()))
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("admin price update %d", w.Code)
	}
	price, e := webStore.Price()
	if e != nil || price.String() != "15000000000000000" {
		t.Fatalf("price %v %v", price, e)
	}
}
