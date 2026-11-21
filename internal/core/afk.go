package core

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// AFK ("away from keyboard") statuses. Every message of every chat is checked against them (is the sender
// back, does the message mention or answer someone who is away), so all statuses are kept in memory, loaded once.
// There are only as many as there are people away right now. The database makes them survive a restart.

const MaxAfkReason = 200

type AfkRecord struct {
	UserID               int64
	Name, Username       string
	Reason               string
	Since                time.Time
	MediaType, MediaFile string
}

type afkState struct {
	mu         sync.Mutex
	loaded     bool
	records    map[int64]*AfkRecord
	byUsername map[string]int64
}

func (a *App) afkLoad(ctx context.Context) error {
	s := &a.afk
	if s.loaded {
		return nil
	}
	rows, err := a.DB.Pool.Query(ctx, "SELECT user_id, COALESCE(name, ''), COALESCE(username, ''), reason, since, COALESCE(media_type, ''), COALESCE(media_file_id, '') FROM afk")
	if err != nil {
		return err
	}
	defer rows.Close()
	s.records, s.byUsername = map[int64]*AfkRecord{}, map[string]int64{}
	for rows.Next() {
		var r AfkRecord
		if err := rows.Scan(&r.UserID, &r.Name, &r.Username, &r.Reason, &r.Since, &r.MediaType, &r.MediaFile); err != nil {
			return err
		}
		s.records[r.UserID] = &r
		if r.Username != "" {
			s.byUsername[r.Username] = r.UserID
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.loaded = true
	slog.Info("Loaded AFK status(es)", "count", len(s.records))
	return nil
}

// AfkGet is the status of a person who is away, or nil.
func (a *App) AfkGet(ctx context.Context, userID int64) *AfkRecord {
	a.afk.mu.Lock()
	defer a.afk.mu.Unlock()
	if a.afkLoad(ctx) != nil {
		return nil
	}
	return a.afk.records[userID]
}

func (a *App) AfkAnyone(ctx context.Context) bool {
	a.afk.mu.Lock()
	defer a.afk.mu.Unlock()
	return a.afkLoad(ctx) == nil && len(a.afk.records) > 0
}

func (a *App) AfkByUsername(ctx context.Context, username string) *AfkRecord {
	a.afk.mu.Lock()
	defer a.afk.mu.Unlock()
	if a.afkLoad(ctx) != nil {
		return nil
	}
	id, ok := a.afk.byUsername[strings.ToLower(strings.TrimLeft(username, "@"))]
	if !ok {
		return nil
	}
	return a.afk.records[id]
}

// AfkSet marks a person as away from now on (or replaces an earlier status).
func (a *App) AfkSet(ctx context.Context, userID int64, name, username, reason, mediaType, mediaFile string) (*AfkRecord, error) {
	a.afk.mu.Lock()
	defer a.afk.mu.Unlock()
	if err := a.afkLoad(ctx); err != nil {
		return nil, err
	}
	r := &AfkRecord{UserID: userID, Name: name, Username: strings.ToLower(username), Reason: reason, Since: time.Now().UTC()}
	if mediaType != "" && mediaFile != "" {
		r.MediaType, r.MediaFile = mediaType, mediaFile
	}
	var uname, mt, mf any
	if r.Username != "" {
		uname = r.Username
	}
	if r.MediaType != "" {
		mt, mf = r.MediaType, r.MediaFile
	}
	if _, err := a.DB.Exec(ctx, `INSERT INTO afk (user_id, name, username, reason, since, media_type, media_file_id)
VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (user_id) DO UPDATE SET name = EXCLUDED.name, username = EXCLUDED.username,
reason = EXCLUDED.reason, since = EXCLUDED.since, media_type = EXCLUDED.media_type, media_file_id = EXCLUDED.media_file_id`,
		userID, name, uname, reason, r.Since, mt, mf); err != nil {
		return nil, err
	}
	if old := a.afk.records[userID]; old != nil && old.Username != "" {
		delete(a.afk.byUsername, old.Username)
	}
	a.afk.records[userID] = r
	if r.Username != "" {
		a.afk.byUsername[r.Username] = userID
	}
	return r, nil
}

// AfkClear: the person is back. It returns the status that ended, or nil when they were not away.
func (a *App) AfkClear(ctx context.Context, userID int64) (*AfkRecord, error) {
	a.afk.mu.Lock()
	defer a.afk.mu.Unlock()
	if err := a.afkLoad(ctx); err != nil {
		return nil, err
	}
	old := a.afk.records[userID]
	if old == nil {
		return nil, nil
	}
	delete(a.afk.records, userID)
	if old.Username != "" {
		delete(a.afk.byUsername, old.Username)
	}
	_, err := a.DB.Exec(ctx, "DELETE FROM afk WHERE user_id = $1", userID)
	return old, err
}
