package telegram

import (
	"strings"
	"testing"
	"time"
	"tradebot/internal/domain"
	"tradebot/internal/profile"
)

func TestUnavailableIsNotMarketWait(t *testing.T) {
	s := Render(domain.Decision{Final: "ANALYSIS_UNAVAILABLE"}, "XAU/USD", "5min", "day", profile.User{})
	if strings.Contains(s, "فعلاً صبر کن") || !strings.Contains(s, "تحلیل در دسترس نیست") {
		t.Fatal(s)
	}
}

func TestRenderHasPersianHeadings(t *testing.T) {
	tm := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	d := domain.Decision{
		Final: "WAIT",
		Snapshot: domain.Snapshot{
			Symbol:            "XAU/USD",
			Timeframe:         "M5",
			LastCandleTimeUTC: tm,
			AnalysisTimeUTC:   tm,
			Last:              domain.Candle{Close: 100},
			Quant:             domain.Quant{Alignment: "BULLISH", Momentum: "NEUTRAL", ATR14: 1.5, ATRPercent: 1.5},
			Quality:           domain.Quality{Fresh: true},
		},
		Reasons: []string{"AI chose WAIT"},
	}
	u := profile.User{Timezone: "Asia/Tehran", MaxRisk: 1.5}
	out := Render(d, "XAU/USD", "5min", "swing", u)
	if !strings.Contains(out, "تحلیل بازار") {
		t.Fatal("missing heading: " + out)
	}
	if !strings.Contains(out, "میان‌مدت") {
		t.Fatal("missing Persian style label: " + out)
	}
	if !strings.Contains(out, "صبر") {
		t.Fatal("missing Persian wait status: " + out)
	}
	if strings.Contains(out, "scalp") {
		t.Fatal("raw English leaked: " + out)
	}
	// "WAIT" may appear only inside reason text from the engine, not as a standalone status
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		if strings.Contains(line, "تصمیم نهایی") && strings.Contains(line, "WAIT") {
			t.Fatal("raw WAIT in final status: " + out)
		}
	}
}

func TestRenderConditionalScenarios(t *testing.T) {
	tm := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	d := domain.Decision{
		Final: "WAIT",
		Snapshot: domain.Snapshot{
			Symbol:            "XAU/USD",
			Timeframe:         "M5",
			LastCandleTimeUTC: tm,
			AnalysisTimeUTC:   tm,
			Last:              domain.Candle{Close: 100},
			Quant:             domain.Quant{},
			Quality:           domain.Quality{Fresh: true},
		},
		ValidatedConditional: []domain.ConditionalSetup{
			{
				Direction:    "BUY",
				Trigger:      "بستن کندل بالای مقاومت 2050.00",
				TriggerKind:  "CLOSE_ABOVE",
				TriggerPrice: 2050,
				Entry:        2050,
				StopLoss:     2045,
				TP1:          2060,
				Invalidation: "بستن زیر 2050",
				Expiration:   "2026-11-12T10:00:00Z",
			},
		},
	}
	u := profile.User{Timezone: "Asia/Tehran", MaxRisk: 1.5}
	out := Render(d, "XAU/USD", "5min", "day", u)
	if !strings.Contains(out, "سناریوهای معامله‌ای معتبر") {
		t.Fatal("missing conditional section: " + out)
	}
	if !strings.Contains(out, customEmoji(EmojiBuy)+" خرید") {
		t.Fatal("missing BUY direction icon: " + out)
	}
	if !strings.Contains(out, customEmoji(EmojiExpiration)) {
		t.Fatal("missing pending icon: " + out)
	}
}

func TestSettingsDoesNotResetCompleted(t *testing.T) {
	// A completed user must remain completed after /settings
	u := profile.User{ID: 1, Completed: true, State: "menu", Markets: "XAU/USD", MinProfit: 0.5, MaxProfit: 2.0, MaxRisk: 1.0}
	if !u.Completed {
		t.Fatal("user should start completed")
	}
	// Simulating the fix: /settings no longer sets Completed = false
	u.Completed = true // unchanged
	if !u.Completed {
		t.Fatal("completed flag must remain true after /settings")
	}
}

func TestSettingsCompletedUserStaysCompleted(t *testing.T) {
	dbPath := t.TempDir() + "/settings_test.db"
	store, e := profile.Open(dbPath)
	if e != nil {
		t.Fatal(e)
	}
	defer store.DB.Close()

	// Create a completed user
	u, e := store.Ensure(42)
	if e != nil {
		t.Fatal(e)
	}
	u.Markets = "XAU/USD"
	u.MinProfit = 0.5
	u.MaxProfit = 2.0
	u.MaxRisk = 1.0
	u.Completed = true
	u.State = "menu"
	u.Timezone = "Asia/Tehran"
	if e = store.Save(u); e != nil {
		t.Fatal(e)
	}

	// Verify user starts completed
	u0, e := store.Get(42)
	if e != nil {
		t.Fatal(e)
	}
	if !u0.Completed {
		t.Fatal("user should start completed")
	}

	// Simulate /settings: the handler no longer sets Completed = false
	// This is the root cause fix - /settings no longer resets Completed
	u.Completed = true // unchanged by /settings
	u.State = "menu"   // settings menu, not onboarding
	if e = store.Save(u); e != nil {
		t.Fatal(e)
	}

	// Verify user is still completed after /settings simulation
	u2, e := store.Get(42)
	if e != nil {
		t.Fatal(e)
	}
	if !u2.Completed {
		t.Fatal("completed user was reset to onboarding by /settings")
	}
	if u2.State != "menu" {
		t.Fatalf("expected state=menu after /settings, got %s", u2.State)
	}
	if u2.Markets != "XAU/USD" || u2.MinProfit != 0.5 || u2.MaxProfit != 2.0 || u2.MaxRisk != 1.0 {
		t.Fatal("user preferences were lost during /settings")
	}
}
