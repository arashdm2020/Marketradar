package announcement

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"tradebot/internal/profile"
)

type Store struct{ DB *sql.DB }

const retryTimeFormat = "2006-01-02T15:04:05.000000000Z"

type Summary struct {
	CampaignID string `json:"campaign_id"`
	Recipients int    `json:"recipients"`
	Accepted   int    `json:"accepted_messages"`
	Failed     int    `json:"failed_messages"`
	Unknown    int    `json:"unknown_messages"`
	Pending    int    `json:"pending_messages"`
}

func (s Store) Migrate() error {
	_, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS announcement_campaigns(
		id TEXT PRIMARY KEY, content_hash TEXT NOT NULL, created_at TEXT NOT NULL);
		CREATE TABLE IF NOT EXISTS announcement_deliveries(
		campaign_id TEXT NOT NULL REFERENCES announcement_campaigns(id),
		telegram_id INTEGER NOT NULL REFERENCES users(id), sequence INTEGER NOT NULL CHECK(sequence BETWEEN 1 AND 3),
		status TEXT NOT NULL DEFAULT 'pending', message_id INTEGER NOT NULL DEFAULT 0,
		emoji_verified INTEGER NOT NULL DEFAULT 0, attempts INTEGER NOT NULL DEFAULT 0,
		retry_at TEXT NOT NULL DEFAULT '', failure_class TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL,
		PRIMARY KEY(campaign_id,telegram_id,sequence));
		CREATE INDEX IF NOT EXISTS announcement_delivery_status ON announcement_deliveries(campaign_id,status,retry_at);`)
	return err
}

// Prepare freezes the recipient list at first creation. Users registered later
// are not added by repeated dry runs or a later broadcast invocation.
func (s Store) Prepare(ctx context.Context) (Summary, error) {
	if err := s.Migrate(); err != nil {
		return Summary{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Summary{}, err
	}
	defer tx.Rollback()
	var prior string
	err = tx.QueryRowContext(ctx, `SELECT content_hash FROM announcement_campaigns WHERE id=?`, CampaignID).Scan(&prior)
	if err != nil && err != sql.ErrNoRows {
		return Summary{}, err
	}
	if err == nil && prior != MessageHash() {
		return Summary{}, errors.New("campaign content differs from frozen version")
	}
	if err == sql.ErrNoRows {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err = tx.ExecContext(ctx, `INSERT INTO announcement_campaigns(id,content_hash,created_at) VALUES(?,?,?)`, CampaignID, MessageHash(), now); err != nil {
			return Summary{}, err
		}
		// The users table is Telegram-only. Require recorded bot activity to
		// exclude fixture/test identities and never select web_users.
		rows, queryErr := tx.QueryContext(ctx, `SELECT u.id FROM users u WHERE u.id>0 AND
			(EXISTS(SELECT 1 FROM telegram_ui_state x WHERE x.telegram_id=u.id) OR
			 EXISTS(SELECT 1 FROM request_states x WHERE x.user_id=u.id) OR
			 EXISTS(SELECT 1 FROM analyses x WHERE x.user_id=u.id)) ORDER BY u.id`)
		if queryErr != nil {
			return Summary{}, queryErr
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if queryErr = rows.Scan(&id); queryErr != nil {
				rows.Close()
				return Summary{}, queryErr
			}
			ids = append(ids, id)
		}
		queryErr = rows.Err()
		rows.Close()
		if queryErr != nil {
			return Summary{}, queryErr
		}
		if len(ids) == 0 {
			return Summary{}, errors.New("no verified Telegram recipients")
		}
		adminFound := false
		for _, id := range ids {
			if id == profile.AdminID {
				adminFound = true
			}
			for sequence := 1; sequence <= 3; sequence++ {
				if _, err = tx.ExecContext(ctx, `INSERT INTO announcement_deliveries(campaign_id,telegram_id,sequence,updated_at) VALUES(?,?,?,?)`, CampaignID, id, sequence, now); err != nil {
					return Summary{}, err
				}
			}
		}
		if !adminFound {
			return Summary{}, errors.New("administrator is not an eligible Telegram recipient")
		}
	}
	if err = tx.Commit(); err != nil {
		return Summary{}, err
	}
	return s.Summary(ctx)
}

func (s Store) Summary(ctx context.Context) (Summary, error) {
	out := Summary{CampaignID: CampaignID}
	err := s.DB.QueryRowContext(ctx, `SELECT count(DISTINCT telegram_id),
		coalesce(sum(status='accepted'),0),coalesce(sum(status='failed'),0),
		coalesce(sum(status IN ('unknown','sending')),0),coalesce(sum(status='pending'),0)
		FROM announcement_deliveries WHERE campaign_id=?`, CampaignID).Scan(
		&out.Recipients, &out.Accepted, &out.Failed, &out.Unknown, &out.Pending)
	return out, err
}

func (s Store) AdminVerified(ctx context.Context) (bool, error) {
	var count int
	err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM announcement_deliveries WHERE campaign_id=? AND telegram_id=? AND status='accepted' AND emoji_verified=1`, CampaignID, profile.AdminID).Scan(&count)
	return count == 3, err
}

func (s Store) RecipientIDs(ctx context.Context, adminOnly bool) ([]int64, error) {
	query := `SELECT DISTINCT telegram_id FROM announcement_deliveries WHERE campaign_id=?`
	args := []any{CampaignID}
	if adminOnly {
		query += ` AND telegram_id=?`
		args = append(args, profile.AdminID)
	}
	query += ` ORDER BY telegram_id`
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

type deliveryState struct {
	Status        string
	EmojiVerified bool
	RetryAt       string
}

func (s Store) state(ctx context.Context, id int64, sequence int) (deliveryState, error) {
	var out deliveryState
	var verified int
	err := s.DB.QueryRowContext(ctx, `SELECT status,emoji_verified,retry_at FROM announcement_deliveries WHERE campaign_id=? AND telegram_id=? AND sequence=?`, CampaignID, id, sequence).Scan(&out.Status, &verified, &out.RetryAt)
	out.EmojiVerified = verified == 1
	return out, err
}

func (s Store) claim(ctx context.Context, id int64, sequence int) (bool, error) {
	result, err := s.DB.ExecContext(ctx, `UPDATE announcement_deliveries SET status='sending',attempts=attempts+1,updated_at=? WHERE campaign_id=? AND telegram_id=? AND sequence=? AND status='pending' AND (retry_at='' OR retry_at<=?)`, time.Now().UTC().Format(time.RFC3339Nano), CampaignID, id, sequence, time.Now().UTC().Format(retryTimeFormat))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (s Store) mark(ctx context.Context, id int64, sequence int, status, class string, messageID int64, emojiOK bool, retryAt time.Time) error {
	verified := 0
	if emojiOK {
		verified = 1
	}
	retry := ""
	if !retryAt.IsZero() {
		retry = retryAt.UTC().Format(retryTimeFormat)
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE announcement_deliveries SET status=?,failure_class=?,message_id=?,emoji_verified=?,retry_at=?,updated_at=? WHERE campaign_id=? AND telegram_id=? AND sequence=? AND status='sending'`, status, class, messageID, verified, retry, time.Now().UTC().Format(time.RFC3339Nano), CampaignID, id, sequence)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("announcement delivery state changed")
	}
	return nil
}
