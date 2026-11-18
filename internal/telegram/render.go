package telegram

import (
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"
	"tradebot/internal/catalog"
	"tradebot/internal/domain"
	"tradebot/internal/profile"
)

// maxMessageRunes is the maximum number of runes per Telegram message.
// Telegram's limit is ~4096 characters; we stay well under to allow for HTML entity expansion.
const maxMessageRunes = 3800

var telegramHTMLTag = regexp.MustCompile(`<[^>]+>`)

func visibleRunes(msg string) int {
	plain := telegramHTMLTag.ReplaceAllString(msg, "")
	return len([]rune(html.UnescapeString(plain)))
}

// splitMessage splits a message into UTF-8-safe, HTML-aware chunks.
// It never splits in the middle of a rune or an HTML open tag.
// Returns the original string if it fits within maxMessageRunes.
func splitMessage(msg string) string {
	if len(msg) == 0 {
		return msg
	}
	runes := []rune(msg)
	if visibleRunes(msg) <= maxMessageRunes {
		return msg
	}

	var b strings.Builder
	count := 0
	inTag := false
	var tagRunes []rune
	var openTags []string // tag names that are open and need closing on truncation

	for _, r := range runes {
		if r == '<' {
			inTag = true
			tagRunes = tagRunes[:0]
			tagRunes = append(tagRunes, r)
			continue
		}
		if inTag {
			tagRunes = append(tagRunes, r)
			if r == '>' {
				inTag = false
				tagStr := string(tagRunes)
				if count+len(tagRunes) > maxMessageRunes {
					break
				}
				for _, tr := range tagRunes {
					b.WriteRune(tr)
				}
				count += len(tagRunes)
				// Track opening tags for potential close-on-truncate
				if strings.HasPrefix(tagStr, "</") {
					// Closing tag - remove from openTags
					name := strings.TrimSuffix(strings.TrimPrefix(tagStr, "</"), ">")
					name = strings.Split(name, " ")[0]
					for i := len(openTags) - 1; i >= 0; i-- {
						if openTags[i] == name {
							openTags = append(openTags[:i], openTags[i+1:]...)
							break
						}
					}
				} else if !strings.HasSuffix(tagStr, "/>") {
					// Opening tag - add to openTags
					name := strings.TrimSuffix(strings.TrimPrefix(tagStr, "<"), ">")
					name = strings.Split(name, " ")[0]
					openTags = append(openTags, name)
				}
			}
			continue
		}
		if count >= maxMessageRunes {
			break
		}
		b.WriteRune(r)
		count++
	}

	// Close any unclosed tags on truncation
	for i := len(openTags) - 1; i >= 0; i-- {
		b.WriteString("</" + openTags[i] + ">")
	}

	return b.String()
}

func styleLabel(style string) string {
	switch strings.ToLower(style) {
	case "scalp":
		return "اسکالپ"
	case "day":
		return "روزانه"
	case "swing":
		return "میان‌مدت"
	case "position":
		return "بلندمدت"
	default:
		return html.EscapeString(style)
	}
}

func dirFa(sig string) string {
	switch sig {
	case "BUY":
		return customEmoji(EmojiBuy) + " خرید"
	case "SELL":
		return customEmoji(EmojiSell) + " فروش"
	case "CONDITIONAL":
		return customEmoji(EmojiIfCondition) + " ورود مشروط"
	default:
		return customEmoji(EmojiWait) + " صبر"
	}
}

func statusFa(final string) string {
	switch final {
	case "BUY":
		return "تأیید شد — معامله خرید مشروط"
	case "SELL":
		return "تأیید شد — معامله فروش مشروط"
	case "WAIT":
		return "صبر — شرایط معامله برقرار نشده است"
	case "CONDITIONAL":
		return "سناریوی معتبر؛ ورود پس از تحقق شرط مشخص‌شده"
	case "ANALYSIS_UNAVAILABLE":
		return "تحلیل در دسترس نیست"
	default:
		return final
	}
}
func triggerText(s domain.ConditionalSetup, precision int) string {
	level := fmt.Sprintf("%.*f", precision, s.TriggerPrice)
	entry := fmt.Sprintf("%.*f", precision, s.Entry)
	switch s.TriggerKind {
	case "BREAK_RETEST_BUY":
		return "بسته‌شدن کندل بالای " + level + "، سپس بازگشت به " + entry + " و بسته‌شدن دوباره بالای سطح"
	case "BREAK_RETEST_SELL":
		return "بسته‌شدن کندل زیر " + level + "، سپس بازگشت به " + entry + " و بسته‌شدن دوباره زیر سطح"
	case "PULLBACK_BUY":
		return "لمس " + entry + " و بسته‌شدن کندل بالای آن"
	case "PULLBACK_SELL":
		return "لمس " + entry + " و بسته‌شدن کندل زیر آن"
	case "CLOSE_ABOVE":
		return "بسته‌شدن کندل بالای " + level
	case "CLOSE_BELOW":
		return "بسته‌شدن کندل زیر " + level
	case "TOUCH":
		return "لمس " + level
	}
	return s.Trigger
}

