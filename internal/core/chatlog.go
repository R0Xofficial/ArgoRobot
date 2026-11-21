package core

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/i18n"
	"argo/internal/text"
)

// Chat logs: a chat's moderation actions, copied to a chat the admins chose (/setlog). A chat without a log costs
// one cached lookup. Sending never raises: a log that can't be reached must not break a command.

type logsCache struct {
	chats     cache[int64, int64] // 0 = the chat has no log
	mu        sync.Mutex
	complaint map[int64]time.Time
}

func (c *logsCache) drop(chatID int64) { c.chats.drop(chatID) }

const complainEvery = time.Hour

// LogChat is the chat that receives this chat's log, or 0.
func (a *App) LogChat(ctx context.Context, chatID int64) (int64, error) {
	if v, ok := a.logs.chats.get(chatID); ok {
		return v, nil
	}
	var id int64
	found, err := a.DB.Found(ctx, "SELECT log_chat_id FROM chat_logs WHERE chat_id = $1", []any{chatID}, &id)
	if err != nil {
		return 0, err
	}
	if !found {
		id = 0
	}
	a.logs.chats.put(chatID, id)
	return id, nil
}

// Detail is one extra line of a log entry, made in the language of the log chat: (label, value as HTML).
type Detail func(l i18n.Lang) (label, value string)

// Entry is a log entry:
//
//	#BANNED
//	Chat: title [id]
//	Admin: name
//	User: name [id]
//	Reason: ...
//
// Admin, user and detail values are HTML made by the caller; the reason is plain text. A note (plain text) goes
// last, in italics, after an empty line.
func Entry(l i18n.Lang, tag string, chat gotgbot.Chat, admin, user, reason string, details []Detail, note string) string {
	title := chat.Title
	if title == "" {
		title = l.T("chatlog.chat")
	}
	lines := []string{"<b>#" + tag + "</b>", "<b>" + l.T("chatlog.chat") + ":</b> " + text.Esc(title) + " [<code>" + itoa(chat.Id) + "</code>]"}
	if admin != "" {
		lines = append(lines, "<b>"+l.T("chatlog.admin")+":</b> "+admin)
	}
	if user != "" {
		lines = append(lines, "<b>"+l.T("chatlog.user")+":</b> "+user)
	}
	for _, d := range details {
		label, value := d(l)
		lines = append(lines, "<b>"+text.Esc(label)+":</b> "+value)
	}
	if r := strings.TrimSpace(reason); r != "" {
		lines = append(lines, "<b>"+l.T("chatlog.reason")+":</b> "+text.Esc(text.Truncate(r, 300, "")))
	}
	out := strings.Join(lines, "\n")
	if note != "" {
		out += "\n\n<i>" + text.Esc(note) + "</i>"
	}
	return out
}

// RecordLog sends one entry to the chat's log, if it has one. It returns whether something was sent.
// noteTag and reasonTag are tags whose text is written in the log chat's language ("" for none).
func (c *Ctx) RecordLog(chat gotgbot.Chat, tag, admin, user, reason, reasonTag string, details []Detail, noteTag string) bool {
	a := c.App
	target, err := a.LogChat(c.Ctx, chat.Id)
	if err != nil {
		slog.Warn("Could not read the log of a chat", "chat", chat.Id, "err", err)
		return false
	}
	if target == 0 {
		return false
	}
	l := a.UseChat(c.Ctx, target) // the entry is written in the language of the log chat
	note := ""
	if noteTag != "" {
		note = l.T(noteTag)
	}
	if reasonTag != "" {
		reason = l.T(reasonTag)
	}
	body := Entry(l, tag, chat, admin, user, reason, details, note)
	_, err = c.Bot.SendMessageWithContext(c.Ctx, target, body, &gotgbot.SendMessageOpts{
		ParseMode:          parseHTML,
		LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true},
	})
	if err != nil {
		a.logs.mu.Lock()
		defer a.logs.mu.Unlock()
		if a.logs.complaint == nil {
			a.logs.complaint = map[int64]time.Time{}
		}
		if time.Since(a.logs.complaint[chat.Id]) >= complainEvery {
			a.logs.complaint[chat.Id] = time.Now()
			slog.Warn("Could not write the log of a chat", "chat", chat.Id, "log", target, "err", err)
		}
		return false
	}
	return true
}
