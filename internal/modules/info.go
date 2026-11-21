package modules

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/core"
	"argo/internal/text"
)

// /id and /info (modules/misc.py). Both take an optional target: reply to a message, or give a @username or id. A
// target can be a user, a bot, a group or a channel. Without one they describe whoever sent the command (a premium
// user writing as a channel is described as that channel).

var kindLabels = map[string]string{
	"user": "common.user", "bot": "misc.kind_labels.bot", "group": "misc.kind_labels.group",
	"supergroup": "misc.kind_labels.supergroup", "channel": "common.channel",
}

func line(label, value string) string { return "<b>" + label + ":</b> " + value }
func item(label, value string) string { return "• " + line(label, value) }

// subject is who the command is about, and whether they were named explicitly.
func subject(c *core.Ctx) (*core.Target, bool, error) {
	if strings.TrimSpace(c.Args) != "" || core.RepliedMessage(c.Msg) != nil {
		t, err := c.ResolveTarget(true, true)
		return t, true, err
	}
	return core.SubjectOfSender(c.Msg), false, nil
}

func cmdID(c *core.Ctx) error {
	target, explicit, err := subject(c)
	if err != nil {
		var te *core.TargetError
		if asTargetError(err, &te) {
			c.Say(text.Esc(te.Msg))
			return nil
		}
		return err
	}
	if target == nil {
		return nil
	}
	kind := c.T(kindLabels[target.Kind])
	label := c.T("misc.id", core.V{"kind": kind})
	if !explicit && !target.IsChat() {
		label = c.T("misc.your_id")
	}
	lines := []string{line(label, fmt.Sprintf("<code>%d</code>", target.ID))}
	// Handy for filling in log chat settings.
	if !explicit && c.Msg.Chat.Type != "private" {
		lines = append(lines, line(c.T("common.chat_id"), fmt.Sprintf("<code>%d</code>", c.Msg.Chat.Id)))
		if c.Msg.IsTopicMessage && c.Msg.MessageThreadId != 0 {
			lines = append(lines, line(c.T("misc.topic_id"), fmt.Sprintf("<code>%d</code>", c.Msg.MessageThreadId)))
		}
	}
	c.Say(strings.Join(lines, "\n"))
	return nil
}

// standing is the "Status" of a person in a chat: Owner, Admin, Banned, Muted, Member (Approved) ... Empty when
// Telegram won't say.
func standing(c *core.Ctx, m *gotgbot.MergedChatMember, approved, captcha bool) string {
	if m == nil {
		return ""
	}
	label, muted := "", false
	switch m.Status {
	case "creator":
		label = c.T("common.owner")
	case "administrator":
		label = c.T("common.admin")
	case "member":
		label = c.T("misc.member")
	case "restricted":
		if m.IsMember {
			label = c.T("misc.member")
			muted = !m.CanSendMessages
		} else {
			label = c.T("misc.not_in_chat")
		}
	case "kicked":
		return c.T("misc.banned")
	default:
		label = c.T("misc.not_in_chat") // left, or never seen
	}
	if muted {
		label = c.T("misc.muted")
	}
	isMember := m.Status == "member" || (m.Status == "restricted" && m.IsMember)
	switch {
	case approved && isMember:
		label += c.T("misc.approved")
	case captcha && muted:
		label = c.T("misc.muted_no_captcha") // muted until the captcha is solved
	}
	return label
}

func isAdminStatus(m *gotgbot.MergedChatMember) bool {
	return m != nil && (m.Status == "creator" || m.Status == "administrator")
}

func flagLines(c *core.Ctx, flags []core.InfoFlag) []string {
	order := []string{c.T("gbans.globally_banned"), c.T("root.whitelisted_2"), c.T("root.immune")}
	index := func(label string) int {
		for i, o := range order {
			if o == label {
				return i
			}
		}
		return len(order)
	}
	sort.SliceStable(flags, func(i, j int) bool { return index(flags[i].Label) < index(flags[j].Label) })
	var out []string
	for _, f := range flags {
		v := c.T("common.no")
		if f.Value {
			v = c.T("common.yes")
		}
		out = append(out, item(f.Label, "<code>"+v+"</code>"))
	}
	return out
}

// refreshUser fills in the name parts for people stored before names were kept in two parts.
func refreshUser(c *core.Ctx, t *core.Target) {
	if t.FirstName != "" {
		return
	}
	chat, err := c.Bot.GetChatWithContext(c.Ctx, t.ID, nil)
	if err != nil { // not reachable: all we have is the stored full name
		t.FirstName = t.Name
		return
	}
	t.FirstName = chat.FirstName
	if t.FirstName == "" {
		t.FirstName = strings.TrimSpace(chat.FirstName + " " + chat.LastName)
	}
	t.LastName = chat.LastName
	if chat.Username != "" {
		t.Username = chat.Username
	}
	_ = c.App.Remember(c.Ctx, &gotgbot.User{Id: chat.Id, FirstName: chat.FirstName, LastName: chat.LastName, Username: chat.Username})
}

