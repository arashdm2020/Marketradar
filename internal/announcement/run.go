package announcement

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type SendFunc func(context.Context, int64, int) (Delivery, error)

type RunResult struct {
	CampaignID   string  `json:"campaign_id"`
	Mode         string  `json:"mode"`
	Attempted    int     `json:"attempted_messages"`
	Accepted     int     `json:"newly_accepted_messages"`
	Rejected     int     `json:"rejected_messages"`
	Unknown      int     `json:"unknown_messages"`
	AdminEmojiOK bool    `json:"admin_emoji_verified"`
	Summary      Summary `json:"summary"`
}

// Run delivers one recipient at a time, in sequence. Only explicit Telegram
// rejection may be retried; ambiguous transport outcomes are never resent.
func (s Store) Run(ctx context.Context, adminOnly bool, send SendFunc) (RunResult, error) {
	result := RunResult{CampaignID: CampaignID, Mode: "broadcast"}
	if adminOnly {
		result.Mode = "admin_test"
	}
	if send == nil {
		return result, errors.New("announcement sender missing")
	}
	if !adminOnly {
		verified, err := s.AdminVerified(ctx)
		if err != nil || !verified {
			return result, errors.New("administrator test is not fully verified")
		}
	}
	ids, err := s.RecipientIDs(ctx, adminOnly)
	if err != nil || len(ids) == 0 {
		return result, errors.New("campaign recipients unavailable")
	}
	var lastCall time.Time
	for _, id := range ids {
		for sequence := 1; sequence <= 3; sequence++ {
			state, err := s.state(ctx, id, sequence)
			if err != nil {
				return result, err
			}
			if state.Status == "accepted" {
				if !state.EmojiVerified {
					return result, errors.New("Telegram accepted a message without verified custom emoji entities")
				}
				continue
			}
			if state.Status != "pending" {
				if adminOnly {
					return result, fmt.Errorf("administrator delivery requires review: %s", state.Status)
				}
				break
			}
			if state.RetryAt != "" && state.RetryAt > time.Now().UTC().Format(retryTimeFormat) {
				return result, errors.New("Telegram rate-limit window has not elapsed")
			}
			claimed, err := s.claim(ctx, id, sequence)
			if err != nil {
				return result, err
			}
			if !claimed {
				return result, errors.New("announcement delivery claimed by another process")
			}
			if !lastCall.IsZero() {
				wait := time.Until(lastCall.Add(time.Second))
				if wait > 0 {
					timer := time.NewTimer(wait)
					select {
					case <-ctx.Done():
						timer.Stop()
						return result, ctx.Err()
					case <-timer.C:
					}
				}
			}
			result.Attempted++
			lastCall = time.Now()
			delivery, sendErr := send(ctx, id, sequence)
			if sendErr == nil {
				if err := s.mark(ctx, id, sequence, "accepted", "", delivery.MessageID, delivery.EmojiOK, time.Time{}); err != nil {
					return result, err
				}
				result.Accepted++
				if !delivery.EmojiOK {
					return result, errors.New("Telegram accepted a message without verified custom emoji entities")
				}
				continue
			}
			var apiErr *SendError
			if !errors.As(sendErr, &apiErr) {
				apiErr = &SendError{Class: "delivery_unknown"}
			}
			switch apiErr.Class {
			case "rate_limited":
				if err := s.mark(ctx, id, sequence, "pending", apiErr.Class, 0, false, time.Now().Add(apiErr.RetryAfter)); err != nil {
					return result, err
				}
				return result, errors.New("Telegram rate limit; retry after recorded delay")
			case "recipient_rejected":
				if err := s.mark(ctx, id, sequence, "failed", apiErr.Class, 0, false, time.Time{}); err != nil {
					return result, err
				}
				result.Rejected++
				if adminOnly {
					return result, errors.New("administrator test rejected by Telegram")
				}
			default:
				if err := s.mark(ctx, id, sequence, "unknown", "delivery_unknown", 0, false, time.Time{}); err != nil {
					return result, err
				}
				result.Unknown++
				return result, errors.New("Telegram delivery uncertain; manual review required")
			}
			break
		}
	}
	result.AdminEmojiOK, err = s.AdminVerified(ctx)
	if err != nil {
		return result, err
	}
	result.Summary, err = s.Summary(ctx)
	return result, err
}
