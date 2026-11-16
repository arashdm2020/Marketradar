package profile

import (
	"database/sql"
	"errors"
	"net/url"
	"strings"
	"time"
)

type TelegramProfile struct {
	TelegramID    int64  `json:"telegram_id"`
	FirstName     string `json:"first_name"`
	LastName      string `json:"last_name"`
	Username      string `json:"username"`
	PhoneNumber   string `json:"phone_number"`
	PhoneVerified bool   `json:"phone_verified"`
	PhotoURL      string `json:"photo_url"`
	UpdatedAt     string `json:"updated_at"`
	ProvidedName  string `json:"provided_display_name"`
}

func (p TelegramProfile) DisplayName() string {
	name := strings.TrimSpace(strings.TrimSpace(p.FirstName) + " " + strings.TrimSpace(p.LastName))
	if name != "" {
		return name
	}
	return p.ProvidedName
}

func boundedProfileField(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) > max {
		return ""
	}
	return value
}

func safeTelegramPhotoURL(raw string) string {
	raw = boundedProfileField(raw, 2048)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return ""
	}
	return raw
}

func (s *Store) telegramProfileTx(tx *sql.Tx, telegramID int64, firstName, lastName, username, photoURL string, now time.Time) error {
	firstName = boundedProfileField(firstName, 256)
	lastName = boundedProfileField(lastName, 256)
	username = strings.TrimPrefix(boundedProfileField(username, 64), "@")
	photoURL = safeTelegramPhotoURL(photoURL)
	_, err := tx.Exec(`UPDATE users SET
		telegram_first_name=CASE WHEN ?<>'' THEN ? ELSE telegram_first_name END,
		telegram_last_name=CASE WHEN ?<>'' THEN ? ELSE telegram_last_name END,
		telegram_username=CASE WHEN ?<>'' THEN ? ELSE telegram_username END,
		telegram_photo_url=CASE WHEN ?<>'' THEN ? ELSE telegram_photo_url END,
		profile_updated_at=? WHERE id=?`,
		firstName, firstName, lastName, lastName, username, username, photoURL, photoURL,
		now.UTC().Format(time.RFC3339Nano), telegramID)
	return err
}

// UpdateTelegramSender records verified sender fields supplied by Telegram.
// Missing fields never erase a previously recorded value.
func (s *Store) UpdateTelegramSender(telegramID int64, firstName, lastName, username string) error {
	if telegramID <= 0 {
		return errors.New("invalid Telegram ID")
	}
	firstName = boundedProfileField(firstName, 256)
	lastName = boundedProfileField(lastName, 256)
	username = strings.TrimPrefix(boundedProfileField(username, 64), "@")
	result, err := s.DB.Exec(`UPDATE users SET
		telegram_first_name=CASE WHEN ?<>'' THEN ? ELSE telegram_first_name END,
		telegram_last_name=CASE WHEN ?<>'' THEN ? ELSE telegram_last_name END,
		telegram_username=CASE WHEN ?<>'' THEN ? ELSE telegram_username END,
		profile_updated_at=CASE WHEN ?<>'' OR ?<>'' OR ?<>'' THEN ? ELSE profile_updated_at END
		WHERE id=?`, firstName, firstName, lastName, lastName, username, username,
		firstName, lastName, username, time.Now().UTC().Format(time.RFC3339Nano), telegramID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errors.New("Telegram user not found")
	}
	return nil
}

// MissingTelegramProfiles returns existing users who have no recorded name or
// username. The limit keeps Bot API enrichment bounded and restart-safe.
func (s *Store) MissingTelegramProfiles(limit int) ([]int64, error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New("profile backfill limit must be 1..100")
	}
	rows, err := s.DB.Query(`SELECT id FROM users WHERE profile_backfill_checked_at='' AND
		(trim(telegram_first_name)='' OR trim(telegram_last_name)='' OR trim(telegram_username)='')
		ORDER BY id LIMIT ?`, limit)
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

// BackfillTelegramProfile fills only missing fields. A successful Bot API
// lookup is marked checked even when Telegram exposes no optional names.
func (s *Store) BackfillTelegramProfile(telegramID int64, firstName, lastName, username string) error {
	firstName = boundedProfileField(firstName, 256)
	lastName = boundedProfileField(lastName, 256)
	username = strings.TrimPrefix(boundedProfileField(username, 64), "@")
	result, err := s.DB.Exec(`UPDATE users SET
		telegram_first_name=CASE WHEN trim(telegram_first_name)='' THEN ? ELSE telegram_first_name END,
		telegram_last_name=CASE WHEN trim(telegram_last_name)='' THEN ? ELSE telegram_last_name END,
		telegram_username=CASE WHEN trim(telegram_username)='' THEN ? ELSE telegram_username END,
		profile_backfill_checked_at=? WHERE id=?`, firstName, lastName, username,
		time.Now().UTC().Format(time.RFC3339Nano), telegramID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errors.New("Telegram user not found")
	}
	return nil
}

