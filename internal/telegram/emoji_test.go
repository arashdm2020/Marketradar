package telegram

import (
	"strings"
	"testing"
	"time"

	"tradebot/internal/domain"
	"tradebot/internal/profile"
)

func TestEmojiRegistryExactIDs(t *testing.T) {
	want := map[EmojiKey]string{
		EmojiBNB: "4956574641075258382", EmojiBuy: "5839260144902344766", EmojiSell: "5841647064387162728", EmojiInvoice: "5444856076954520455", EmojiSuccess: "5278482869407198831", EmojiFailure: "5276429278039258798", EmojiAttention: "5949361713139683377", EmojiRisk: "5330194932781050507", EmojiEntryPoint: "5370726448959085259", EmojiRiskReward: "5422394304234536556", EmojiTakeProfit: "5386688235353166072", EmojiStopLoss: "5244795211816650110", EmojiCancellation: "5161208387957950108", EmojiExpiration: "5298961058726175115", EmojiIfCondition: "4927486932113425461", EmojiDigit1: "5393089050884202480", EmojiDigit2: "5393392851100919543", EmojiDigit3: "5393210254861294826", EmojiDigit4: "5393501067096908920", EmojiDigit5: "5393420098373443978", EmojiDigit6: "5393295054695588974", EmojiDigit7: "5393480575807939250", EmojiDigit8: "5393131064254293974", EmojiDigit9: "5393216847636102886", EmojiDigit0: "5393565105059291505", EmojiMarketStructure: "5307726889408809792", EmojiAscending: "5429463966432643712", EmojiDescending: "5429165728198576891", EmojiTimeframe: "5348236797606379943", EmojiMarketAnalysis: "5190924609548999975", EmojiLastClosedCandle: "5003748650594075621", EmojiFinalDecision: "5215313353706057331", EmojiWait: "5255765065096774716",
	}
	// The supplied table contains 33 rows even though its prose says 31.
	// Preserve every listed semantic key and exact ID.
	if len(emojiRegistry) != 33 || len(want) != 33 {
		t.Fatalf("registry size=%d want=33", len(emojiRegistry))
	}
	for key, id := range want {
		if got := emojiRegistry[key]; got.ID != id || got.Fallback == "" {
			t.Fatalf("%s=%q fallback=%q", key, got.ID, got.Fallback)
		}
	}
}

func TestCustomEmojiHTMLAndFallback(t *testing.T) {
	got := customEmoji(EmojiBuy)
	if got != `<tg-emoji emoji-id="5839260144902344766">🟢</tg-emoji>` {
		t.Fatal(got)
	}
	if !strings.Contains(customEmoji(EmojiFailure), "❌") {
		t.Fatal("unicode fallback missing")
	}
	if customEmoji(EmojiKey(`<script>`)) != "" {
		t.Fatal("unknown key emitted markup")
	}
}

func TestButtonsNeverContainCustomEmojiIDs(t *testing.T) {
	buttons := []map[string]any{button("📈 تحلیل بازار", "analyze"), copyButton("📋 کپی", "0xabc")}
	for _, b := range buttons {
		raw := b["text"].(string)
		if strings.Contains(raw, "emoji-id") {
			t.Fatal("custom emoji leaked into button")
		}
		if cb, ok := b["callback_data"].(string); ok && strings.Contains(cb, "495657") {
			t.Fatal("emoji id leaked into callback")
		}
	}
}

func TestAnalysisTemplatesBUYSELLWAITAndEscaping(t *testing.T) {
	for _, final := range []string{"BUY", "SELL", "WAIT"} {
		d := domain.Decision{Final: final, Snapshot: domain.Snapshot{AnalysisTimeUTC: time.Unix(1, 0), LastCandleTimeUTC: time.Unix(1, 0), Quant: domain.Quant{Alignment: "BULLISH"}}, Proposed: domain.Signal{Signal: final}}
		got := Render(d, `XAU/USD<script>alert(1)</script>`, "5min", "scalp", profile.User{Timezone: "UTC"})
		if strings.Contains(got, "<script>") || !strings.Contains(got, "&lt;script&gt;") {
			t.Fatalf("dynamic HTML not escaped for %s", final)
		}
		if !strings.Contains(got, "<tg-emoji emoji-id=") {
			t.Fatalf("custom emoji missing for %s", final)
		}
		if strings.Contains(got, "&lt;tg-emoji") {
			t.Fatalf("trusted emoji markup double escaped for %s", final)
		}
	}
}
