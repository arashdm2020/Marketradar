package telegram

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"tradebot/internal/profile"
)

func TestLegacyProfileBackfillPreservesIdentityAndFinancialRecords(t *testing.T) {
	store, err := profile.Open(t.TempDir() + "/legacy.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	for _, id := range []int64{101, 102, 103, 104, 105} {
		if _, err := store.Ensure(id); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.UpdateTelegramSender(105, "Newer", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`INSERT INTO telegram_wallets(telegram_id,derivation_index,address,created_at) VALUES(101,0,'0x1111111111111111111111111111111111111111',?)`, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`INSERT INTO entitlements(user_id,subscription_until,plan_id) VALUES(101,'2030-01-01T00:00:00Z','standard')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`INSERT INTO telegram_invoices(id,telegram_id,address,price_wei,chain_id,created_at,expires_at) VALUES('legacy-invoice',101,'0x1111111111111111111111111111111111111111','1000000000000000',56,?,?)`, time.Now().UTC().Format(time.RFC3339), time.Now().Add(time.Hour).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	profiles := map[int64]ChatProfile{
		101: {ID: 101, Type: "private", FirstName: "Ada", LastName: "Lovelace"},
		102: {ID: 102, Type: "private", FirstName: "Sara"},
		103: {ID: 103, Type: "private", Username: "onlyhandle"},
		104: {ID: 104, Type: "private"},
		105: {ID: 105, Type: "private", FirstName: "Old", LastName: "Family"},
	}
	lookups := 0
	lookup := func(_ context.Context, id int64) (ChatProfile, error) { lookups++; return profiles[id], nil }
	result, err := BackfillProfiles(context.Background(), store, 10, false, lookup)
	if err != nil || result.Inspected != 5 || result.Recovered != 4 || result.Unavailable != 1 {
		t.Fatalf("dry run: %+v %v", result, err)
	}
	if p, _ := store.TelegramProfile(101); p.FirstName != "" {
		t.Fatal("dry run wrote profile")
	}
	result, err = BackfillProfiles(context.Background(), store, 10, true, lookup)
	if err != nil || result.Inspected != 5 || result.Recovered != 4 || result.Unavailable != 1 {
		t.Fatalf("apply: %+v %v", result, err)
	}
	if p, _ := store.TelegramProfile(105); p.FirstName != "Newer" || p.LastName != "Family" {
		t.Fatalf("newer profile overwritten: %+v", p)
	}
	admin, err := store.AdminUsers(profile.AdminID)
	if err != nil || len(admin) != 5 {
		t.Fatalf("admin users: %d %v", len(admin), err)
	}
	if admin[0].DisplayName != "Ada Lovelace" || admin[1].DisplayName != "Sara" || admin[2].Username != "onlyhandle" || admin[3].DisplayName != "" {
		t.Fatalf("legacy names not mapped: %+v", admin)
	}
	result, err = BackfillProfiles(context.Background(), store, 10, true, lookup)
	if err != nil || result.Inspected != 0 || lookups != 10 {
		t.Fatalf("repeat made calls: %+v lookups=%d %v", result, lookups, err)
	}
	for table, want := range map[string]int{"users": 5, "telegram_wallets": 1, "entitlements": 1, "telegram_invoices": 1} {
		var got int
		if err := store.DB.QueryRow("SELECT count(*) FROM " + table).Scan(&got); err != nil || got != want {
			t.Fatalf("%s count=%d err=%v", table, got, err)
		}
	}
}

func TestBotPersistsSenderMetadataOnMessagesAndCallbacks(t *testing.T) {
	store, err := profile.Open(t.TempDir() + "/sender.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	bot := Bot{Store: store, Token: "test", HTTP: &http.Client{Transport: &uxTransport{}}}
	bot.handle(context.Background(), telegramUpdate(t, `{"message":{"text":"/start","from":{"id":901,"first_name":"Ali","last_name":"Reza","username":"ali1"},"chat":{"id":901,"type":"private"}}}`))
	bot.handle(context.Background(), telegramUpdate(t, `{"callback_query":{"id":"cb","data":"home","from":{"id":901,"first_name":"Ali","username":"ali2"},"message":{"message_id":71,"chat":{"id":901,"type":"private"}}}}`))
	p, err := store.TelegramProfile(901)
	if err != nil || p.DisplayName() != "Ali Reza" || p.Username != "ali2" {
		t.Fatalf("sender metadata: %+v %v", p, err)
	}
	if !strings.Contains(p.UpdatedAt, "T") {
		t.Fatal("profile update timestamp missing")
	}
}
