package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/text"
)

// Error reports for the developer log (core/errors.py): a caption (Bot Error Detected! with the error, the chat and
// the user) and a .txt file with the details. Telegram API and network errors are not reported this way, because the
// report itself goes through Telegram and would most likely fail for the same reason; they are only logged.

// errorReportCooldown: the same error is reported at most once in this time, so a failing loop can't flood the log.
const errorReportCooldown = 60 * time.Second

// describeContext names the chat and the user of an update, "N/A" when unknown.
func describeContext(u *gotgbot.Update) (chat, user string) {
	chat, user = "N/A", "N/A"
	if u == nil {
		return
	}
	var ch *gotgbot.Chat
	var from *gotgbot.User
	switch {
	case u.Message != nil:
		ch, from = &u.Message.Chat, u.Message.From
	case u.EditedMessage != nil:
		ch, from = &u.EditedMessage.Chat, u.EditedMessage.From
	case u.ChannelPost != nil:
		ch, from = &u.ChannelPost.Chat, u.ChannelPost.From
	case u.EditedChannelPost != nil:
		ch, from = &u.EditedChannelPost.Chat, u.EditedChannelPost.From
	case u.CallbackQuery != nil:
		from = &u.CallbackQuery.From
		if u.CallbackQuery.Message != nil {
			c := u.CallbackQuery.Message.GetChat()
			ch = &c
		}
	case u.MyChatMember != nil:
		ch, from = &u.MyChatMember.Chat, &u.MyChatMember.From
	case u.ChatMember != nil:
		ch, from = &u.ChatMember.Chat, &u.ChatMember.From
	case u.ChatJoinRequest != nil:
		ch, from = &u.ChatJoinRequest.Chat, &u.ChatJoinRequest.From
	}
	if ch != nil {
		name := ch.Title
		if name == "" {
			name = ch.Type
		}
		chat = fmt.Sprintf("%s (%d)", name, ch.Id)
	}
	if from != nil {
		label := "@" + from.Username
		if from.Username == "" {
			label = FullName(from)
			if label == "" {
				label = "user"
			}
		}
		user = fmt.Sprintf("%s (%d)", label, from.Id)
	}
	return
}

// isDeliveryError tells the errors of Telegram and of the network, which are only logged.
func isDeliveryError(err error) bool {
	var tg *gotgbot.TelegramError
	var nerr net.Error
	return errors.As(err, &tg) || errors.As(err, &nerr) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// errorLine is "type: first line of the message".
func errorLine(err any) string {
	name := fmt.Sprintf("%T", err)
	msg := strings.TrimSpace(fmt.Sprint(err))
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	return strings.TrimRight(name+": "+msg, " ")
}

// BuildErrorReport returns the caption (HTML), the file name and the file content of an error report.
func BuildErrorReport(what string, err any, u *gotgbot.Update, stack []byte, now time.Time) (caption, name string, data []byte) {
	now = now.UTC()
	chat, user := describeContext(u)
	line := errorLine(err)
	caption = "<b>Bot Error Detected!</b>\n\n" +
		"Error: " + text.Esc(text.Truncate(line, 300)) + "\n" +
		"In: " + text.Esc(text.Truncate(what, 100)) + "\n" +
		"Chat: " + text.Esc(text.Truncate(chat, 100)) + "\n" +
		"User: " + text.Esc(text.Truncate(user, 100)) + "\n\n" +
		"Full details are attached as a file."
	updateID := "N/A"
	if u != nil {
		updateID = fmt.Sprint(u.UpdateId)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Time (UTC): %s\nError: %s\nIn: %s\nUpdate id: %s\nChat: %s\nUser: %s\n\n",
		now.Format("2006-01-02 15:04:05"), line, what, updateID, chat, user)
	if len(stack) > 0 {
		b.Write(stack)
	} else {
		fmt.Fprintf(&b, "Full error: %v\n(an error returned by the handler, there is no stack to show)\n", err)
	}
	return caption, "traceback_" + now.Format("20060102_150405") + ".txt", []byte(b.String())
}

// throttled says whether this error was reported lately; when it was not, it is marked as reported now.
func (a *App) throttled(err any) bool {
	key := fmt.Sprintf("%T:%.100s", err, fmt.Sprint(err))
	now := time.Now()
	a.errMu.Lock()
	defer a.errMu.Unlock()
	if a.lastReport == nil {
		a.lastReport = map[string]time.Time{}
	}
	if len(a.lastReport) > 100 {
		for k, t := range a.lastReport {
			if now.Sub(t) > errorReportCooldown {
				delete(a.lastReport, k)
			}
		}
	}
	if t, ok := a.lastReport[key]; ok && now.Sub(t) < errorReportCooldown {
		return true
	}
	a.lastReport[key] = now
	return false
}

// reportError tells the developer log about an unexpected failure: the caption and a file with the details. c and
// stack may be nil. Errors of Telegram and of the network are only logged.
func (a *App) reportError(what string, err any, c *Ctx, stack []byte) {
	if e, ok := err.(error); ok && isDeliveryError(e) {
		logErr("Telegram or network error in "+what, e)
		return
	}
	if a.throttled(err) {
		return
	}
	var u *gotgbot.Update
	if c != nil {
		u = c.Update
	}
	caption, name, data := BuildErrorReport(what, err, u, stack, time.Now())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a.DevFile(ctx, name, data, caption)
}
