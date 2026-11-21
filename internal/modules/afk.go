package modules

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/core"
	"argo/internal/text"
)

// Tell people you are away. Anyone who mentions or replies to you while you are AFK gets a notice with
// your reason and the time you left. A photo, GIF or video sent with /afk (or replied to) is shown with the notice.

const afkNoticeCooldown = time.Minute

func registerAfk(r *core.Router) {
	// Before the other modules, so that a person who is back is noticed even when their message is a command another
	// module answers.
	r.Watch(-50, "afk", watchAfk)
	r.Command(cmdAfk, "afk")
}

func afkText(m *gotgbot.Message) string {
	if m.Text != "" {
		return strings.TrimSpace(m.Text)
	}
	return strings.TrimSpace(m.Caption)
}

// brbReason: the reason when the text is "brb [reason]".
func brbReason(s string) (string, bool) {
	if len(s) < 3 || !strings.EqualFold(s[:3], "brb") {
		return "", false
	}
	rest := s[3:]
	if rest == "" {
		return "", true
	}
	if r := []rune(rest)[0]; r != ' ' && r != '\t' && r != '\n' && r != '\r' && r != '\f' && r != '\v' && r != 0xa0 && r != 0x85 {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

func wantsAfk(c *core.Ctx, s string) bool {
	if _, ok := brbReason(s); ok {
		return true
	}
	r := []rune(s)
	if len(r) < 4 || !strings.ContainsRune(c.App.Cfg.CommandPrefix, r[0]) || !strings.EqualFold(string(r[1:4]), "afk") {
		return false
	}
	rest := r[4:]
	if len(rest) == 0 || rest[0] == ' ' || rest[0] == '\n' || rest[0] == '\t' || rest[0] == '\r' {
		return true
	}
	return rest[0] == '@' // /afk@botname
}

// afkSender is the person who wrote the message, or nil for channels, anonymous admins and bots.
func afkSender(m *gotgbot.Message) *gotgbot.User {
	u := m.From
	if u == nil || u.IsBot || m.SenderChat != nil || core.IsImmune(u.Id) {
		return nil
	}
	return u
}

func afkMedia(m *gotgbot.Message) (string, string) {
	switch {
	case m == nil:
	case m.Animation != nil: // a GIF also counts as a document
		return "animation", m.Animation.FileId
	case m.Video != nil:
		return "video", m.Video.FileId
	case len(m.Photo) > 0:
		return "photo", m.Photo[len(m.Photo)-1].FileId // the biggest size comes last
	}
	return "", ""
}

func afkAgo(c *core.Ctx, since time.Time) string {
	if t := core.FormatDuration(time.Since(since)); t != "" {
		return t
	}
	return c.T("afk.less_than_a_minute")
}

func goAfk(c *core.Ctx, reason string) error {
	m := c.Msg
	u := afkSender(m)
	if u == nil {
		if m.Chat.Type != "private" {
			c.Say(c.T("afk.anonymous_senders_and_channels_can_t"))
		}
		return nil
	}
	kind, file := afkMedia(m)
	if kind == "" {
		kind, file = afkMedia(core.RepliedMessage(m))
	}
	reason = text.Truncate(strings.Join(strings.Fields(reason), " "), core.MaxAfkReason)
	if _, err := c.App.AfkSet(c.Ctx, u.Id, core.FullName(u), u.Username, reason, kind, file); err != nil {
		return err
	}
	msg := c.T("afk.is_now_afk", core.V{"id": text.UserLink(u.Id, core.FullName(u))})
	if reason != "" {
		msg += c.T("common.reason", core.V{"reason": text.Esc(reason)})
	}
	_, err := c.ReplyOpts(msg, nil, &gotgbot.LinkPreviewOptions{IsDisabled: true})
	return err
}

func cmdAfk(c *core.Ctx) error { return goAfk(c, c.Args) }

func sliceUTF16(s string, offset, length int64) string {
	u := utf16.Encode([]rune(s))
	if offset < 0 || offset > int64(len(u)) {
		return ""
	}
	end := min(offset+length, int64(len(u)))
	return string(utf16.Decode(u[offset:end]))
}

// afkUsersIn: people who are away and were mentioned in, or replied to by, the message.
func afkUsersIn(c *core.Ctx, m *gotgbot.Message) []*core.AfkRecord {
	var found []*core.AfkRecord
	add := func(r *core.AfkRecord) {
		if r == nil {
			return
		}
		for _, f := range found {
			if f.UserID == r.UserID {
				return
			}
		}
		found = append(found, r)
	}
	if rep := core.RepliedMessage(m); rep != nil && !rep.IsAutomaticForward && rep.From != nil && rep.SenderChat == nil {
		add(c.App.AfkGet(c.Ctx, rep.From.Id))
	}
	for _, part := range []struct {
		body     string
		entities []gotgbot.MessageEntity
	}{{m.Text, m.Entities}, {m.Caption, m.CaptionEntities}} {
		for _, e := range part.entities {
			switch {
			case e.Type == "text_mention" && e.User != nil:
				add(c.App.AfkGet(c.Ctx, e.User.Id))
			case e.Type == "mention" && part.body != "":
				add(c.App.AfkByUsername(c.Ctx, sliceUTF16(part.body, e.Offset, e.Length)))
			}
		}
	}
	return found
}

func afkNotice(c *core.Ctx, r *core.AfkRecord) string {
	msg := c.T("afk.is_afk", core.V{"user_id": text.UserLink(r.UserID, r.Name)})
	if r.Reason != "" {
		msg += c.T("common.reason", core.V{"reason": text.Esc(r.Reason)})
	}
	return msg + c.T("afk.away_since_ago", core.V{"since_text": r.Since.UTC().Format("02 Jan 2006, 15:04 UTC"), "ago": afkAgo(c, r.Since)})
}

// afkSend answers in the same chat and topic, with the person's photo, GIF or video when they added one.
func afkSend(c *core.Ctx, msg string, r *core.AfkRecord) {
	if r.MediaType != "" {
		if _, err := c.SendMediaReply(r.MediaType, r.MediaFile, msg); err == nil {
			return
		} // the file is gone or the caption was rejected: send the text alone
	}
	_, _ = c.ReplyOpts(msg, nil, &gotgbot.LinkPreviewOptions{IsDisabled: true})
}

// watchAfk looks at every message and takes none of them (except "brb", which is answered here).
func watchAfk(c *core.Ctx) (bool, error) {
	m := c.Msg
	if m == nil || c.Cb != nil || c.Edited {
		return false, nil
	}
	body := afkText(m)
	if reason, ok := brbReason(body); ok {
		return true, goAfk(c, reason)
	}
	if wantsAfk(c, body) {
		return false, nil // the command goes on
	}
	u := afkSender(m)
	if u == nil {
		return false, nil
	}
	if len(m.NewChatMembers) == 0 && m.LeftChatMember == nil {
		back, err := c.App.AfkClear(c.Ctx, u.Id)
		if err != nil {
			return false, err
		}
		if back != nil {
			_, _ = c.ReplyOpts(c.T("afk.is_no_longer_afk_you_were", core.V{"id": text.UserLink(u.Id, core.FullName(u)), "ago": afkAgo(c, back.Since)}), nil, &gotgbot.LinkPreviewOptions{IsDisabled: true})
		}
	}
	if isGroup(m.Chat.Type) && c.App.AfkAnyone(c.Ctx) {
		for _, r := range afkUsersIn(c, m) {
			if r.UserID == u.Id || !c.App.Cooldown(fmt.Sprintf("afk:%d/%d", m.Chat.Id, r.UserID), afkNoticeCooldown) {
				continue
			}
			afkSend(c, afkNotice(c, r), r)
		}
	}
	return false, nil
}
