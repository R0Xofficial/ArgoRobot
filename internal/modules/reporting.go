package modules

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/core"
	"argo/internal/text"
)

// Report messages to the admins.

const (
	reportCooldown  = 20 * time.Second
	maxReportReason = 200
	maxExcerpt      = 200
	maxMentioned    = 20 // admins pinged in the chat
	maxAlerted      = 30 // admins messaged in private
)

func registerReporting(r *core.Router) {
	r.Command(cmdReport, "report")
	r.Command(cmdReports, "reports")
	r.Watch(0, "report @admin", watchAdminCall)
}

// messageLink opens the message, when Telegram has a link for this kind of chat.
func messageLink(chat gotgbot.Chat, id int64) string {
	if chat.Username != "" {
		return fmt.Sprintf("https://t.me/%s/%d", chat.Username, id)
	}
	if s := strconv.FormatInt(chat.Id, 10); strings.HasPrefix(s, "-100") {
		return fmt.Sprintf("https://t.me/c/%s/%d", s[4:], id)
	}
	return ""
}

// withID shows a person or chat the way the reports do: the link or name, then the id.
func withID(who string, id int64) string {
	return fmt.Sprintf("%s [<code>%d</code>]", who, id)
}

func senderLink(c *core.Ctx, m *gotgbot.Message) string {
	if m.SenderChat != nil {
		t := m.SenderChat.Title
		if t == "" {
			t = c.T("reporting.a_channel")
		}
		return withID("<b>"+text.Esc(t)+"</b>", m.SenderChat.Id)
	}
	if m.From != nil {
		return withID(text.UserLink(m.From.Id, core.FullName(m.From)), m.From.Id)
	}
	return c.T("reporting.someone")
}

// writesAsAdmin: an admin, an anonymous admin, or no one we can look at (then there is nothing to report).
func writesAsAdmin(c *core.Ctx, m *gotgbot.Message) bool {
	if m.SenderChat != nil && m.SenderChat.Id == m.Chat.Id {
		return true // an admin writing as the group
	}
	if m.From == nil || m.From.IsBot {
		return true
	}
	return c.IsChatAdmin(m.Chat.Id, m.From.Id)
}

func squash(s string, limit int) string {
	return text.Truncate(strings.Join(strings.Fields(s), " "), limit)
}

