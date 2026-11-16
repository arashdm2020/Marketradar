package webapp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
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
	chain := &fakeChain{head: 300, chainID: 97, blocks: map[uint64]bnb.Block{}, receipts: map[string]bnb.Receipt{}}
	billing := &Billing{Store: s, Deriver: testDeriver(t), Chain: chain, ExpectedChainID: 97, Confirmations: 2, ReferenceVerified: true}
	billing.TestInvoicesEnabled = true
	billing.TestEmail = "browser@example.com"
	h := (&Handler{Store: s, Billing: billing, Analysis: &AnalysisService{Store: s, Config: p, Engine: fakeAnalyzer{}, Context: context.Background()}}).Routes()
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
	register := post("/app/register", url.Values{"email": {"browser@example.com"}, "password": {"long-browser-password"}}, "")
	if register.Code != 303 {
		t.Fatalf("register %d", register.Code)
	}
	cookie := register.Result().Cookies()[0].String()
	webID, _ := s.Session(register.Result().Cookies()[0].Value)
	token, _ := p.IssueWebLink(tgID, time.Now())
	if e := p.ConsumeWebLink(token, webID, time.Now()); e != nil {
		t.Fatal(e)
	}
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
