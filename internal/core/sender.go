package core

import (
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/markup"
	"argo/internal/text"
)

// Saved messages: reading one from a command and sending it later.

const (
	CaptionLimit = 1024
	MessageLimit = 4096
	MaxContent   = 4000
)

// Media is the media a message carries: its kind and Telegram file id.
type Media struct{ Kind, FileID string }

// MediaOf is the media of a message, if any.
func MediaOf(m *gotgbot.Message) *Media {
	if m == nil {
		return nil
	}
	switch {
	case m.Animation != nil: // a GIF is also a document
		return &Media{"animation", m.Animation.FileId}
	case m.Video != nil:
		return &Media{"video", m.Video.FileId}
	case m.Audio != nil:
		return &Media{"audio", m.Audio.FileId}
	case m.Voice != nil:
		return &Media{"voice", m.Voice.FileId}
	case m.VideoNote != nil:
		return &Media{"video_note", m.VideoNote.FileId}
	case m.Sticker != nil:
		return &Media{"sticker", m.Sticker.FileId}
	case m.Document != nil:
		return &Media{"document", m.Document.FileId}
	case len(m.Photo) > 0:
		return &Media{"photo", m.Photo[len(m.Photo)-1].FileId} // the biggest size comes last
	}
	return nil
}

func entitiesOf(es []gotgbot.MessageEntity) []markup.Entity {
	out := make([]markup.Entity, 0, len(es))
	for _, e := range es {
		out = append(out, markup.Entity{Type: e.Type, Offset: int(e.Offset), Length: int(e.Length), URL: e.Url, Language: e.Language})
	}
	return out
}

// TextOf is the text (or caption) of a message, written in the HTML of package markup.
func TextOf(m *gotgbot.Message) string {
	if m.Text != "" {
		return markup.FromEntities(m.Text, entitiesOf(m.Entities), true, 0)
	}
	return markup.FromEntities(m.Caption, entitiesOf(m.CaptionEntities), true, 0)
}

var (
	formatting = map[string]bool{"bold": true, "italic": true, "underline": true, "strikethrough": true, "spoiler": true,
		"code": true, "pre": true, "text_link": true, "blockquote": true, "expandable_blockquote": true}
	firstWordRE = regexp.MustCompile(`^\S+\s*`)
)

// ArgsOf is what was typed after the command, in the HTML of package markup. Text formatted with the app's menu
// becomes tags; tags typed by hand are left as they are.
func ArgsOf(m *gotgbot.Message, args string) string {
	args = strings.TrimSpace(args)
	src, ents := m.Text, m.Entities
	if src == "" {
		src, ents = m.Caption, m.CaptionEntities
	}
	has := false
	for _, e := range ents {
		if formatting[e.Type] {
			has = true
		}
	}
	if args == "" || !has {
		return args
	}
	loc := firstWordRE.FindString(src)
	if loc == "" {
		return args
	}
	skip := len(utf16.Encode([]rune(loc)))
	rest := src[len(loc):]
	if out := strings.TrimSpace(markup.FromEntities(rest, entitiesOf(ents), false, skip)); out != "" {
		return out
	}
	return args
}

// Validate returns a *markup.Error with a sentence for people when this could not be sent.
func (c *Ctx) Validate(content string, hasMedia bool) error {
	if utf8.RuneCountInString(content) > MaxContent {
		return &markup.Error{Msg: c.T("sender.that_message_is_too_long_at", V{"MAX_CONTENT": MaxContent})}
	}
	if err := markup.Check(c.L, content); err != nil {
		return err
	}
	for _, variant := range markup.Variants(content) {
		parsed, err := markup.Parse(c.L, variant, nil)
		if err != nil {
			return err
		}
		if utf16Len(parsed.HTML) > MessageLimit {
			return &markup.Error{Msg: c.T("sender.a_variant_is_too_long_once")}
		}
		if parsed.HTML == "" && !hasMedia {
			return &markup.Error{Msg: c.T("sender.a_message_needs_some_text_buttons")}
		}
	}
	return nil
}

func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