// fileReport reports target to the admins. m is what the reporter wrote; explicit is /report (which answers when
// something is wrong) as opposed to an @admin in passing. Without a message to report, /report can name the user
// instead (named): `/report <user> [reason]`.
func fileReport(c *core.Ctx, m, target *gotgbot.Message, reason string, explicit, named bool) error {
	chat := m.Chat
	if !isGroup(chat.Type) {
		if explicit {
			c.Say(c.T("common.this_command_only_works_in_groups"))
		}
		return nil
	}
	if writesAsAdmin(c, m) {
		return nil
	}
	if !c.App.Setting(c.Ctx, "reports", chat.Id) {
		if explicit {
			c.Say(c.T("reporting.reporting_is_turned_off_in_this"))
		}
		return nil
	}
	var user *core.Target
	if target == nil {
		if !named || strings.TrimSpace(c.Args) == "" {
			c.Say(c.T("reporting.reply_to_the_message_you_want"))
			return nil
		}
		t, err := c.ResolveTarget(true, false)
		if err != nil {
			var te *core.TargetError
			if errors.As(err, &te) {
				c.Say(te.Msg)
				return nil
			}
			return err
		}
		user, reason = t, t.Extra
	}
	reporter := m.From
	if user != nil {
		problem := ""
		switch {
		case user.ID == c.Bot.Id:
			problem = c.T("reporting.report_me_i_m_the_one")
		case user.ID == reporter.Id:
			problem = c.T("reporting.you_can_t_report_yourself")
		case c.IsChatAdmin(chat.Id, user.ID):
			problem = c.T("reporting.you_can_t_report_an_admin")
		default:
			if ok, _ := c.App.IsApproved(c.Ctx, chat.Id, user.ID); ok {
				problem = c.T("reporting.you_can_t_report_an_approved")
			}
		}
		if problem != "" {
			c.Say(problem)
			return nil
		}
	} else if target != m { // an @admin that is no reply calls the admins for the message itself
		problem := ""
		author := target.From
		switch {
		case target.SenderChat != nil && target.SenderChat.Id == chat.Id:
			problem = c.T("reporting.you_can_t_report_an_admin")
		case author != nil && author.Id == c.Bot.Id:
			problem = c.T("reporting.report_me_i_m_the_one")
		case author != nil && author.Id == reporter.Id:
			problem = c.T("reporting.you_can_t_report_yourself")
		case author != nil && !author.IsBot && c.IsChatAdmin(chat.Id, author.Id):
			problem = c.T("reporting.you_can_t_report_an_admin")
		default:
			if author != nil {
				if ok, _ := c.App.IsApproved(c.Ctx, chat.Id, author.Id); ok {
					problem = c.T("reporting.you_can_t_report_an_approved")
				}
			}
		}
		if problem != "" {
			c.Say(problem)
			return nil
		}
	}
	if !c.App.Cooldown(fmt.Sprintf("report:%d/%d", chat.Id, reporter.Id), reportCooldown) {
		if explicit {
			c.Say(c.T("reporting.you_just_sent_a_report_give"))
		}
		return nil
	}
	list, err := c.Bot.GetChatAdministratorsWithContext(c.Ctx, chat.Id, nil)
	if err != nil {
		c.Say(c.T("reporting.i_couldn_t_read_the_admins"))
		return nil
	}
	var admins []gotgbot.MergedChatMember
	for _, a := range list {
		if mm := a.MergeChatMember(); mm.User.IsBot {
			continue
		} else {
			admins = append(admins, mm)
		}
	}

	reason = squash(reason, maxReportReason)
	var reported string
	answerTo := target
	if user != nil {
		name := user.Name
		if name == "" {
			name = user.FirstName
		}
		if name == "" {
			name = user.Username
		}
		reported = withID(text.UserLink(user.ID, name), user.ID)
		answerTo = m // no message to report: answer the /report itself
	} else {
		reported = senderLink(c, target)
	}
	notice := c.T("reporting.reported_to_the_admins", core.V{"reported": reported})
	if reason != "" {
		notice += c.T("reporting.reason", core.V{"reason": text.Esc(reason)})
	}
	// Invisible mentions: they notify the admins without cluttering the message.
	for _, a := range admins[:min(len(admins), maxMentioned)] {
		notice += fmt.Sprintf("<a href=\"tg://user?id=%d\">​</a>", a.User.Id)
	}
	_, _ = c.ReplyTo(answerTo, notice)
	return alertAdmins(c, chat, answerTo, user == nil, admins, reporter, reported, reason)
}

// alertAdmins sends a private message to each admin who has alerts on (and has started a chat with the bot).
// quote says whether target is the reported message (its text is quoted) or the /report that named the user.
func alertAdmins(c *core.Ctx, chat gotgbot.Chat, target *gotgbot.Message, quote bool, admins []gotgbot.MergedChatMember, reporter *gotgbot.User, reported, reason string) error {
	shown := admins[:min(len(admins), maxAlerted)]
	ids := make([]int64, len(shown))
	for i, a := range shown {
		ids[i] = a.User.Id
	}
	muted, err := c.App.ReportOptedOut(c.Ctx, ids)
	if err != nil {
		return err
	}
	title := chat.Title
	if title == "" {
		title = c.T("common.a_chat")
	}
	lines := []string{
		c.T("reporting.new_report_in", core.V{"v": text.Esc(title)}),
		c.T("reporting.reporter", core.V{"id": withID(text.UserLink(reporter.Id, core.FullName(reporter)), reporter.Id)}),
		c.T("reporting.reported", core.V{"reported": reported}),
	}
	if reason != "" {
		lines = append(lines, "• <b>Reason:</b> <code>"+text.Esc(reason)+"</code>")
	}
	if ex := squash(core.TextOf(target), maxExcerpt); quote && ex != "" {
		lines = append(lines, "• <b>Message:</b> <code>"+text.Esc(ex)+"</code>")
	}
	opts := &gotgbot.SendMessageOpts{ParseMode: "HTML", LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true}}
	if link := messageLink(chat, target.MessageId); link != "" {
		opts.ReplyMarkup = gotgbot.InlineKeyboardMarkup{InlineKeyboard: [][]gotgbot.InlineKeyboardButton{{{Text: c.T("reporting.go_to_the_message"), Url: link}}}}
	}
	body := strings.Join(lines, "\n")
	for _, a := range shown {
		if muted[a.User.Id] || a.User.Id == reporter.Id {
			continue
		}
		_, _ = c.Bot.SendMessageWithContext(c.Ctx, a.User.Id, body, opts) // an admin who never started the bot can't be messaged
	}
	return nil
}

