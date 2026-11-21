package core

import (
	"errors"
	"log/slog"
	"strings"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/text"
)

const parseHTML = "HTML"

// TgError returns the Telegram error behind err, if there is one.
func TgError(err error) *gotgbot.TelegramError {
	var tg *gotgbot.TelegramError
	if errors.As(err, &tg) {
		return tg
	}
	return nil
}

func problem(err error) string {
	if tg := TgError(err); tg != nil {
		return strings.ToLower(tg.Description)
	}
	return strings.ToLower(err.Error())
}

// Reply sends text where the message was sent: same chat, same topic. In groups the answer is attached to the
// message (even if it is deleted meanwhile); private chats stay clean. Broken markup is resent as plain text.
func (c *Ctx) Reply(msg string, markup *gotgbot.InlineKeyboardMarkup) (*gotgbot.Message, error) {
	opts := &gotgbot.SendMessageOpts{ParseMode: parseHTML}
	if markup != nil {
		opts.ReplyMarkup = *markup
	}
	m := c.Msg
	if m.IsTopicMessage {
		opts.MessageThreadId = m.MessageThreadId
	}
	if m.Chat.Type != "private" && m.MessageId != 0 {
		opts.ReplyParameters = &gotgbot.ReplyParameters{MessageId: m.MessageId, AllowSendingWithoutReply: true}
	}
	return c.send(m.Chat.Id, msg, opts)
}

// SendPlain sends text to the chat and topic of the message without attaching it to the message.
func (c *Ctx) SendPlain(msg string) (*gotgbot.Message, error) {
	opts := &gotgbot.SendMessageOpts{ParseMode: parseHTML}
	if c.Msg.IsTopicMessage {
		opts.MessageThreadId = c.Msg.MessageThreadId
	}
	return c.send(c.Msg.Chat.Id, msg, opts)
}

// ReplyText is Reply without buttons.
func (c *Ctx) ReplyText(msg string) (*gotgbot.Message, error) { return c.Reply(msg, nil) }

// Say is for handlers that only want to answer: errors are logged, not returned.
func (c *Ctx) Say(msg string) {
	if _, err := c.Reply(msg, nil); err != nil {
		slog.Warn("Could not answer", "err", err)
	}
}

// SayTo sends a message to another chat (no reply, no topic).
func (c *Ctx) SayTo(chatID int64, msg string) error {
	_, err := c.send(chatID, msg, &gotgbot.SendMessageOpts{
		ParseMode:          parseHTML,
		LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true},
	})
	return err
}

func (c *Ctx) send(chatID int64, msg string, opts *gotgbot.SendMessageOpts) (*gotgbot.Message, error) {
	for attempt := 0; ; attempt++ {
		m, err := c.Bot.SendMessageWithContext(c.Ctx, chatID, msg, opts)
		if err == nil || attempt >= 2 {
			return m, err
		}
		p := problem(err)
		switch {
		case strings.Contains(p, "can't parse entities") && opts.ParseMode != "":
			slog.Warn("Telegram rejected the markup, resending as plain text")
			msg, opts.ParseMode = text.StripTags(msg), ""
		case strings.Contains(p, "repl") && strings.Contains(p, "not found") && opts.ReplyParameters != nil:
			opts.ReplyParameters = nil
		default:
			return m, err
		}
	}
}

// Edit edits a message the bot sent. "Not modified" is not an error. A nil markup removes the buttons.
func (c *Ctx) Edit(msg string, markup *gotgbot.InlineKeyboardMarkup) error {
	opts := &gotgbot.EditMessageTextOpts{ChatId: c.Msg.Chat.Id, MessageId: c.Msg.MessageId, Text: msg, ParseMode: parseHTML}
	if markup != nil {
		opts.ReplyMarkup = *markup
	}
	_, _, err := c.Bot.EditMessageTextWithContext(c.Ctx, opts)
	if err == nil {
		return nil
	}
	p := problem(err)
	switch {
	case strings.Contains(p, "message is not modified"):
		return nil
	case strings.Contains(p, "can't parse entities"):
		opts.ParseMode, opts.Text = "", text.StripTags(msg)
		_, _, err = c.Bot.EditMessageTextWithContext(c.Ctx, opts)
		return err
	}
	return err
}

// RemoveButtons takes the buttons off the message a button is under. Errors are ignored: it may be gone.
func (c *Ctx) RemoveButtons() {
	_, _, _ = c.Bot.EditMessageReplyMarkupWithContext(c.Ctx, &gotgbot.EditMessageReplyMarkupOpts{
		ChatId: c.Msg.Chat.Id, MessageId: c.Msg.MessageId,
	})
}

// Answer answers a button press (a toast, or an alert when alert is true).
func (c *Ctx) Answer(msg string, alert bool) {
	opts := &gotgbot.AnswerCallbackQueryOpts{}
	if msg != "" {
		opts.Text, opts.ShowAlert = msg, alert
	}
	if _, err := c.Bot.AnswerCallbackQueryWithContext(c.Ctx, c.Cb.Id, opts); err != nil {
		slog.Debug("Could not answer a button", "err", err)
	}
}
