package webapp

import (
	"errors"
	"time"
)

// AllowUserRequest applies a durable per-user, per-operation fixed-window limit.
// The row is updated atomically so concurrent requests cannot race the counter.
func (s *Store) AllowUserRequest(userID int64, operation string, limit int, window time.Duration, now time.Time) (bool, error) {
	if userID <= 0 || operation == "" || limit < 1 || window < time.Second {
		return false, errors.New("invalid rate limit")
	}
	seconds := int64(window / time.Second)
	start := now.UTC().Unix() / seconds * seconds
	var count int
	err := s.DB.QueryRow(`INSERT INTO web_user_rate_limits(user_id,operation,window_start,request_count) VALUES(?,?,?,1)
		ON CONFLICT(user_id,operation) DO UPDATE SET
		request_count=CASE WHEN web_user_rate_limits.window_start=excluded.window_start THEN web_user_rate_limits.request_count+1 ELSE 1 END,
		window_start=excluded.window_start RETURNING request_count`, userID, operation, start).Scan(&count)
	if err != nil {
		return false, err
	}
	return count <= limit, nil
}
