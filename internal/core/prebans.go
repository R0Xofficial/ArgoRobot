package core

import (
	"context"
	"sync"
	"time"
)

// Bans and mutes saved for people Telegram won't let the bot touch yet.
//
// A ban of an ID the bot has never seen is refused by Telegram ("user not found"). The ban is saved instead, and
// modules/bans.go applies it as soon as that person writes or joins the chat. A timed ban keeps its end time.
// A mute is saved the same way. One person has one saved action per chat, and a ban outranks a mute.

// Kinds of saved actions.
const (
	PrebanBan  = "ban"
	PrebanMute = "mute"
)

// Preban is a saved ban or mute.
type Preban struct {
	Kind   string // PrebanBan (when empty) or PrebanMute
	Reason string
	Until  *time.Time
	ByID   int64
	ByName string
}

type prebanState struct {
	mu     sync.Mutex
	loaded bool
	set    map[[2]int64]bool // (chat, user): so a message costs no query
}

func (a *App) prebanSet(ctx context.Context) map[[2]int64]bool {
	if !a.prebans.loaded {
		a.prebans.set = map[[2]int64]bool{}
		rows, err := a.DB.Pool.Query(ctx, "SELECT chat_id, user_id FROM chat_prebans")
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var chat, user int64
				if rows.Scan(&chat, &user) == nil {
					a.prebans.set[[2]int64{chat, user}] = true
				}
			}
			a.prebans.loaded = true
		}
	}
	return a.prebans.set
}

// AddPreban saves a ban for later (replacing an earlier one for the same person).
func (a *App) AddPreban(ctx context.Context, chatID, userID int64, p Preban) error {
	if p.Kind == "" {
		p.Kind = PrebanBan
	}
	// A mute never replaces a saved ban.
	_, err := a.DB.Exec(ctx, `INSERT INTO chat_prebans (chat_id, user_id, reason, until, by_id, by_name, kind)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (chat_id, user_id) DO UPDATE
		SET reason = EXCLUDED.reason, until = EXCLUDED.until, by_id = EXCLUDED.by_id, by_name = EXCLUDED.by_name,
		    kind = EXCLUDED.kind
		WHERE EXCLUDED.kind = 'ban' OR chat_prebans.kind <> 'ban'`,
		chatID, userID, p.Reason, p.Until, p.ByID, p.ByName, p.Kind)
	if err == nil {
		a.prebans.mu.Lock()
		a.prebanSet(ctx)[[2]int64{chatID, userID}] = true
		a.prebans.mu.Unlock()
	}
	return err
}

// HasPreban tells whether a ban is saved for the person here.
func (a *App) HasPreban(ctx context.Context, chatID, userID int64) bool {
	a.prebans.mu.Lock()
	defer a.prebans.mu.Unlock()
	return a.prebanSet(ctx)[[2]int64{chatID, userID}]
}

// TakePreban removes and returns the saved ban, unless it has run out in the meantime.
func (a *App) TakePreban(ctx context.Context, chatID, userID int64) (*Preban, error) {
	var p Preban
	var byID *int64
	var byName *string
	found, err := a.DB.Found(ctx, `DELETE FROM chat_prebans WHERE chat_id = $1 AND user_id = $2 RETURNING reason, until, by_id, by_name, kind`,
		[]any{chatID, userID}, &p.Reason, &p.Until, &byID, &byName, &p.Kind)
	a.prebans.mu.Lock()
	delete(a.prebanSet(ctx), [2]int64{chatID, userID})
	a.prebans.mu.Unlock()
	if err != nil || !found {
		return nil, err
	}
	if byID != nil {
		p.ByID = *byID
	}
	if byName != nil {
		p.ByName = *byName
	}
	if p.Until != nil && !p.Until.After(time.Now()) {
		return nil, nil
	}
	return &p, nil
}

// RemovePreban forgets a saved ban or mute of that kind (an unban, an unmute). True when there was one.
func (a *App) RemovePreban(ctx context.Context, chatID, userID int64, kind string) bool {
	found, _ := a.DB.Found(ctx, `DELETE FROM chat_prebans WHERE chat_id = $1 AND user_id = $2 AND kind = $3 RETURNING user_id`,
		[]any{chatID, userID, kind}, new(int64))
	if found {
		a.prebans.mu.Lock()
		delete(a.prebanSet(ctx), [2]int64{chatID, userID})
		a.prebans.mu.Unlock()
	}
	return found
}
