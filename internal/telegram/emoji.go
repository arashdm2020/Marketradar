package telegram

import "html"

// EmojiKey is a semantic presentation token. IDs live only in this registry;
// callbacks and business data must never contain them.
type EmojiKey string

const (
	EmojiBNB              EmojiKey = "BNB"
	EmojiBuy              EmojiKey = "BUY"
	EmojiSell             EmojiKey = "SELL"
	EmojiInvoice          EmojiKey = "INVOICE"
	EmojiSuccess          EmojiKey = "SUCCESS"
	EmojiFailure          EmojiKey = "FAILURE"
	EmojiAttention        EmojiKey = "ATTENTION"
	EmojiRisk             EmojiKey = "RISK"
	EmojiEntryPoint       EmojiKey = "ENTRY_POINT"
	EmojiRiskReward       EmojiKey = "RISK_REWARD"
	EmojiTakeProfit       EmojiKey = "TAKE_PROFIT"
	EmojiStopLoss         EmojiKey = "STOP_LOSS"
	EmojiCancellation     EmojiKey = "CANCELLATION"
	EmojiExpiration       EmojiKey = "EXPIRATION"
	EmojiIfCondition      EmojiKey = "IF_CONDITION"
	EmojiDigit1           EmojiKey = "DIGIT_1"
	EmojiDigit2           EmojiKey = "DIGIT_2"
	EmojiDigit3           EmojiKey = "DIGIT_3"
	EmojiDigit4           EmojiKey = "DIGIT_4"
	EmojiDigit5           EmojiKey = "DIGIT_5"
	EmojiDigit6           EmojiKey = "DIGIT_6"
	EmojiDigit7           EmojiKey = "DIGIT_7"
	EmojiDigit8           EmojiKey = "DIGIT_8"
	EmojiDigit9           EmojiKey = "DIGIT_9"
	EmojiDigit0           EmojiKey = "DIGIT_0"
	EmojiMarketStructure  EmojiKey = "MARKET_STRUCTURE"
	EmojiAscending        EmojiKey = "ASCENDING"
	EmojiDescending       EmojiKey = "DESCENDING"
	EmojiTimeframe        EmojiKey = "TIMEFRAME"
	EmojiMarketAnalysis   EmojiKey = "MARKET_ANALYSIS"
	EmojiLastClosedCandle EmojiKey = "LAST_CLOSED_CANDLE"
	EmojiFinalDecision    EmojiKey = "FINAL_DECISION"
	EmojiWait             EmojiKey = "WAIT"
)

type emojiDefinition struct {
	ID       string
	Fallback string
}

var emojiRegistry = map[EmojiKey]emojiDefinition{
	EmojiBNB: {"4956574641075258382", "🟡"}, EmojiBuy: {"5839260144902344766", "🟢"},
	EmojiSell: {"5841647064387162728", "🔴"}, EmojiInvoice: {"5444856076954520455", "🧾"},
	EmojiSuccess: {"5278482869407198831", "✅"}, EmojiFailure: {"5276429278039258798", "❌"},
	EmojiAttention: {"5949361713139683377", "⚠️"}, EmojiRisk: {"5330194932781050507", "🛡️"},
	EmojiEntryPoint: {"5370726448959085259", "📍"}, EmojiRiskReward: {"5422394304234536556", "⚖️"},
	EmojiTakeProfit: {"5386688235353166072", "🎯"}, EmojiStopLoss: {"5244795211816650110", "🛑"},
	EmojiCancellation: {"5161208387957950108", "🚫"}, EmojiExpiration: {"5298961058726175115", "⏳"},
	EmojiIfCondition: {"4927486932113425461", "🔘"}, EmojiDigit1: {"5393089050884202480", "1️⃣"},
	EmojiDigit2: {"5393392851100919543", "2️⃣"}, EmojiDigit3: {"5393210254861294826", "3️⃣"},
	EmojiDigit4: {"5393501067096908920", "4️⃣"}, EmojiDigit5: {"5393420098373443978", "5️⃣"},
	EmojiDigit6: {"5393295054695588974", "6️⃣"}, EmojiDigit7: {"5393480575807939250", "7️⃣"},
	EmojiDigit8: {"5393131064254293974", "8️⃣"}, EmojiDigit9: {"5393216847636102886", "9️⃣"},
	EmojiDigit0: {"5393565105059291505", "0️⃣"}, EmojiMarketStructure: {"5307726889408809792", "🏗️"},
	EmojiAscending: {"5429463966432643712", "📈"}, EmojiDescending: {"5429165728198576891", "📉"},
	EmojiTimeframe: {"5348236797606379943", "⏱️"}, EmojiMarketAnalysis: {"5190924609548999975", "📊"},
	EmojiLastClosedCandle: {"5003748650594075621", "🕯️"}, EmojiFinalDecision: {"5215313353706057331", "🎯"},
	EmojiWait: {"5255765065096774716", "⏸️"},
}

// customEmoji returns trusted Telegram HTML. Dynamic content must be escaped
// separately before being concatenated with this markup.
func customEmoji(key EmojiKey) string {
	d, ok := emojiRegistry[key]
	if !ok {
		return ""
	}
	return `<tg-emoji emoji-id="` + html.EscapeString(d.ID) + `">` + html.EscapeString(d.Fallback) + `</tg-emoji>`
}

func digitEmoji(n int) string {
	keys := []EmojiKey{EmojiDigit0, EmojiDigit1, EmojiDigit2, EmojiDigit3, EmojiDigit4, EmojiDigit5, EmojiDigit6, EmojiDigit7, EmojiDigit8, EmojiDigit9}
	if n < 0 || n > 9 {
		return ""
	}
	return customEmoji(keys[n])
}
