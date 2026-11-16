package webapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
	"tradebot/internal/bnb"
	"tradebot/internal/profile"
)

func TestBrowserFlowInvoiceToAnalysis(t *testing.T) {
	t.Setenv("WALLET_KEY_ENCRYPTION_KEY", strings.Repeat("23", 32))
	p, s := testStores(t)
	defer p.DB.Close()
	tgID := int64(78001)
	_, _ = p.Ensure(tgID)
	chain := &fakeChain{head: 300, chainID: 56, blocks: map[uint64]bnb.Block{}, receipts: map[string]bnb.Receipt{}}
	billing := &Billing{Store: s, Profile: p, Deriver: testDeriver(t), Chain: chain, ExpectedChainID: 56, Confirmations: 2, ReferenceVerified: true}
	billing.TestInvoicesEnabled = true
	billing.TestEmail = "browser@example.com"
	state, e := p.IssueTelegramLoginNonce(time.Now())
	if e != nil {
		t.Fatal(e)
	}
	webID, e := p.TelegramLoginOnce(state, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", tgID, "Browser", "", "browser", "", time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB.Exec(`UPDATE web_users SET email=? WHERE id=?`, "browser@example.com", webID); e != nil {
		t.Fatal(e)
	}
	seedCheckoutWallet(t, p, s, webID, tgID)
	session, e := s.CreateSessionForUser(webID, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	h := (&Handler{Store: s, Billing: billing, Profile: p, Analysis: &AnalysisService{Store: s, Config: p, Engine: fakeAnalyzer{}, Context: context.Background()}}).Routes()
	post := func(path string, form url.Values, cookie string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
		r.Header.Set("Origin", "http://example.test")
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Host = "example.test"
		if cookie != "" {
			r.Header.Set("Cookie", cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	cookie := "mr_web=" + session
	if w := post("/app/api/invoice", url.Values{}, cookie); w.Code != 503 {
		t.Fatalf("invoice allowed before price %d", w.Code)
	}
	if e := s.SetPrice(profile.AdminID, "0.01"); e != nil {
		t.Fatal(e)
	}
	w := post("/app/api/invoice", url.Values{}, cookie)
	if w.Code != 200 {
		t.Fatalf("invoice response %d %s", w.Code, w.Body.String())
	}
	var inv Invoice
	if e := json.Unmarshal(w.Body.Bytes(), &inv); e != nil {
		t.Fatal(e)
	}
	duplicate := post("/app/api/invoice", url.Values{}, cookie)
	var duplicateInvoice Invoice
	if duplicate.Code != 200 || json.Unmarshal(duplicate.Body.Bytes(), &duplicateInvoice) != nil || duplicateInvoice.ID != inv.ID {
		t.Fatalf("repeat checkout did not reuse pending invoice: %d %s", duplicate.Code, duplicate.Body.String())
	}
	var sharedInvoices, parallelInvoices int
	_ = p.DB.QueryRow(`SELECT count(*) FROM telegram_invoices WHERE telegram_id=?`, tgID).Scan(&sharedInvoices)
	_ = s.DB.QueryRow(`SELECT count(*) FROM web_invoices WHERE user_id=?`, webID).Scan(&parallelInvoices)
	if sharedInvoices != 1 || parallelInvoices != 0 {
		t.Fatalf("invoice storage: shared=%d parallel=%d", sharedInvoices, parallelInvoices)
	}
	addPayment(chain, 301, inv.CreatedAt.Add(time.Minute), inv.Address, "0x2386f26fc10000", 11)
	chain.head = 302
	if _, e := billing.ScanOnce(context.Background()); e != nil {
		t.Fatal(e)
	}
	w = post("/app/api/analyze", url.Values{"symbol": {"XAU/USD"}, "timeframe": {"5min"}, "style": {"day"}}, cookie)
	if w.Code != 202 {
		t.Fatalf("analyze response %d %s", w.Code, w.Body.String())
	}
	var accepted struct {
		ID string `json:"analysis_id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &accepted)
	for i := 0; i < 30; i++ {
		r := httptest.NewRequest("GET", "/app/api/analysis?id="+accepted.ID, nil)
		r.Header.Set("Cookie", cookie)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, r)
		if rr.Code == 200 && strings.Contains(rr.Body.String(), "report_html") && strings.Contains(rr.Body.String(), "تحلیل بازار") && strings.Contains(rr.Body.String(), "شماره پیگیری تحلیل") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("report not visible")
}

func TestFinancialEndpointsUseLinkedIdentityAndNeverProvisionWallet(t *testing.T) {
	t.Setenv("WALLET_KEY_ENCRYPTION_KEY", strings.Repeat("24", 32))
	p, s := testStores(t)
	defer p.DB.Close()
	owner, err := s.Register("owner@example.com", "owner-password-long")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Register("other-owner@example.com", "other-password-long")
	if err != nil {
		t.Fatal(err)
	}
	const tgOwner, tgOther int64 = 81001, 81002
	ownerAddress := seedCheckoutWallet(t, p, s, owner, tgOwner)
	if _, err = p.Ensure(tgOther); err != nil {
		t.Fatal(err)
	}
	link, err := p.IssueWebLink(tgOther, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = p.ConsumeWebLink(link, other, time.Now()); err != nil {
		t.Fatal(err)
	}
	ownerSession, _ := s.CreateSessionForUser(owner, time.Now())
	otherSession, _ := s.CreateSessionForUser(other, time.Now())
	invoice, err := p.CreateTelegramInvoice(tgOwner, ownerAddress, "1000000000000000", 56, time.Now().Add(15*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	h := (&Handler{Store: s, Profile: p, Billing: &Billing{Store: s, Profile: p}}).Routes()
	get := func(path, session string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(&http.Cookie{Name: "mr_web", Value: session})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	owned := get("/app/api/billing/history", ownerSession)
	if owned.Code != 200 || !strings.Contains(owned.Body.String(), invoice.ID) {
		t.Fatalf("owner history: %d %s", owned.Code, owned.Body.String())
	}
	isolated := get("/app/api/billing/history", otherSession)
	if isolated.Code != 200 || strings.Contains(isolated.Body.String(), invoice.ID) {
		t.Fatalf("cross-account history: %d %s", isolated.Code, isolated.Body.String())
	}
	crossInvoice := get("/app/api/invoice?id="+invoice.ID, otherSession)
	if crossInvoice.Code != http.StatusNotFound {
		t.Fatalf("cross-account invoice returned %d", crossInvoice.Code)
	}
	activity := get("/app/api/wallet/activity", otherSession)
	if activity.Code != http.StatusNotFound || strings.Contains(activity.Body.String(), ownerAddress) {
		t.Fatalf("cross-account wallet activity: %d %s", activity.Code, activity.Body.String())
	}
	me := get("/app/api/me", otherSession)
	if me.Code != 200 {
		t.Fatalf("account API: %d %s", me.Code, me.Body.String())
	}
	var walletCount int
	if err = p.DB.QueryRow(`SELECT count(*) FROM telegram_wallets WHERE telegram_id=?`, tgOther).Scan(&walletCount); err != nil || walletCount != 0 {
		t.Fatalf("website visit provisioned wallet: count=%d err=%v", walletCount, err)
	}
	otherAddress := seedCheckoutWallet(t, p, s, other, tgOther)
	otherHash := "0x" + strings.Repeat("b", 64)
	_, err = p.DB.Exec(`INSERT INTO web_payment_events(tx_hash,address,amount_wei,block_number,block_time,classification,seen_at) VALUES(?,?,?,1,?,?,?)`, otherHash, otherAddress, "1000000000000000", time.Now().UTC().Format(time.RFC3339), "paid_telegram", time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	otherActivity := get("/app/api/wallet/activity", otherSession)
	if otherActivity.Code != 200 || !strings.Contains(otherActivity.Body.String(), otherHash) {
		t.Fatalf("own wallet activity missing: %d %s", otherActivity.Code, otherActivity.Body.String())
	}
	ownerActivity := get("/app/api/wallet/activity", ownerSession)
	if ownerActivity.Code != 200 || strings.Contains(ownerActivity.Body.String(), otherHash) {
		t.Fatalf("cross-account wallet activity leaked: %d %s", ownerActivity.Code, ownerActivity.Body.String())
	}
}

func TestDurableRateLimitIsAtomic(t *testing.T) {
	_, s := testStores(t)
	defer s.DB.Close()
	user, err := s.Register("limit@example.com", "rate-limit-password")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, e := s.AllowUserRequest(user, "invoice_status", 8, time.Minute, time.Unix(1000, 0))
			if e != nil {
				t.Error(e)
				return
			}
			if ok {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != 8 {
		t.Fatalf("allowed %d concurrent requests; want 8", allowed)
	}
}
