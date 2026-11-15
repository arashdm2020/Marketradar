package adminweb

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
	"tradebot/internal/aimgmt"
	"tradebot/internal/profile"
	"tradebot/internal/treasury"
	"tradebot/internal/webapp"
)

func TestAuthenticatedTreasuryConfigurationPersistsAndActivates(t *testing.T) {
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&q)
		result := "0x38"
		if q.Method == "eth_blockNumber" {
			result = "0x123"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
	defer rpc.Close()
	p, e := profile.Open(t.TempDir() + "/admin.db")
	if e != nil {
		t.Fatal(e)
	}
	defer p.DB.Close()
	ws, e := webapp.NewStore(p.DB)
	if e != nil {
		t.Fatal(e)
	}
	ai, e := aimgmt.New(p.DB, strings.Repeat("ab", 32))
	if e != nil {
		t.Fatal(e)
	}
	billing := &webapp.Billing{Store: ws}
	srv := New(p, "token", "bot")
	srv.AIStore = ai
	srv.Web = &webapp.Handler{Store: ws, Billing: billing}
	srv.Treasury = &treasury.Store{DB: p.DB, Profiles: p}
	value := strconv.FormatInt(profile.AdminID, 10) + "." + strconv.FormatInt(time.Now().Unix(), 10)
	body := "rpc_url=" + rpc.URL + "&confirmations=18&monitoring_seconds=30"
	req := httptest.NewRequest(http.MethodPost, "http://admin.local/api/treasury/config", strings.NewReader(body))
	req.Host = "admin.local"
	req.Header.Set("Origin", "http://admin.local")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "mr_admin", Value: value + "." + srv.signed(value)})
	out := httptest.NewRecorder()
	srv.Handler().ServeHTTP(out, req)
	if out.Code != 200 {
		t.Fatalf("status=%d body=%s", out.Code, out.Body.String())
	}
	var confirmations int
	if e = p.DB.QueryRow(`SELECT confirmations FROM treasury_public_config WHERE id=1`).Scan(&confirmations); e != nil || confirmations != 18 {
		t.Fatalf("not persisted: %d %v", confirmations, e)
	}
	if billing.Confirmations != 18 || billing.ExpectedChainID != 56 || billing.Chain == nil {
		t.Fatal("active billing was not updated")
	}
}
