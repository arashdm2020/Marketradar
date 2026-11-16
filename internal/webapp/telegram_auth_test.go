package webapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"tradebot/internal/profile"
)

func signedTelegramPayload(token string, id int64, first, username string, at time.Time) telegramLoginPayload {
	v := telegramLoginPayload{ID: id, FirstName: first, Username: username, AuthDate: at.Unix(), Nonce: "browser-state-nonce-at-least-32-characters"}
	return signTelegramPayload(token, v)
}

func signTelegramPayload(token string, v telegramLoginPayload) telegramLoginPayload {
	values := map[string]string{"id": strconv.FormatInt(v.ID, 10), "first_name": v.FirstName, "auth_date": strconv.FormatInt(v.AuthDate, 10)}
	for key, value := range map[string]string{"last_name": v.LastName, "username": v.Username, "photo_url": v.PhotoURL, "allows_write_to_pm": v.AllowsWriteToPM} {
		if value != "" {
			values[key] = value
		}
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+values[k])
	}
	secret := sha256.Sum256([]byte(token))
	mac := hmac.New(sha256.New, secret[:])
	_, _ = mac.Write([]byte(strings.Join(parts, "\n")))
	v.Hash = hex.EncodeToString(mac.Sum(nil))
	return v
}

func TestTelegramWidgetHMACAndFreshness(t *testing.T) {
	now := time.Now()
	p := signedTelegramPayload("test-bot-token", 42, "A", "a", now)
	if !verifyTelegramLogin(p, "test-bot-token", now) {
		t.Fatal("valid Telegram signature rejected")
	}
	p.Nonce = "different-client-state"
	if !verifyTelegramLogin(p, "test-bot-token", now) {
		t.Fatal("browser state should not alter Telegram signature")
	}
	p.Username = "other"
	if verifyTelegramLogin(p, "test-bot-token", now) {
		t.Fatal("tampered Telegram field accepted")
	}
	p = signedTelegramPayload("test-bot-token", 42, "A", "a", now.Add(-6*time.Minute))
	if verifyTelegramLogin(p, "test-bot-token", now) {
		t.Fatal("stale Telegram assertion accepted")
	}
}

func TestNonceEndpointDoesNotAcceptOrCreateClientIdentity(t *testing.T) {
	p, err := profile.Open(t.TempDir() + "/nonce.db")
	if err != nil {
		t.Fatal(err)
	}
	defer p.DB.Close()
	w, err := NewStore(p.DB)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/app/api/telegram/nonce", strings.NewReader(`{"id":999999}`))
	req.Header.Set("Origin", "http://example.test")
	req.Host = "example.test"
	response := httptest.NewRecorder()
	(&Handler{Store: w, Profile: p}).Routes().ServeHTTP(response, req)
	if response.Code != 200 {
		t.Fatalf("nonce status=%d body=%s", response.Code, response.Body.String())
	}
	var payload map[string]string
	if err = json.NewDecoder(io.LimitReader(response.Body, 1024)).Decode(&payload); err != nil || len(payload["nonce"]) != 64 {
		t.Fatalf("nonce response=%v err=%v", payload, err)
	}
	var users int
	if err = p.DB.QueryRow(`SELECT count(*) FROM users WHERE id=999999`).Scan(&users); err != nil || users != 0 {
		t.Fatalf("client identity created: users=%d err=%v", users, err)
	}
}