func userInfo(c *core.Ctx, t *core.Target) []string {
	refreshUser(c, t)
	lines := []string{c.T("misc.user_info"), item(c.T("common.id"), fmt.Sprintf("<code>%d</code>", t.ID))}
	if t.FirstName != "" {
		lines = append(lines, item(c.T("misc.first_name"), text.Esc(t.FirstName)))
	}
	if t.LastName != "" {
		lines = append(lines, item(c.T("misc.last_name"), text.Esc(t.LastName)))
	}
	if t.Username != "" {
		lines = append(lines, item(c.T("misc.username"), "@"+text.Esc(t.Username)))
	}
	lines = append(lines, item(c.T("misc.user_link"), fmt.Sprintf(`<a href="tg://user?id=%d">link</a>`, t.ID)))

	chat := c.Msg.Chat
	inGroup := isGroup(chat.Type)
	var chatID int64
	var member *gotgbot.MergedChatMember
	if inGroup {
		chatID = chat.Id
		member = c.GetMember(chat.Id, t.ID)
	}
	role := c.App.GetRole(c.Ctx, t.ID)

	// A role settles it: admins and the bot's staff have no flags to list.
	hasRole := role != core.NoRole || isAdminStatus(member)
	var flags []core.InfoFlag
	approved, captcha := false, false
	approvedLabel, captchaLabel := c.T("approves.approved"), c.T("captcha.captcha_pending")
	if !hasRole {
		found := c.InfoFlags(t.ID, chatID, false)
		for _, f := range found {
			approved = approved || (f.Label == approvedLabel && f.Value)
			captcha = captcha || (f.Label == captchaLabel && f.Value)
			if f.Label != approvedLabel && f.Label != captchaLabel {
				flags = append(flags, f)
			}
		}
	}
	// Facts that hold for admins and staff as well.
	flags = append(flags, c.InfoFlags(t.ID, chatID, true)...)

	if s := standing(c, member, approved, captcha); s != "" {
		lines = append(lines, item(c.T("misc.status"), "<code>"+s+"</code>"))
	}
	var about []string
	if role != core.NoRole {
		about = append(about, item(c.T("misc.bot_role"), "<code>"+core.RoleLabel(role, false)+"</code>"))
	}
	about = append(about, flagLines(c, flags)...)
	if len(about) > 0 {
		lines = append(lines, "", c.T("misc.argo_info"))
		lines = append(lines, about...)
	}
	// Sentences the modules write themselves (a federation ban with its reason ...) close the message.
	for _, note := range c.InfoNotes(t.ID, chatID) {
		lines = append(lines, "", note)
	}
	return lines
}

func chatInfo(c *core.Ctx, t *core.Target) []string {
	lines := []string{c.T("misc.chat_info"), item(c.T("common.id"), fmt.Sprintf("<code>%d</code>", t.ID))}
	if t.Title != "" {
		lines = append(lines, item(c.T("common.title"), text.Esc(t.Title)))
	}
	lines = append(lines, item(c.T("misc.type"), "<code>"+c.T(kindLabels[t.Kind])+"</code>"))
	if t.Username != "" {
		lines = append(lines, item(c.T("misc.username"), "@"+text.Esc(t.Username)),
			item(c.T("misc.link"), fmt.Sprintf(`<a href="https://t.me/%s">link</a>`, text.Esc(t.Username))))
	}
	if n, err := c.Bot.GetChatMemberCountWithContext(c.Ctx, t.ID, nil); err == nil {
		lines = append(lines, item(c.T("misc.members"), fmt.Sprintf("<code>%d</code>", n)))
	}
	return lines
}

func cmdInfo(c *core.Ctx) error {
	target, _, err := subject(c)
	if err != nil {
		var te *core.TargetError
		if asTargetError(err, &te) {
			if te.Unknown {
				c.Say(c.T("misc.unknown_user"))
			} else {
				c.Say(text.Esc(te.Msg))
			}
			return nil
		}
		return err
	}
	if target == nil || !target.IsKnown() {
		c.Say(c.T("misc.unknown_user"))
		return nil
	}
	var lines []string
	if target.IsChat() {
		lines = chatInfo(c, target)
	} else {
		lines = userInfo(c, target)
	}
	_, err = c.ReplyOpts(strings.Join(lines, "\n"), nil, &gotgbot.LinkPreviewOptions{IsDisabled: true})
	return err
}

func asTargetError(err error, out **core.TargetError) bool { return errors.As(err, out) }
