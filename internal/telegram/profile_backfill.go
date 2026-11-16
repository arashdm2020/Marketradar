package telegram

import (
	"context"
	"errors"
	"time"

	"tradebot/internal/profile"
)

type ChatProfile struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

type ProfileBackfillResult struct {
	Inspected   int `json:"inspected"`
	Recovered   int `json:"recovered"`
	Unavailable int `json:"unavailable"`
	Failed      int `json:"failed"`
}

// BackfillProfiles checks at most limit existing Telegram identities. It does
// not create users or wallets, and dry runs never write profile data.
func BackfillProfiles(ctx context.Context, store *profile.Store, limit int, apply bool, lookup func(context.Context, int64) (ChatProfile, error)) (ProfileBackfillResult, error) {
	var result ProfileBackfillResult
	if store == nil || lookup == nil {
		return result, errors.New("profile backfill dependencies missing")
	}
	ids, err := store.MissingTelegramProfiles(limit)
	if err != nil {
		return result, err
	}
	for i, id := range ids {
		if i > 0 {
			timer := time.NewTimer(250 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return result, ctx.Err()
			case <-timer.C:
			}
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		result.Inspected++
		chat, lookupErr := lookup(ctx, id)
		if lookupErr != nil || chat.ID != id || chat.Type != "private" {
			result.Failed++
			continue
		}
		if chat.FirstName != "" || chat.LastName != "" || chat.Username != "" {
			result.Recovered++
		} else {
			result.Unavailable++
		}
		if apply {
			if err := store.BackfillTelegramProfile(id, chat.FirstName, chat.LastName, chat.Username); err != nil {
				return result, err
			}
		}
	}
	return result, nil
}

func (b Bot) LookupChatProfile(ctx context.Context, id int64) (ChatProfile, error) {
	var chat ChatProfile
	err := b.call(ctx, "getChat", map[string]any{"chat_id": id}, &chat)
	return chat, err
}