func Render(d domain.Decision, symbol, frame, style string, u profile.User) string {
	inst, _ := catalog.Find(symbol)
	loc, e := time.LoadLocation(u.Timezone)
	if e != nil {
		loc = time.UTC
	}
	at := d.Snapshot.AnalysisTimeUTC.In(loc).Format("2006-01-02 15:04")
	last := d.Snapshot.LastCandleTimeUTC.In(loc).Format("2006-01-02 15:04")

	var b strings.Builder

	// Header
	fmt.Fprintf(&b, "<b>%s تحلیل بازار — %s | %s</b>\n", customEmoji(EmojiMarketAnalysis), html.EscapeString(symbol), html.EscapeString(catalog.Timeframe(frame).Label()))
	fmt.Fprintf(&b, "%s <b>تایم‌فریم:</b> %s  |  <b>سبک:</b> %s  |  %s\n", customEmoji(EmojiTimeframe), html.EscapeString(catalog.Timeframe(frame).Label()), styleLabel(style), at)
	fmt.Fprintf(&b, "%s <b>شروع آخرین کندل بسته‌شده:</b> %s\n\n", customEmoji(EmojiLastClosedCandle), last)

	if d.Final == "ANALYSIS_UNAVAILABLE" {
		b.WriteString("<b>" + customEmoji(EmojiFailure) + " تحلیل در دسترس نیست</b>\n")
		b.WriteString("تحلیل بازار تکمیل نشد؛ این پیام توصیه به صبر یا معامله نیست. لطفاً بعداً دوباره تلاش کنید.\n\n")
		if len(d.Reasons) > 0 {
			b.WriteString("دلیل: " + html.EscapeString(strings.Join(d.Reasons, "؛ ")) + "\n\n")
		}
		b.WriteString(reportDisclaimer)
		return b.String()
	}

	if d.Final == "CONDITIONAL" && len(d.ValidatedConditional) > 0 {
		b.WriteString("<b>" + customEmoji(EmojiWait) + " ورود هنوز تأیید نشده است.</b>\n" + customEmoji(EmojiIfCondition) + " <b>شرط نخست:</b> " + html.EscapeString(triggerText(d.ValidatedConditional[0], inst.Precision)) + "\n\n")
	}
	b.WriteString("<b>" + customEmoji(EmojiFinalDecision) + " تصمیم نهایی: </b>" + dirFa(d.Final) + "\n")
	b.WriteString("📋 وضعیت: " + statusFa(d.Final) + "\n\n")
	if (d.Final == "WAIT" || d.Final == "CONDITIONAL") && len(d.Proposed.Reasoning) > 0 {
		label := "دلیل صبر"
		if d.Final == "CONDITIONAL" {
			label = "دلیل ورود مشروط"
		}
		b.WriteString("<b>" + label + ":</b> " + html.EscapeString(strings.Join(d.Proposed.Reasoning, "؛ ")) + "\n\n")
	}

	// AI proposed immediate trade
	if d.Final == "BUY" || d.Final == "SELL" {
		p := d.Proposed
		if p.Entry != nil && p.StopLoss != nil && p.TP1 != nil {
			f := func(v float64) string { return fmt.Sprintf("%.*f", inst.Precision, v) }
			b.WriteString("<b>" + customEmoji(EmojiEntryPoint) + " معامله فوری:</b>\n")
			if p.EntryTrigger != "" {
				b.WriteString("   " + customEmoji(EmojiIfCondition) + " <b>شرط ورود:</b> " + html.EscapeString(p.EntryTrigger) + "\n")
			}
			fmt.Fprintf(&b, "   %s <b>ورود:</b> %s\n", customEmoji(EmojiEntryPoint), f(*p.Entry))
			fmt.Fprintf(&b, "   %s <b>حد ضرر:</b> %s\n", customEmoji(EmojiStopLoss), f(*p.StopLoss))
			fmt.Fprintf(&b, "   %s <b>حد سود ۱:</b> %s\n", customEmoji(EmojiTakeProfit), f(*p.TP1))
			if p.TP2 != nil {
				fmt.Fprintf(&b, "   %s <b>حد سود ۲:</b> %s\n", customEmoji(EmojiTakeProfit), f(*p.TP2))
			}
			fmt.Fprintf(&b, "   %s <b>نسبت سود به ضرر:</b> %.2f\n", customEmoji(EmojiRiskReward), d.RiskReward)
			if p.SetupQuality != "" {
				fmt.Fprintf(&b, "   کیفیت استراتژی: %s\n", html.EscapeString(p.SetupQuality))
			}
			b.WriteString("   " + customEmoji(EmojiCancellation) + " <b>ابطال:</b> " + html.EscapeString(p.Invalidation) + "\n\n")
		}
		if len(p.Reasoning) > 0 {
			b.WriteString("<b>💬 استدلال:</b>\n")
			for _, r := range p.Reasoning {
				fmt.Fprintf(&b, "   • %s\n", html.EscapeString(r))
			}
			b.WriteString("\n")
		}
	}

	// Market structure context
	q := d.Snapshot.Quant
	if q.Alignment != "" {
		alignmentFa := "خاموش"
		switch q.Alignment {
		case "BULLISH":
			alignmentFa = "صعودی"
		case "BEARISH":
			alignmentFa = "نزولی"
		}
		momentum := "خنثی"
		if q.Momentum == "BEARISH" {
			momentum = "نزولی"
		}
		if q.Momentum == "BULLISH" {
			momentum = "صعودی"
		}
		trendEmoji := customEmoji(EmojiWait)
		if q.Alignment == "BULLISH" {
			trendEmoji = customEmoji(EmojiAscending)
		}
		if q.Alignment == "BEARISH" {
			trendEmoji = customEmoji(EmojiDescending)
		}
		fmt.Fprintf(&b, "<b>%s ساختار بازار:</b> %s %s | <b>شتاب:</b> %s\n", customEmoji(EmojiMarketStructure), trendEmoji, html.EscapeString(alignmentFa), momentum)
		if q.ATR14 > 0 {
			fmt.Fprintf(&b, "   ATR14: %.4f (%.2f%%)\n", q.ATR14, q.ATRPercent)
		}
		b.WriteString("\n")
	}

	// Validated conditional scenarios (PENDING)
	if len(d.ValidatedConditional) > 0 {
		b.WriteString("<b>" + customEmoji(EmojiExpiration) + " سناریوهای معامله‌ای معتبر (در انتظار فعال شدن):</b>\n")
		for i, s := range d.ValidatedConditional {
			if i >= 3 {
				break
			}
			dir := dirFa(s.Direction)
			f := func(v float64) string { return fmt.Sprintf("%.*f", inst.Precision, v) }
			fmt.Fprintf(&b, "\n<b>%s %s</b>\n", digitEmoji(i+1), dir)
			fmt.Fprintf(&b, "   %s <b>شرط:</b> %s\n", customEmoji(EmojiIfCondition), html.EscapeString(triggerText(s, inst.Precision)))
			fmt.Fprintf(&b, "   %s <b>ورود:</b> %s\n", customEmoji(EmojiEntryPoint), f(s.Entry))
			fmt.Fprintf(&b, "   %s <b>حد ضرر:</b> %s\n", customEmoji(EmojiStopLoss), f(s.StopLoss))
			fmt.Fprintf(&b, "   %s <b>حد سود ۱:</b> %s\n", customEmoji(EmojiTakeProfit), f(s.TP1))
			if s.TP2 != nil {
				fmt.Fprintf(&b, "   %s <b>حد سود ۲:</b> %s\n", customEmoji(EmojiTakeProfit), f(*s.TP2))
			}
			rr := 0.0
			if s.Direction == "BUY" && s.StopLoss < s.Entry {
				rr = (s.TP1 - s.Entry) / (s.Entry - s.StopLoss)
			} else if s.Direction == "SELL" && s.StopLoss > s.Entry {
				rr = (s.Entry - s.TP1) / (s.StopLoss - s.Entry)
			}
			if rr > 0 {
				fmt.Fprintf(&b, "   %s <b>نسبت سود به ضرر:</b> %.2f\n", customEmoji(EmojiRiskReward), rr)
			}
			fmt.Fprintf(&b, "   %s <b>ابطال:</b> بسته‌شدن کندل آن‌سوی حد ضرر %s\n", customEmoji(EmojiCancellation), f(s.StopLoss))
			exp, err := time.Parse(time.RFC3339, s.Expiration)
			if err == nil {
				fmt.Fprintf(&b, "   %s <b>انقضا:</b> %s\n", customEmoji(EmojiExpiration), exp.In(loc).Format("2006-01-02 15:04"))
			}
		}
		b.WriteString("\n")
	}

	if len(d.Reasons) > 0 && d.Final != "WAIT" {
		b.WriteString("<b>🔎 توضیحات:</b>\n")
		for _, r := range d.Reasons {
			if r == "AI chose WAIT" {
				continue
			}
			fmt.Fprintf(&b, "   • %s\n", html.EscapeString(r))
		}
		b.WriteString("\n")
	}

	if u.MaxRisk > 0 {
		b.WriteString(customEmoji(EmojiRisk) + " <b>سقف ریسک انتخابی:</b> " + fmt.Sprintf("%.2f%%", u.MaxRisk) + " سرمایه\n")
	} else {
		b.WriteString(customEmoji(EmojiRisk) + " سقف ریسک هنوز تنظیم نشده است.\n")
	}
	b.WriteString(reportDisclaimer)
	return b.String()
}

const reportDisclaimer = "تحلیل‌های مارکت‌رادار صرفاً برای اطلاع‌رسانی ارائه می‌شوند. تصمیم و اجرای معامله بر عهدهٔ کاربر است.\nمارکت‌رادار مسئولیتی در قبال زیان ناشی از تصمیم‌های معاملاتی کاربر نمی‌پذیرد."
