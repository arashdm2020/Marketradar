package announcement

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"
)

type Client struct {
	Token   string
	HTTP    *http.Client
	BaseURL string // tests only; production uses the Telegram Bot API
}

type SendError struct {
	Class      string
	RetryAfter time.Duration
}

func (e *SendError) Error() string { return e.Class }

type Delivery struct {
	MessageID int64
	EmojiOK   bool
}

type entity struct {
	Type          string `json:"type"`
	CustomEmojiID string `json:"custom_emoji_id"`
}

func (c Client) Send(ctx context.Context, chatID int64, sequence int) (Delivery, error) {
	if c.Token == "" || chatID <= 0 || sequence < 1 || sequence > 3 {
		return Delivery{}, errors.New("invalid announcement request")
	}
	base := c.BaseURL
	if base == "" {
		base = "https://api.telegram.org"
	}
	msg := Messages()[sequence-1]
	body, _ := json.Marshal(map[string]any{
		"chat_id": chatID, "text": msg, "parse_mode": "HTML",
		"disable_web_page_preview": true,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/bot"+c.Token+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return Delivery{}, &SendError{Class: "request_setup"}
	}
	req.Header.Set("Content-Type", "application/json")
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 12 * time.Second}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		// The request may have reached Telegram. Never auto-retry this state.
		return Delivery{}, &SendError{Class: "delivery_unknown"}
	}
	defer resp.Body.Close()
	var wire struct {
		OK         bool `json:"ok"`
		ErrorCode  int  `json:"error_code"`
		Parameters struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
		Result struct {
			MessageID int64    `json:"message_id"`
			Entities  []entity `json:"entities"`
		} `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&wire); err != nil {
		return Delivery{}, &SendError{Class: "delivery_unknown"}
	}
	if !wire.OK || resp.StatusCode >= 400 {
		code := wire.ErrorCode
		if code == 0 {
			code = resp.StatusCode
		}
		switch code {
		case 429:
			seconds := wire.Parameters.RetryAfter
			if seconds <= 0 {
				seconds, _ = strconv.Atoi(resp.Header.Get("Retry-After"))
			}
			if seconds < 1 {
				seconds = 5
			}
			if seconds > 3600 {
				seconds = 3600
			}
			return Delivery{}, &SendError{Class: "rate_limited", RetryAfter: time.Duration(seconds) * time.Second}
		case 400, 403:
			return Delivery{}, &SendError{Class: "recipient_rejected"}
		default:
			return Delivery{}, &SendError{Class: "delivery_unknown"}
		}
	}
	if wire.Result.MessageID <= 0 {
		return Delivery{}, &SendError{Class: "delivery_unknown"}
	}
	want := ExpectedEmojiIDs(sequence)
	var got []string
	for _, e := range wire.Result.Entities {
		if e.Type == "custom_emoji" {
			got = append(got, e.CustomEmojiID)
		}
	}
	match := len(want) == len(got)
	if match {
		for i := range want {
			if want[i] != got[i] {
				match = false
				break
			}
		}
	}
	return Delivery{MessageID: wire.Result.MessageID, EmojiOK: match}, nil
}
