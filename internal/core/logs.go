package core

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/text"
)

// Log destinations: the developer log (DEV_LOG_CHAT, else the owner's private chat) and the user log
// (LOG_CHAT_ID, else the developer log) for promotions and demotions of staff and other public information.
// Sending never fails: a missing log chat must not break a command.

func (a *App) roleTarget() (int64, int64) {
	if a.Cfg.LogChatID != 0 {
		return a.Cfg.LogChatID, a.Cfg.LogThreadID
	}
	if a.Cfg.DevLogChat != 0 {
		return a.Cfg.DevLogChat, a.Cfg.DevLogThread
	}
	return a.Cfg.OwnerID, 0
}

// RoleLog sends a message to the user log (role changes, global bans ...), optionally with buttons.
func (a *App) RoleLog(ctx context.Context, msg string, markup *gotgbot.InlineKeyboardMarkup) bool {
	chat, thread := a.roleTarget()
	opts := &gotgbot.SendMessageOpts{ParseMode: parseHTML, MessageThreadId: thread, LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true}}
	if markup != nil {
		opts.ReplyMarkup = *markup
	}
	if _, err := a.Bot.SendMessageWithContext(ctx, chat, msg, opts); err != nil {
		slog.Warn("Could not send a role log message", "chat", chat, "err", err)
		return false
	}
	return true
}

// SendTo sends a message to somebody's private chat and returns its id; false when they can't be reached
// (they never started the bot, or blocked it).
func (a *App) SendTo(ctx context.Context, chat int64, msg string, markup *gotgbot.InlineKeyboardMarkup) (int64, bool) {
	if chat <= 0 {
		return 0, false
	}
	opts := &gotgbot.SendMessageOpts{ParseMode: parseHTML, LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true}}
	if markup != nil {
		opts.ReplyMarkup = *markup
	}
	sent, err := a.Bot.SendMessageWithContext(ctx, chat, msg, opts)
	if err != nil {
		slog.Debug("Could not message", "chat", chat, "err", err)
		return 0, false
	}
	return sent.MessageId, true
}

// Timestamp is a time as shown in the logs: 2026-10-29 18:09:00 UTC, in a <code> tag.
func Timestamp(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	return "<code>" + t.UTC().Format("2006-01-02 15:04:05") + " UTC</code>"
}

// AuditText is the user log entry for something an admin did to a user (a role change, a whitelist change ...).
func AuditText(tag string, userID int64, name string, adminID int64, adminName string) string {
	return fmt.Sprintf("<b>%s</b>\n<b>User:</b> %s\n<b>Timestamp:</b> %s\n<b>Admin:</b> %s",
		tag, text.UserRef(userID, name), Timestamp(time.Time{}), text.UserRef(adminID, adminName))
}
