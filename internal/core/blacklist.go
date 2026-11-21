package core

import (
	"context"

	"argo/internal/blacklist"
	"argo/internal/i18n"
)

// The triggers and settings of every chat's blacklist. Every message of every
// chat is checked, so each chat's triggers are compiled once and kept in memory; a chat without triggers costs nothing.

// BlacklistSettings are the blacklist settings of a chat.
type BlacklistSettings struct {
	Mode          string // off, del, warn, ban, kick, mute, tban, tmute
	ModeSeconds   int
	DeleteMatches bool
}

// Triggers are the triggers of a chat, in the order they were added.
func (a *App) Triggers(ctx context.Context, chatID int64) ([]*blacklist.Trigger, error) {
	if cached, ok := a.blTriggers.get(chatID); ok {
		return cached, nil
	}
	rows, err := a.DB.Pool.Query(ctx, `SELECT pattern, action, action_seconds, reason FROM blacklist WHERE chat_id = $1 ORDER BY id`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	built := []*blacklist.Trigger{}
	for rows.Next() {
		var pattern string
		var action, reason *string
		var seconds int
		if err := rows.Scan(&pattern, &action, &seconds, &reason); err != nil {
			return nil, err
		}
		t, err := blacklist.Build(i18n.English, pattern, deref(action), seconds, deref(reason))
		if err != nil {
			continue // a stored trigger that can no longer be used is skipped, not a reason to fail every message
		}
		built = append(built, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	a.blTriggers.put(chatID, built)
	return built, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// AddTrigger adds a trigger, or changes the action and reason of an existing one. isNew is false when it was updated;
// full is true when the chat has no room for another.
func (a *App) AddTrigger(ctx context.Context, l i18n.Lang, chatID int64, pattern, action string, seconds int, reason string, addedBy int64) (isNew, full bool, err error) {
	current, err := a.Triggers(ctx, chatID)
	if err != nil {
		return false, false, err
	}
	known := false
	for _, t := range current {
		if t.Pattern == pattern {
			known = true
		}
	}
	if !known && len(current) >= blacklist.MaxTriggers {
		return false, true, nil
	}
	if _, err := blacklist.Build(l, pattern, action, seconds, reason); err != nil { // refuse what can't be used before saving
		return false, false, err
	}
	var addedByV any
	if addedBy != 0 {
		addedByV = addedBy
	}
	_, err = a.DB.Exec(ctx, `INSERT INTO blacklist (chat_id, pattern, action, action_seconds, reason, added_by)
VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (chat_id, pattern) DO UPDATE SET action = EXCLUDED.action,
action_seconds = EXCLUDED.action_seconds, reason = EXCLUDED.reason`, chatID, pattern, nullable(action), seconds, nullable(reason), addedByV)
	a.blTriggers.drop(chatID)
	return !known, false, err
}

func (a *App) RemoveTrigger(ctx context.Context, chatID int64, pattern string) (bool, error) {
	n, err := a.DB.Exec(ctx, `DELETE FROM blacklist WHERE chat_id = $1 AND pattern = $2`, chatID, pattern)
	a.blTriggers.drop(chatID)
	return n > 0, err
}

func (a *App) ClearTriggers(ctx context.Context, chatID int64) (int64, error) {
	n, err := a.DB.Exec(ctx, `DELETE FROM blacklist WHERE chat_id = $1`, chatID)
	a.blTriggers.drop(chatID)
	return n, err
}

// BlacklistSettings of a chat (the defaults when nothing was set).
func (a *App) BlacklistSettings(ctx context.Context, chatID int64) (BlacklistSettings, error) {
	if cached, ok := a.blSettings.get(chatID); ok {
		return cached, nil
	}
	s := BlacklistSettings{Mode: "del", DeleteMatches: true}
	if _, err := a.DB.Found(ctx, `SELECT mode, mode_seconds, delete_matches FROM blacklist_settings WHERE chat_id = $1`,
		[]any{chatID}, &s.Mode, &s.ModeSeconds, &s.DeleteMatches); err != nil {
		return s, err
	}
	a.blSettings.put(chatID, s)
	return s, nil
}

// SetBlacklistMode sets the default action.
func (a *App) SetBlacklistMode(ctx context.Context, chatID int64, mode string, seconds int) error {
	if _, err := a.DB.Exec(ctx, `INSERT INTO blacklist_settings (chat_id, mode, mode_seconds) VALUES ($1, $2, $3)
ON CONFLICT (chat_id) DO UPDATE SET mode = EXCLUDED.mode, mode_seconds = EXCLUDED.mode_seconds`, chatID, mode, seconds); err != nil {
		return err
	}
	a.blSettings.drop(chatID)
	return nil
}

// SetBlacklistDelete sets whether blacklisted messages are deleted whatever the action.
func (a *App) SetBlacklistDelete(ctx context.Context, chatID int64, on bool) error {
	if _, err := a.DB.Exec(ctx, `INSERT INTO blacklist_settings (chat_id, delete_matches) VALUES ($1, $2)
ON CONFLICT (chat_id) DO UPDATE SET delete_matches = EXCLUDED.delete_matches`, chatID, on); err != nil {
		return err
	}
	a.blSettings.drop(chatID)
	return nil
}
