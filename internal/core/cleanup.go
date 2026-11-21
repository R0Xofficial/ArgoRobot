package core

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/text"
)

// Cleaning the stored chats.
//
// A chat is dropped (together with everything kept for it) when the bot learns it was removed: right away from a
// my_chat_member update, and by this cleanup that asks Telegram about every stored chat. It only removes a chat
// on definite evidence; when Telegram can't be reached the chat is kept and reported as unverified.

const (
	checkDelay        = 100 * time.Millisecond // between Telegram calls while cleaning (about 10 a second)
	firstCleanupDelay = 10 * time.Minute       // the first scheduled cleanup, after start-up
	reportTitled      = 15                     // removed chats shown with their title
	reportLimit       = 3500                   // characters; a Telegram message holds 4096
)

// CleanedChat is a stored chat.
type CleanedChat struct {
	ID    int64
	Type  string
	Title string
}

// CleanupResult is what one cleanup found.
type CleanupResult struct {
	Checked    int
	Removed    []CleanedChat
	Moved      int
	Unverified int
	Leftovers  []int64 // chats the bot was no longer in whose data was still lying around
	Restored   int     // chats that have data but were missing from `chats` and are still there: recorded again
}

// IsCleaning tells whether a cleanup is running.
func (a *App) IsCleaning() bool {
	if a.cleanup.TryLock() {
		a.cleanup.Unlock()
		return false
	}
	return true
}

type botState int

const (
	statePresent botState = iota
	stateGone
	stateMoved
	stateUnknown
)

// botStatus tells whether the bot is still in a chat. Only definite answers count.
func (a *App) botStatus(ctx context.Context, chatID int64) (botState, bool, int64) {
	for attempt := 1; attempt <= 2; attempt++ {
		member, err := a.Bot.GetChatMemberWithContext(ctx, chatID, a.Bot.Id, nil)
		if err != nil {
			tg := TgError(err)
			if tg == nil {
				return stateUnknown, false, 0
			}
			if tg.ResponseParams != nil && tg.ResponseParams.MigrateToChatId != 0 {
				return stateMoved, false, tg.ResponseParams.MigrateToChatId
			}
			if tg.Code == 429 && attempt == 1 {
				wait := time.Second
				if tg.ResponseParams != nil && tg.ResponseParams.RetryAfter > 0 {
					wait = time.Duration(min(tg.ResponseParams.RetryAfter, 60)) * time.Second
				}
				select {
				case <-time.After(wait):
					continue
				case <-ctx.Done():
					return stateUnknown, false, 0
				}
			}
			p := strings.ToLower(tg.Description)
			if tg.Code == 403 || strings.Contains(p, "chat not found") || strings.Contains(p, "not a member") {
				return stateGone, false, 0
			}
			return stateUnknown, false, 0
		}
		status := member.GetStatus()
		if status == "left" || status == "kicked" {
			return stateGone, false, 0
		}
		return statePresent, status == "administrator" || status == "creator", 0
	}
	return stateUnknown, false, 0
}