func TestTelegramCallbackCreatesSecureSessionAndReplaysFail(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-bot-token")
	p, err := profile.Open(t.TempDir() + "/web-auth.db")
	if err != nil {
		t.Fatal(err)
	}
	defer p.DB.Close()
	const tg int64 = 891122
	if _, err = p.Ensure(tg); err != nil {
		t.Fatal(err)
	}
	const walletAddress = "0x1111111111111111111111111111111111111111"
	if _, err = p.DB.Exec(`INSERT INTO telegram_wallets(telegram_id,derivation_index,address,created_at) VALUES(?,0,?,?)`, tg, walletAddress, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	nonce, err := p.IssueTelegramLoginNonce(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	w, err := NewStore(p.DB)
	if err != nil {
		t.Fatal(err)
	}
	h := (&Handler{Store: w, Profile: p, Billing: &Billing{Chain: &fakeChain{chainID: 56}, ExpectedChainID: 56}}).Routes()
	login := signedTelegramPayload("test-bot-token", tg, "Test User", "testuser", time.Now())
	login.LastName = "Profile"
	login.PhotoURL = "https://t.me/i/userpic/test.jpg"
	login = signTelegramPayload("test-bot-token", login)
	login.Nonce = nonce
	body, _ := json.Marshal(login)
	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/app/api/telegram/callback", strings.NewReader(string(body)))
		req.Header.Set("Origin", "http://example.test")
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Host = "example.test"
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}
	resp := call()
	if resp.Code != 200 {
		t.Fatalf("login status=%d body=%s", resp.Code, resp.Body.String())
	}
	cookie := resp.Result().Cookies()[0]
	if cookie.Name != "mr_web" || !cookie.HttpOnly || !cookie.Secure || cookie.Path != "/" {
		t.Fatalf("insecure or scoped session cookie: %+v", cookie)
	}
	if replay := call(); replay.Code != 401 {
		t.Fatalf("replay status=%d", replay.Code)
	}
	me := httptest.NewRequest("GET", "/app/api/me", nil)
	me.AddCookie(cookie)
	out := httptest.NewRecorder()
	h.ServeHTTP(out, me)
	if out.Code != 200 || !strings.Contains(out.Body.String(), `"telegram_id":891122`) || !strings.Contains(out.Body.String(), `"last_name":"Profile"`) || !strings.Contains(out.Body.String(), `"username":"testuser"`) || !strings.Contains(out.Body.String(), `"photo_url":"https://t.me/i/userpic/test.jpg"`) || !strings.Contains(out.Body.String(), `"daily_allowance":1`) || !strings.Contains(out.Body.String(), walletAddress) || !strings.Contains(out.Body.String(), `"balance_bnb":"0"`) || strings.Contains(out.Body.String(), "private_key") {
		t.Fatalf("account response status=%d body=%s", out.Code, out.Body.String())
	}
	updateBody := `{"phone_number":"+1 (415) 555-2671","display_name":"Personal Name","consent":true,"telegram_id":999999}`
	update := httptest.NewRequest("POST", "/app/api/profile", strings.NewReader(updateBody))
	update.Header.Set("Origin", "http://example.test")
	update.Header.Set("Content-Type", "application/json")
	update.Host = "example.test"
	update.AddCookie(cookie)
	updateResult := httptest.NewRecorder()
	h.ServeHTTP(updateResult, update)
	stored, profileErr := p.TelegramProfile(tg)
	if updateResult.Code != 200 || profileErr != nil || stored.PhoneNumber != "+14155552671" || stored.PhoneVerified || stored.ProvidedName != "Personal Name" {
		t.Fatalf("owner phone update status=%d profile=%+v err=%v", updateResult.Code, stored, profileErr)
	}
	otherTelegramID := int64(891123)
	if _, err = p.Ensure(otherTelegramID); err != nil {
		t.Fatal(err)
	}
	otherNonce, err := p.IssueTelegramLoginNonce(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	otherWebID, err := p.TelegramLoginOnce(otherNonce, strings.Repeat("9", 64), otherTelegramID, "Other", "", "", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	otherSession, err := w.CreateSessionForUser(otherWebID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	otherUpdate := httptest.NewRequest("POST", "/app/api/profile", strings.NewReader(`{"phone_number":"+44 20 7946 0958","consent":true,"telegram_id":891122}`))
	otherUpdate.Header.Set("Origin", "http://example.test")
	otherUpdate.Host = "example.test"
	otherUpdate.AddCookie(&http.Cookie{Name: "mr_web", Value: otherSession})
	otherResult := httptest.NewRecorder()
	h.ServeHTTP(otherResult, otherUpdate)
	ownerAfter, _ := p.TelegramProfile(tg)
	otherAfter, _ := p.TelegramProfile(otherTelegramID)
	if otherResult.Code != 200 || ownerAfter.PhoneNumber != "+14155552671" || otherAfter.PhoneNumber != "+442079460958" {
		t.Fatalf("client-selected identity was trusted: status=%d owner=%+v other=%+v", otherResult.Code, ownerAfter, otherAfter)
	}
	unauthorized := httptest.NewRequest("POST", "/app/api/profile", strings.NewReader(updateBody))
	unauthorized.Header.Set("Origin", "http://example.test")
	unauthorized.Host = "example.test"
	unauthorizedResult := httptest.NewRecorder()
	h.ServeHTTP(unauthorizedResult, unauthorized)
	if unauthorizedResult.Code != 401 {
		t.Fatalf("unauthenticated profile update status=%d", unauthorizedResult.Code)
	}
	var walletCount int
	if err = p.DB.QueryRow(`SELECT count(*) FROM telegram_wallets WHERE telegram_id=?`, tg).Scan(&walletCount); err != nil || walletCount != 1 {
		t.Fatalf("Telegram login changed existing wallet count: count=%d err=%v", walletCount, err)
	}
	for _, path := range []string{"/app/login", "/app/register"} {
		req := httptest.NewRequest("POST", path, nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != 410 {
			t.Fatalf("legacy auth %s status=%d", path, rr.Code)
		}
	}
}

func TestTelegramCallbackRejectsMissingToken(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	p, err := profile.Open(t.TempDir() + "/web-auth-empty.db")
	if err != nil {
		t.Fatal(err)
	}
	defer p.DB.Close()
	w, err := NewStore(p.DB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Ensure(5); err != nil {
		t.Fatal(err)
	}
	nonce, err := p.IssueTelegramLoginNonce(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	login := signedTelegramPayload("test-bot-token", 5, "A", "", time.Now())
	login.Nonce = nonce
	body, _ := json.Marshal(login)
	req := httptest.NewRequest("POST", "/app/api/telegram/callback", strings.NewReader(string(body)))
	req.Header.Set("Origin", "http://example.test")
	req.Host = "example.test"
	rr := httptest.NewRecorder()
	(&Handler{Store: w, Profile: p}).Routes().ServeHTTP(rr, req)
	if rr.Code != 401 {
		t.Fatalf("expected authentication rejection, got %d", rr.Code)
	}
}

func TestLegacyPasswordSessionIsNotAnAuthenticationMethod(t *testing.T) {
	p, err := profile.Open(t.TempDir() + "/legacy-session.db")
	if err != nil {
		t.Fatal(err)
	}
	defer p.DB.Close()
	w, err := NewStore(p.DB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Register("legacy@example.test", "long-legacy-password-123"); err != nil {
		t.Fatal(err)
	}
	token, err := w.Login("legacy@example.test", "long-legacy-password-123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Session(token); err == nil {
		t.Fatal("legacy password session authenticated")
	}
}
