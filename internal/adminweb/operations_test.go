package adminweb

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
	"tradebot/internal/profile"
	"tradebot/internal/webapp"
)

func TestOperationsAuthorizationAndFinanceNoSweepDoubleCount(t *testing.T) {
	p, e := profile.Open(t.TempDir() + "/admin.db")
	if e != nil {
		t.Fatal(e)
	}
	defer p.DB.Close()
	_, e = webapp.NewStore(p.DB)
	if e != nil {
		t.Fatal(e)
	}
	_, e = p.DB.Exec(`INSERT INTO web_users(id,email,password_hash,created_at) VALUES(1,'test@example.invalid',X'00','2026-11-14T00:00:00Z')`)
	if e != nil {
		t.Fatal(e)
	}
	_, e = p.DB.Exec(`INSERT INTO web_invoices(id,user_id,address,price_wei,chain_id,created_at,expires_at,start_block,status,tx_hash) VALUES('inv1',1,'0xabc','1000000000000000000',56,'2026-11-14T00:00:00Z','2026-11-14T00:15:00Z',1,'paid','tx1')`)
	if e != nil {
		t.Fatal(e)
	}
	_, e = p.DB.Exec(`INSERT INTO web_payment_events(tx_hash,invoice_id,address,amount_wei,block_number,block_time,classification,seen_at) VALUES('tx1','inv1','0xabc','1000000000000000000',2,'2026-11-14T00:05:00Z','paid','2026-11-14T00:06:00Z')`)
	if e != nil {
		t.Fatal(e)
	}
	_, e = p.DB.Exec(`INSERT INTO treasury_jobs(payment_hash,source,amount_wei,status,fee_wei,updated_at) VALUES('tx1','0xabc','1000000000000000000','transferred','1000000000000000','2026-11-14T00:10:00Z')`)
	if e != nil {
		t.Fatal(e)
	}
	s := New(p, "test-token", "bot")
	h := s.Handler()
	req := httptest.NewRequest("GET", "/api/operations/finance", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatalf("unauthorized=%d", w.Code)
	}
	value := strconv.FormatInt(profile.AdminID, 10) + "." + strconv.FormatInt(time.Now().Unix(), 10)
	req.AddCookie(&http.Cookie{Name: "mr_admin", Value: value + "." + s.signed(value)})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("finance=%d %s", w.Code, w.Body.String())
	}
	var data map[string]any
	if e = json.Unmarshal(w.Body.Bytes(), &data); e != nil {
		t.Fatal(e)
	}
	if data["confirmed_wei"] != "1000000000000000000" || data["recognized_revenue_wei"] != "1000000000000000000" || data["settled_wei"] != "1000000000000000000" {
		t.Fatalf("double count or missing data: %+v", data)
	}
	for _, path := range []string{"/api/session", "/api/operations/users", "/api/operations/subscriptions", "/api/operations/payments", "/api/operations/wallets", "/api/operations/analyses", "/api/operations/system", "/api/operations/audit"} {
		q := httptest.NewRequest("GET", path, nil)
		q.AddCookie(&http.Cookie{Name: "mr_admin", Value: value + "." + s.signed(value)})
		out := httptest.NewRecorder()
		h.ServeHTTP(out, q)
		if out.Code != 200 {
			t.Errorf("%s: %d %s", path, out.Code, out.Body.String())
		}
	}
	grant := httptest.NewRequest("POST", "/api/subscription/grant", strings.NewReader("user_id=1&channel=web&days=7"))
	grant.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	grant.Header.Set("Origin", "http://example.com")
	grant.Host = "example.com"
	grant.AddCookie(&http.Cookie{Name: "mr_admin", Value: value + "." + s.signed(value)})
	grantOut := httptest.NewRecorder()
	h.ServeHTTP(grantOut, grant)
	if grantOut.Code != 200 {
		t.Fatalf("grant: %d %s", grantOut.Code, grantOut.Body.String())
	}
	var until string
	if e = p.DB.QueryRow(`SELECT subscription_until FROM web_users WHERE id=1`).Scan(&until); e != nil || until == "" {
		t.Fatal("grant was not persisted")
	}
}