func (s *Store) TelegramProfile(telegramID int64) (TelegramProfile, error) {
	var p TelegramProfile
	var verified int
	err := s.DB.QueryRow(`SELECT id,telegram_first_name,telegram_last_name,telegram_username,phone_number,phone_verified,telegram_photo_url,profile_updated_at,user_display_name FROM users WHERE id=?`, telegramID).Scan(
		&p.TelegramID, &p.FirstName, &p.LastName, &p.Username, &p.PhoneNumber, &verified, &p.PhotoURL, &p.UpdatedAt, &p.ProvidedName)
	p.PhoneVerified = verified == 1
	return p, err
}

// SetUserProvidedPhone persists a number only after explicit user consent.
// Manually entered numbers remain unverified until a separate verification flow exists.
func (s *Store) SetUserProvidedPhone(telegramID int64, raw string, consent bool) error {
	if !consent {
		return errors.New("explicit consent required")
	}
	phone, err := normalizePhone(raw)
	if err != nil {
		return err
	}
	result, err := s.DB.Exec(`UPDATE users SET phone_number=?,phone_verified=0,profile_updated_at=? WHERE id=?`, phone, time.Now().UTC().Format(time.RFC3339Nano), telegramID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return errors.New("Telegram user not found")
	}
	return nil
}

func (s *Store) SetUserProvidedDisplayName(telegramID int64, raw string, consent bool) error {
	if !consent {
		return errors.New("explicit consent required")
	}
	name := boundedProfileField(raw, 128)
	if name == "" {
		return errors.New("display name is required")
	}
	result, err := s.DB.Exec(`UPDATE users SET user_display_name=?,profile_updated_at=? WHERE id=?`, name, time.Now().UTC().Format(time.RFC3339Nano), telegramID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return errors.New("Telegram user not found")
	}
	return nil
}

// UpdateUserProvidedProfile atomically applies voluntarily supplied details.
// A submitted phone always becomes unverified; Telegram-owned fields are never changed.
func (s *Store) UpdateUserProvidedProfile(telegramID int64, name, rawPhone string, consent, clearName, clearPhone bool) error {
	name = strings.TrimSpace(name)
	phone := ""
	if name != "" {
		if len(name) > 128 {
			return errors.New("display name is too long")
		}
		if !consent {
			return errors.New("explicit consent required")
		}
	}
	if strings.TrimSpace(rawPhone) != "" {
		if !consent {
			return errors.New("explicit consent required")
		}
		var err error
		phone, err = normalizePhone(rawPhone)
		if err != nil {
			return err
		}
	}
	if name == "" && strings.TrimSpace(rawPhone) == "" && !clearName && !clearPhone {
		return errors.New("no profile changes supplied")
	}
	nameSet := name != ""
	phoneSet := strings.TrimSpace(rawPhone) != ""
	result, err := s.DB.Exec(`UPDATE users SET
		user_display_name=CASE WHEN ? THEN ? WHEN ? THEN '' ELSE user_display_name END,
		phone_number=CASE WHEN ? THEN ? WHEN ? THEN '' ELSE phone_number END,
		phone_verified=CASE WHEN ? OR ? THEN 0 ELSE phone_verified END,
		profile_updated_at=? WHERE id=?`,
		nameSet, name, clearName, phoneSet, phone, clearPhone, phoneSet, clearPhone, time.Now().UTC().Format(time.RFC3339Nano), telegramID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return errors.New("Telegram user not found")
	}
	return nil
}

func (s *Store) ClearUserProvidedDisplayName(telegramID int64) error {
	result, err := s.DB.Exec(`UPDATE users SET user_display_name='',profile_updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), telegramID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return errors.New("Telegram user not found")
	}
	return nil
}

func (s *Store) ClearUserProvidedPhone(telegramID int64) error {
	result, err := s.DB.Exec(`UPDATE users SET phone_number='',phone_verified=0,profile_updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), telegramID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return errors.New("Telegram user not found")
	}
	return nil
}

func normalizePhone(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' && b.Len() == 0:
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '(' || r == ')':
		default:
			return "", errors.New("phone must use international format")
		}
	}
	phone := b.String()
	if len(phone) < 9 || len(phone) > 16 || !strings.HasPrefix(phone, "+") {
		return "", errors.New("phone must use E.164 international format")
	}
	return phone, nil
}
