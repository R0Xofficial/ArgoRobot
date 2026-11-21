package core

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

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

// dest is where an answer to the current message goes: the chat, the topic, and what it is attached to. A command run
// for a connected group is answered in the private chat it came from.
func (c *Ctx) dest(attach bool) (chat, thread int64, reply *gotgbot.ReplyParameters) {
	m := c.Msg
	if c.Origin != 0 {
		return c.Origin, 0, nil
	}
	if m.IsTopicMessage {
		thread = m.MessageThreadId
	}
	if attach && m.Chat.Type != "private" && m.MessageId != 0 {
		reply = &gotgbot.ReplyParameters{MessageId: m.MessageId, AllowSendingWithoutReply: true}
	}
	return m.Chat.Id, thread, reply
}

// Reply sends text where the message was sent: same chat, same topic. In groups the answer is attached to the
// message (even if it is deleted meanwhile); private chats stay clean. Broken markup is resent as plain text.
func (c *Ctx) Reply(msg string, markup *gotgbot.InlineKeyboardMarkup) (*gotgbot.Message, error) {
	return c.ReplyOpts(msg, markup, nil)
}

// ReplyOpts is Reply with link preview options.
func (c *Ctx) ReplyOpts(msg string, markup *gotgbot.InlineKeyboardMarkup, preview *gotgbot.LinkPreviewOptions) (*gotgbot.Message, error) {
	opts := &gotgbot.SendMessageOpts{ParseMode: parseHTML, LinkPreviewOptions: preview}
	if markup != nil {
		opts.ReplyMarkup = *markup
	}
	chat, thread, reply := c.dest(true)
	opts.MessageThreadId, opts.ReplyParameters = thread, reply
	return c.send(chat, msg, opts)
}

// SendPlain sends text to the chat and topic of the message without attaching it to the message.
func (c *Ctx) SendPlain(msg string) (*gotgbot.Message, error) {
	opts := &gotgbot.SendMessageOpts{ParseMode: parseHTML}
	chat, thread, _ := c.dest(false)
	opts.MessageThreadId = thread
	return c.send(chat, msg, opts)
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

// EditSent edits a message the bot sent earlier (a message that is not the one being handled).
func (c *Ctx) EditSent(m *gotgbot.Message, msg string, markup *gotgbot.InlineKeyboardMarkup) error {
	opts := &gotgbot.EditMessageTextOpts{ChatId: m.Chat.Id, MessageId: m.MessageId, Text: msg, ParseMode: parseHTML}
	if markup != nil {
		opts.ReplyMarkup = *markup
	}
	_, _, err := c.Bot.EditMessageTextWithContext(c.Ctx, opts)
	if err != nil && strings.Contains(problem(err), "message is not modified") {
		return nil
	}
	return err
}

// Download fetches a file Telegram holds (a document somebody sent). At most MaxImportBytes are read.
func (c *Ctx) Download(fileID string) ([]byte, error) {
	f, err := c.Bot.GetFileWithContext(c.Ctx, fileID, nil)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(c.Ctx, http.MethodGet, f.URL(c.Bot, nil), nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download failed: %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, MaxImportBytes+1))
}

// SendDocument sends a file made here to the chat (and topic) of the message.
func (c *Ctx) SendDocument(name string, data []byte, caption string) error {
	chat, thread, _ := c.dest(false)
	_, err := c.Bot.SendDocumentWithContext(c.Ctx, chat, gotgbot.InputFileByReader(name, bytes.NewReader(data)),
		&gotgbot.SendDocumentOpts{Caption: caption, ParseMode: parseHTML, MessageThreadId: thread})
	return err
}

// ReplyTo answers another message than the one being handled (in its topic), with no link preview.
func (c *Ctx) ReplyTo(target *gotgbot.Message, msg string) (*gotgbot.Message, error) {
	opts := &gotgbot.SendMessageOpts{
		ParseMode:          parseHTML,
		LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true},
		ReplyParameters:    &gotgbot.ReplyParameters{MessageId: target.MessageId, AllowSendingWithoutReply: true},
	}
	if target.IsTopicMessage {
		opts.MessageThreadId = target.MessageThreadId
	}
	return c.send(target.Chat.Id, msg, opts)
}

// SendDocumentTo sends a file made here to another chat (a person's private chat).
func (c *Ctx) SendDocumentTo(chatID int64, name string, data []byte, caption string) error {
	_, err := c.Bot.SendDocumentWithContext(c.Ctx, chatID, gotgbot.InputFileByReader(name, bytes.NewReader(data)),
		&gotgbot.SendDocumentOpts{Caption: caption, ParseMode: parseHTML})
	return err
}