// Fillings are the facts about user for the {first}, {mention}... of a saved message (and about chat).
func (c *Ctx) Fillings(user *gotgbot.User, source string, chat *gotgbot.Chat) map[string]string {
	if chat == nil {
		chat = &c.Msg.Chat
	}
	v := map[string]string{
		"first": user.FirstName, "last": user.LastName, "fullname": FullName(user), "username": "",
		"mention": FullName(user), "id": strconv.FormatInt(user.Id, 10), "chatname": chat.Title, "count": "",
	}
	if user.Username != "" {
		v["username"] = "@" + user.Username
	}
	if strings.Contains(source, "{count}") {
		if n, err := c.Bot.GetChatMemberCountWithContext(c.Ctx, chat.Id, nil); err == nil {
			v["count"] = strconv.FormatInt(n, 10)
		}
	}
	return v
}

func keyboardOf(p *markup.Parsed) *gotgbot.InlineKeyboardMarkup {
	if len(p.Buttons) == 0 {
		return nil
	}
	var rows [][]gotgbot.InlineKeyboardButton
	for _, r := range p.Buttons {
		var row []gotgbot.InlineKeyboardButton
		for _, b := range r {
			row = append(row, gotgbot.InlineKeyboardButton{Text: b.Text, Url: b.URL})
		}
		rows = append(rows, row)
	}
	return &gotgbot.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// SendOpts changes how a saved message is sent.
type SendOpts struct {
	Raw         bool                          // the text exactly as saved, without formatting, buttons or options
	Label       string                        // for the log
	Chat        *gotgbot.Chat                 // the chat the message belongs to when sent elsewhere
	ExtraButton *gotgbot.InlineKeyboardButton // one more button on a row of its own (the captcha button)
	Alone       bool                          // a message of its own, not an answer to the current message (/permapin)
}

// SendSaved sends a saved message in answer to the current message, filled in for user. It returns the message sent
// (the media, or the text when it follows on its own), or nil when nothing could be sent.
func (c *Ctx) SendSaved(user *gotgbot.User, content, mediaType, mediaFileID string, o SendOpts) *gotgbot.Message {
	if o.Label == "" {
		o.Label = c.T("sender.saved_message")
	}
	if o.Alone {
		// A made-up message (id 0) has nothing to answer to.
		made, original := *c.Msg, c.Msg
		made.MessageId = 0
		c.Msg = &made
		defer func() { c.Msg = original }()
	}
	var parsed *markup.Parsed
	if o.Raw {
		parsed = &markup.Parsed{HTML: text.Esc(content), Options: map[string]bool{}}
	} else {
		source := markup.Pick(content)
		var err error
		parsed, err = markup.Parse(c.L, source, c.Fillings(user, source, o.Chat))
		if err != nil {
			slog.Warn("A saved message can no longer be sent", "what", o.Label, "chat", c.Msg.Chat.Id)
			return nil
		}
	}
	keyboard := keyboardOf(parsed)
	if o.ExtraButton != nil {
		var rows [][]gotgbot.InlineKeyboardButton
		if keyboard != nil {
			rows = keyboard.InlineKeyboard
		}
		keyboard = &gotgbot.InlineKeyboardMarkup{InlineKeyboard: append(rows, []gotgbot.InlineKeyboardButton{*o.ExtraButton})}
	}
	protect, quiet := parsed.Options["protect"], parsed.Options["nonotif"]

	var sent *gotgbot.Message
	textFollows := true
	if mediaType != "" {
		inline := parsed.HTML != "" && utf16Len(parsed.HTML) <= CaptionLimit && mediaType != "sticker" && mediaType != "video_note"
		textFollows = parsed.HTML != "" && !inline
		var markupForMedia *gotgbot.InlineKeyboardMarkup
		if keyboard != nil && !textFollows {
			markupForMedia = keyboard // nothing else carries the buttons
		}
		caption := ""
		if inline {
			caption = parsed.HTML
		}
		spoiler := parsed.Options["mediaspoiler"] && (mediaType == "photo" || mediaType == "video" || mediaType == "animation")
		m, err := c.sendMedia(mediaType, mediaFileID, caption, markupForMedia, protect, quiet, spoiler)
		if err != nil {
			slog.Warn("A saved message's media can't be sent", "what", o.Label, "chat", c.Msg.Chat.Id, "err", err)
			if parsed.HTML == "" {
				return nil
			}
			textFollows = true // fall back to the text alone
		} else {
			sent = m
		}
		if !textFollows {
			return sent
		}
	}
	opts := &gotgbot.SendMessageOpts{
		ParseMode:           parseHTML,
		DisableNotification: quiet,
		ProtectContent:      protect,
		LinkPreviewOptions:  &gotgbot.LinkPreviewOptions{IsDisabled: !parsed.Options["preview"]},
	}
	if keyboard != nil {
		opts.ReplyMarkup = *keyboard
	}
	var chat int64
	chat, opts.MessageThreadId, opts.ReplyParameters = c.dest(true)
	m, err := c.send(chat, parsed.HTML, opts)
	if err != nil {
		return sent
	}
	if sent != nil {
		return sent
	}
	return m
}

func (c *Ctx) threadAndReply(thread *int64, reply **gotgbot.ReplyParameters) {
	if c.Msg.IsTopicMessage {
		*thread = c.Msg.MessageThreadId
	}
	if c.Msg.MessageId != 0 { // a made-up message (id 0) has nothing to answer to
		*reply = &gotgbot.ReplyParameters{MessageId: c.Msg.MessageId, AllowSendingWithoutReply: true}
	}
}

func (c *Ctx) sendMedia(kind, fileID, caption string, kb *gotgbot.InlineKeyboardMarkup, protect, quiet, spoiler bool) (*gotgbot.Message, error) {
	var thread int64
	var reply *gotgbot.ReplyParameters
	c.threadAndReply(&thread, &reply)
	var markupV gotgbot.ReplyMarkup
	if kb != nil {
		markupV = *kb
	}
	f := gotgbot.InputFileByID(fileID)
	chat := c.Msg.Chat.Id
	parse := ""
	if caption != "" {
		parse = parseHTML
	}
	switch kind {
	case "photo":
		return c.Bot.SendPhotoWithContext(c.Ctx, chat, f, &gotgbot.SendPhotoOpts{MessageThreadId: thread, Caption: caption, ParseMode: parse, HasSpoiler: spoiler, DisableNotification: quiet, ProtectContent: protect, ReplyParameters: reply, ReplyMarkup: markupV})
	case "video":
		return c.Bot.SendVideoWithContext(c.Ctx, chat, f, &gotgbot.SendVideoOpts{MessageThreadId: thread, Caption: caption, ParseMode: parse, HasSpoiler: spoiler, DisableNotification: quiet, ProtectContent: protect, ReplyParameters: reply, ReplyMarkup: markupV})
	case "animation":
		return c.Bot.SendAnimationWithContext(c.Ctx, chat, f, &gotgbot.SendAnimationOpts{MessageThreadId: thread, Caption: caption, ParseMode: parse, HasSpoiler: spoiler, DisableNotification: quiet, ProtectContent: protect, ReplyParameters: reply, ReplyMarkup: markupV})
	case "document":
		return c.Bot.SendDocumentWithContext(c.Ctx, chat, f, &gotgbot.SendDocumentOpts{MessageThreadId: thread, Caption: caption, ParseMode: parse, DisableNotification: quiet, ProtectContent: protect, ReplyParameters: reply, ReplyMarkup: markupV})
	case "audio":
		return c.Bot.SendAudioWithContext(c.Ctx, chat, f, &gotgbot.SendAudioOpts{MessageThreadId: thread, Caption: caption, ParseMode: parse, DisableNotification: quiet, ProtectContent: protect, ReplyParameters: reply, ReplyMarkup: markupV})
	case "voice":
		return c.Bot.SendVoiceWithContext(c.Ctx, chat, f, &gotgbot.SendVoiceOpts{MessageThreadId: thread, Caption: caption, ParseMode: parse, DisableNotification: quiet, ProtectContent: protect, ReplyParameters: reply, ReplyMarkup: markupV})
	case "sticker":
		return c.Bot.SendStickerWithContext(c.Ctx, chat, f, &gotgbot.SendStickerOpts{MessageThreadId: thread, DisableNotification: quiet, ProtectContent: protect, ReplyParameters: reply, ReplyMarkup: markupV})
	default: // video_note
		return c.Bot.SendVideoNoteWithContext(c.Ctx, chat, f, &gotgbot.SendVideoNoteOpts{MessageThreadId: thread, DisableNotification: quiet, ProtectContent: protect, ReplyParameters: reply, ReplyMarkup: markupV})
	}
}

// SendMediaReply answers the current message with a photo, GIF or video and a caption.
func (c *Ctx) SendMediaReply(kind, fileID, caption string) (*gotgbot.Message, error) {
	return c.sendMedia(kind, fileID, caption, nil, false, false, false)
}