func (a *App) allChats(ctx context.Context) ([]CleanedChat, error) {
	rows, err := a.DB.Pool.Query(ctx, "SELECT chat_id, type, title FROM chats ORDER BY chat_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CleanedChat
	for rows.Next() {
		var c CleanedChat
		var title *string
		if err := rows.Scan(&c.ID, &c.Type, &title); err != nil {
			return nil, err
		}
		if title != nil {
			c.Title = *title
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// leftovers are chat ids that have data in some table but no row in `chats` (only groups and channels: their ids
// are negative): what a chat leaves behind when it was removed by an older version of the bot.
func (a *App) leftovers(ctx context.Context) ([]int64, error) {
	var parts []string
	for _, table := range chatTables {
		var exists bool
		if err := a.DB.Row(ctx, "SELECT to_regclass($1) IS NOT NULL", []any{"public." + table}, &exists); err != nil {
			return nil, err
		}
		if exists {
			parts = append(parts, fmt.Sprintf("SELECT chat_id FROM %s WHERE chat_id < 0 AND chat_id NOT IN (SELECT chat_id FROM chats)", table))
		}
	}
	if len(parts) == 0 {
		return nil, nil
	}
	rows, err := a.DB.Pool.Query(ctx, strings.Join(parts, " UNION ")+" ORDER BY chat_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func pause(ctx context.Context, d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

// CleanChats checks every stored chat and drops those the bot is no longer in. It returns nil when another cleanup
// is already running.
func (a *App) CleanChats(ctx context.Context) (*CleanupResult, error) {
	if !a.cleanup.TryLock() {
		return nil, nil
	}
	defer a.cleanup.Unlock()
	result := &CleanupResult{}
	chats, err := a.allChats(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range chats {
		result.Checked++
		state, admin, moved := a.botStatus(ctx, row.ID)
		switch state {
		case stateGone:
			if err := a.removeChat(ctx, row.ID); err != nil {
				return result, err
			}
			result.Removed = append(result.Removed, row)
		case stateMoved:
			if err := a.migrateChat(ctx, row.ID, moved); err != nil {
				return result, err
			}
			result.Moved++
		case statePresent:
			if _, err := a.DB.Exec(ctx, "UPDATE chats SET bot_admin = $2, last_seen = now() WHERE chat_id = $1", row.ID, admin); err != nil {
				return result, err
			}
		default:
			result.Unverified++
		}
		if !pause(ctx, checkDelay) {
			return result, ctx.Err()
		}
	}
	return result, a.clearLeftovers(ctx, result)
}

// clearLeftovers asks Telegram about each chat whose data is left behind: a chat the bot is still in is recorded
// again, never deleted.
func (a *App) clearLeftovers(ctx context.Context, result *CleanupResult) error {
	ids, err := a.leftovers(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		state, admin, moved := a.botStatus(ctx, id)
		switch state {
		case stateGone:
			if err := a.removeChat(ctx, id); err != nil {
				return err
			}
			result.Leftovers = append(result.Leftovers, id)
		case stateMoved:
			if err := a.migrateChat(ctx, id, moved); err != nil {
				return err
			}
			result.Moved++
		case statePresent:
			chat, err := a.Bot.GetChatWithContext(ctx, id, nil)
			if err != nil {
				result.Unverified++
				break
			}
			plain := gotgbot.Chat{Id: chat.Id, Type: chat.Type, Title: chat.Title, Username: chat.Username}
			if err := a.rememberChat(ctx, plain, &admin); err != nil {
				return err
			}
			result.Restored++
		default:
			result.Unverified++
		}
		if !pause(ctx, checkDelay) {
			return ctx.Err()
		}
	}
	return nil
}

func chatLine(c CleanedChat) string {
	title := c.Title
	if title == "" {
		title = "Untitled"
	}
	return fmt.Sprintf("%s [<code>%d</code>]", text.Esc(title), c.ID)
}

// idLines are chat ids as <code> items, as many as fit; the rest is counted.
func idLines(ids []int64, used int) []string {
	var shown []string
	size := used
	for _, id := range ids {
		item := fmt.Sprintf("<code>%d</code>", id)
		if size+len(item)+2 > reportLimit {
			break
		}
		shown = append(shown, item)
		size += len(item) + 2
	}
	var lines []string
	if len(shown) > 0 {
		lines = append(lines, strings.Join(shown, ", "))
	}
	if len(shown) < len(ids) {
		lines = append(lines, fmt.Sprintf("... and %d more.", len(ids)-len(shown)))
	}
	return lines
}

func linesSize(lines []string) int {
	n := 0
	for _, l := range lines {
		n += len(l) + 1
	}
	return n
}

// CleanupReport is the text that tells what a cleanup did.
func CleanupReport(r *CleanupResult, title string) string {
	lines := []string{
		"<b>" + text.Esc(title) + "</b>",
		"",
		fmt.Sprintf("Checked: <code>%d</code>", r.Checked),
		fmt.Sprintf("Removed: <code>%d</code>", len(r.Removed)),
	}
	if len(r.Leftovers) > 0 {
		lines = append(lines, fmt.Sprintf("Leftover data deleted for: <code>%d</code> chats", len(r.Leftovers)))
	}
	if r.Restored > 0 {
		lines = append(lines, fmt.Sprintf("Recorded again (the bot is still in them): <code>%d</code>", r.Restored))
	}
	if r.Moved > 0 {
		lines = append(lines, fmt.Sprintf("Moved to a new id: <code>%d</code>", r.Moved))
	}
	if r.Unverified > 0 {
		lines = append(lines, fmt.Sprintf("Could not be verified: <code>%d</code>", r.Unverified))
	}
	if len(r.Removed) > 0 {
		lines = append(lines, "", "<b>Removed chats:</b>")
		for i, c := range r.Removed {
			if i >= reportTitled {
				break
			}
			lines = append(lines, chatLine(c))
		}
		if len(r.Removed) > reportTitled {
			var ids []int64
			for _, c := range r.Removed[reportTitled:] {
				ids = append(ids, c.ID)
			}
			lines = append(lines, idLines(ids, linesSize(lines))...)
		}
	}
	if len(r.Leftovers) > 0 {
		lines = append(lines, "", "<b>Leftover data deleted for:</b>")
		lines = append(lines, idLines(r.Leftovers, linesSize(lines))...)
	}
	return strings.Join(lines, "\n")
}

// RunCleanupSchedule checks the chats every `hours` hours until ctx ends. 0 hours means never.
func (a *App) RunCleanupSchedule(ctx context.Context, hours float64) {
	if hours <= 0 {
		return
	}
	if !pause(ctx, firstCleanupDelay) {
		return
	}
	for {
		result, err := a.CleanChats(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			a.ReportError("the chat cleanup", err)
		case result != nil:
			slog.Info("Chat cleanup", "checked", result.Checked, "removed", len(result.Removed),
				"leftovers", len(result.Leftovers), "unverified", result.Unverified)
			a.DevLog(ctx, CleanupReport(result, "Automatic chat cleanup"))
		}
		if !pause(ctx, time.Duration(hours*float64(time.Hour))) {
			return
		}
	}
}

// RemoveChat forgets a chat the bot no longer belongs to and deletes everything kept for it.
func (a *App) RemoveChat(ctx context.Context, chatID int64) error { return a.removeChat(ctx, chatID) }
