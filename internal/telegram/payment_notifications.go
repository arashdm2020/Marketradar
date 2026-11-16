package telegram

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"time"
)

func (b Bot) runPaymentNotifications(ctx context.Context) {
	if b.Store == nil {
		return
	}
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		b.processPaymentNotifications(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (b Bot) processPaymentNotifications(ctx context.Context) {
	items, err := b.Store.ClaimPaymentNotifications(time.Now(), 20)
	if err != nil {
		slog.Warn("payment_notification_claim_failed", "error", err.Error())
		return
	}
	for _, item := range items {
		text := "<b>✅ پرداخت تأیید شد</b>\n\n" +
			"شماره فاکتور: <code>" + html.EscapeString(item.InvoiceID) + "</code>\n" +
			"مبلغ دقیق (Wei): <code>" + html.EscapeString(item.AmountWei) + "</code>\n" +
			"تراکنش: <code>" + html.EscapeString(item.TxHash) + "</code>\n\n" +
			"دریافت پرداخت تأیید شد؛ وضعیت فعال‌سازی اشتراک جداگانه ثبت می‌شود."
		if item.Kind == "subscription_activated" {
			text = "<b>💎 اشتراک فعال شد</b>\n\nپلن: <b>Standard</b>\nاعتبار تا: <code>" + html.EscapeString(item.SubscriptionUntil) + "</code>\nتراکنش: <code>" + html.EscapeString(item.TxHash) + "</code>"
		}
		sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err = b.send(sendCtx, item.TelegramID, text, nil)
		cancel()
		if completeErr := b.Store.CompletePaymentNotification(item.ID, err == nil, fmt.Sprint(err), time.Now()); completeErr != nil {
			slog.Warn("payment_notification_update_failed", "id", item.ID, "error", completeErr.Error())
		}
	}
}