func cmdReport(c *core.Ctx) error {
	return fileReport(c, c.Msg, core.RepliedMessage(c.Msg), c.Args, true, true)
}

// callsAdmins finds "@admin" / "@admins" as a whole word.
func callIndexes(s string) [][2]int {
	var out [][2]int
	lower := s
	for i := 0; i+6 <= len(lower); i++ {
		if !strings.EqualFold(lower[i:i+6], "@admin") {
			continue
		}
		end := i + 6
		if end < len(lower) && (lower[end] == 's' || lower[end] == 'S') {
			end++
		}
		if i > 0 {
			if r := []rune(lower[:i]); isWordRune(r[len(r)-1]) {
				continue
			}
		}
		if end < len(lower) {
			if r := []rune(lower[end:]); isWordRune(r[0]) {
				continue
			}
		}
		out = append(out, [2]int{i, end})
		i = end - 1
	}
	return out
}

func isWordRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

func removeCalls(s string) string {
	spans := callIndexes(s)
	var b strings.Builder
	prev := 0
	for _, sp := range spans {
		b.WriteString(s[prev:sp[0]])
		prev = sp[1]
	}
	b.WriteString(s[prev:])
	return b.String()
}

// watchAdminCall: a message that writes @admin, from someone who is not an admin and where reporting is on.
func watchAdminCall(c *core.Ctx) (bool, error) {
	m := c.Msg
	if m == nil || c.Cb != nil || c.Edited || m.From == nil || !isGroup(m.Chat.Type) {
		return false, nil
	}
	body := m.Text
	if body == "" {
		body = m.Caption
	}
	if body == "" || strings.ContainsRune(c.App.Cfg.CommandPrefix, []rune(body)[0]) || len(callIndexes(body)) == 0 {
		return false, nil
	}
	if !c.App.Setting(c.Ctx, "reports", m.Chat.Id) || writesAsAdmin(c, m) {
		return false, nil
	}
	target := core.RepliedMessage(m)
	if target == nil {
		target = m
	}
	return true, fileReport(c, m, target, removeCalls(body), false, false)
}

func cmdReports(c *core.Ctx) error {
	arg := strings.ToLower(strings.TrimSpace(c.Args))
	usage := c.T("reporting.use_reports_on_or_reports_off")
	on, ok := text.ParseSwitch(arg)
	if arg != "" && !ok {
		c.Say(usage)
		return nil
	}
	state := func(v bool) string { return stateText(c, v) }
	switch {
	case c.Msg.Chat.Type == "private":
		if c.User == nil {
			return nil
		}
		if arg == "" {
			now, err := c.App.ReportAlertsOn(c.Ctx, c.User.Id)
			if err != nil {
				return err
			}
			c.Say(c.T("reporting.your_report_alerts_are", core.V{"opt": state(now), "usage": usage}))
			return nil
		}
		if err := c.App.SetReportAlerts(c.Ctx, c.User.Id, on); err != nil {
			return err
		}
		if on {
			c.Say(c.T("reporting.your_report_alerts_are_now_on"))
		} else {
			c.Say(c.T("reporting.your_report_alerts_are_now_off"))
		}
		return nil
	case !isGroup(c.Msg.Chat.Type):
		c.Say(c.T("reporting.this_command_only_works_in_groups"))
		return nil
	case arg == "":
		if c.BeginChatCommand(core.AuthorizeOpts{Right: "can_manage_chat"}) == nil {
			return nil
		}
		c.Say(c.T("reporting.reporting_is_in_this_chat", core.V{"opt": state(c.App.Setting(c.Ctx, "reports", c.Msg.Chat.Id)), "usage": usage}))
		return nil
	}
	if c.BeginChatCommand(core.AuthorizeOpts{Right: rightChangeInfo}) == nil {
		return nil
	}
	if err := c.App.SetSetting(c.Ctx, "reports", c.Msg.Chat.Id, on); err != nil {
		return err
	}
	if on {
		c.Say(c.T("reporting.reporting_is_now_on_anyone_can"))
	} else {
		c.Say(c.T("reporting.reporting_is_now_off_report_and"))
	}
	return nil
}
