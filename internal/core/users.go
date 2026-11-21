package core

import (
	"context"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

// The phone book of the users the bot has seen. Telegram only tells a bot
// about users it is in contact with, so the bot writes down everyone it meets and uses the book later to turn
// @username into an account. A user is written only when something changed, or once an hour.
const refreshEvery = time.Hour

// Accounts Telegram shows as the sender of messages written on behalf of a chat; never recorded as users.
var placeholderSenders = map[int64]bool{777000: true, 1087968824: true, 136817688: true}

// Telegram's own accounts: no action may ever be taken against them.
var serviceAccounts = map[int64]bool{777000: true, 1087968824: true, 136817688: true, 93372553: true}

func IsImmune(id int64) bool { return serviceAccounts[id] }

type seenUser struct {
	name, username string
	at             time.Time
}

type usersCache struct{ cache[int64, seenUser] }

// FullName is first and last name.
func FullName(u *gotgbot.User) string {
	if u == nil {
		return ""
	}
	return strings.TrimSpace(u.FirstName + " " + u.LastName)
}

const upsertUser = `
WITH freed AS (
    UPDATE users SET username = NULL
    WHERE lower(username) = lower($3) AND user_id <> $1
)
INSERT INTO users (user_id, name, username, is_bot, first_name, last_name)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (user_id) DO UPDATE SET
    name = EXCLUDED.name,
    username = EXCLUDED.username,
    is_bot = EXCLUDED.is_bot,
    first_name = EXCLUDED.first_name,
    last_name = EXCLUDED.last_name,
    last_seen = now()`

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Remember records a user.
func (a *App) Remember(ctx context.Context, u *gotgbot.User) error {
	if u == nil || placeholderSenders[u.Id] {
		return nil
	}
	name, username := FullName(u), u.Username
	if c, ok := a.users.get(u.Id); ok && c.name == name && c.username == username && time.Since(c.at) < refreshEvery {
		return nil
	}
	if _, err := a.DB.Exec(ctx, upsertUser, u.Id, nullable(name), nullable(username), u.IsBot, nullable(u.FirstName), nullable(u.LastName)); err != nil {
		return err
	}
	a.users.put(u.Id, seenUser{name, username, time.Now()})
	return nil
}

// UserRow is a stored user.
type UserRow struct {
	ID        int64
	Name      string
	FirstName string
	LastName  string
	Username  string
	IsBot     bool
}

func (a *App) userQuery(ctx context.Context, where string, arg any) (*UserRow, error) {
	var (
		row                         UserRow
		name, first, last, username *string
	)
	found, err := a.DB.Found(ctx,
		"SELECT user_id, name, first_name, last_name, username, is_bot FROM users WHERE "+where,
		[]any{arg}, &row.ID, &name, &first, &last, &username, &row.IsBot)
	if err != nil || !found {
		return nil, err
	}
	deref := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	row.Name, row.FirstName, row.LastName, row.Username = deref(name), deref(first), deref(last), deref(username)
	return &row, nil
}

// UserByID is the stored record of a user id, or nil if the bot never met them.
func (a *App) UserByID(ctx context.Context, id int64) (*UserRow, error) {
	return a.userQuery(ctx, "user_id = $1", id)
}

// UserByUsername is the user currently known under @username (case does not matter).
func (a *App) UserByUsername(ctx context.Context, username string) (*UserRow, error) {
	return a.userQuery(ctx, "lower(username) = lower($1)", strings.TrimPrefix(username, "@"))
}
