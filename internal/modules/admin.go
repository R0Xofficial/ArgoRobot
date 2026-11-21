package modules

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/core"
	"argo/internal/text"
)

// Group administration (modules/admin.py).

const (
	cleanRight    = "can_change_info" // the admin right needed for /cleanbluetext
	maxChatTitle  = 128               // Telegram's limit for a chat title
	maxLinkUses   = 99999
	maxAdminsList = 80
	pinRight      = "can_pin_messages"
	pinRefused    = "admin.pin_refused"
	cbUnpinall    = "unpinall:" // unpinall:yes / unpinall:no
)

func registerAdmin(r *core.Router) {
	r.Command(cmdStaffhelp, "staffhelp")
	r.Command(cmdAdminerror, "adminerror")
	r.Command(cmdAnonadmin, "anonadmin")
	r.Command(cmdLink, "link")
	r.Command(cmdGtitle, "gtitle")
	r.Command(cmdAdminlist, "adminlist")
	r.Command(cmdCleanbluetext, "cleanbluetext")
	r.Command(cmdPin, "pin")
	r.Command(cmdUnpin, "unpin")
	r.Command(cmdUnpinall, "unpinall")
	r.Button(cbUnpinall, cbUnpinallPress)
}

func stateText(c *core.Ctx, enabled bool) string {
	if enabled {
		return c.T("common.on")
	}
	return c.T("common.off")
}

// switchSetting is a yes/no setting of the chat. Any admin can look at it; only the chat's owner can change it.
// The bot's staff can't change it either.
func switchSetting(c *core.Ctx, setting string, status func(bool) string, changed func(bool) string, usage string) error {
	arg := strings.ToLower(strings.TrimSpace(c.Args))
	chatID := c.Msg.Chat.Id
	if arg == "" {
		// can_manage_chat is held by every admin: looking is for any of them.
		if c.BeginChatCommand(core.AuthorizeOpts{Right: "can_manage_chat"}) == nil {
			return nil
		}
		c.Say(status(c.App.Setting(c.Ctx, setting, chatID)))
		return nil
	}
	enabled, ok := text.ParseSwitch(arg)
	if !ok {
		c.Say(usage)
		return nil
	}
	if !c.RequireChatOwner() {
		return nil
	}
	if err := c.App.SetSetting(c.Ctx, setting, chatID, enabled); err != nil {
		return err
	}
	c.Say(changed(enabled))
	return nil
}

func cmdStaffhelp(c *core.Ctx) error {
	return switchSetting(c, "staff_help",
		func(on bool) string {
			return c.T("admin.help_from_the_bot_s_staff_3", core.V{"state": stateText(c, on)})
		},
		func(on bool) string {
			if on {
				return c.T("admin.help_from_the_bot_s_staff")
			}
			return c.T("admin.help_from_the_bot_s_staff_2")
		},
		c.T("admin.use_staffhelp_on_or_staffhelp_off"))
}

func cmdAdminerror(c *core.Ctx) error {
	return switchSetting(c, "admin_errors",
		func(on bool) string {
			return c.T("admin.error_replies_are_in_this_chat", core.V{"state": stateText(c, on)})
		},
		func(on bool) string {
			if on {
				return c.T("admin.error_replies_are_now_on_people")
			}
			return c.T("admin.error_replies_are_now_off_admin")
		},
		c.T("admin.use_adminerror_on_or_adminerror_off"))
}

func cmdAnonadmin(c *core.Ctx) error {
	return switchSetting(c, "anon_admin",
		func(on bool) string {
			return c.T("admin.trust_in_anonymous_admins_is_in", core.V{"state": stateText(c, on)})
		},
		func(on bool) string {
			if on {
				return c.T("admin.anonymous_admins_can_now_use_all")
			}
			return c.T("admin.anonymous_admins_are_no_longer_trusted")
		},
		c.T("admin.use_anonadmin_on_or_anonadmin_off"))
}

// --- /link --------------------------------------------------------------------------

// parseLinkOptions: "" / "request" / "<count>" / "count <count>" -> (needs approval, use limit or 0).
func parseLinkOptions(c *core.Ctx, arg string) (bool, int64, error) {
	arg = strings.ToLower(strings.TrimSpace(arg))
	if arg == "" {
		return false, 0, nil
	}
	if arg == "request" {
		return true, 0, nil
	}
	words := strings.Fields(arg)
	if len(words) == 2 && words[0] == "count" {
		words = words[1:]
	}
	if len(words) == 1 && strings.Trim(words[0], "0123456789") == "" {
		if n, err := strconv.ParseInt(words[0], 10, 64); err == nil && n >= 1 && n <= maxLinkUses {
			return false, n, nil
		}
	}
	return false, 0, refuse(c.T("admin.use_link_link_request_or_link", core.V{"MAX_LINK_USES": maxLinkUses}))
}

func cmdLink(c *core.Ctx) error {
	actor := c.BeginChatCommand(core.AuthorizeOpts{Right: "can_invite_users"})
	if actor == nil {
		return nil
	}
	chat := c.Msg.Chat
	needsApproval, limit, err := parseLinkOptions(c, c.Args)
	if err != nil {
		return finish(c, err)
	}
	me := c.GetMember(chat.Id, c.Bot.Id)
	if me == nil || !(me.Status == "creator" || me.CanInviteUsers) {
		return finish(c, refuse(c.T("admin.i_need_the_admin_right_to")))
	}
	const rights = "admin.i_don_t_have_the_right"

	if !needsApproval && limit == 0 {
		// The chat's usual link: public chats have one; otherwise use the existing primary link, and only make
		// one when there is none.
		var link string
		if chat.Username != "" {
			link = "https://t.me/" + chat.Username
		} else {
			full, err := c.Bot.GetChatWithContext(c.Ctx, chat.Id, nil)
			if err = c.Perform(err, rights); err != nil {
				return finish(c, err)
			}
			link = full.InviteLink
			if link == "" {
				link, err = c.Bot.ExportChatInviteLinkWithContext(c.Ctx, chat.Id, nil)
				if err = c.Perform(err, rights); err != nil {
					return finish(c, err)
				}
			}
		}
		_, err := c.ReplyOpts(c.T("admin.invite_link", core.V{"link": text.Esc(link)}), nil, noPreview)
		return err
	}

	created, err := c.Bot.CreateChatInviteLinkWithContext(c.Ctx, chat.Id, &gotgbot.CreateChatInviteLinkOpts{
		Name:               text.Truncate(c.T("admin.by", core.V{"name": actor.Name}), 32),
		CreatesJoinRequest: needsApproval,
		MemberLimit:        limit,
	})
	if err = c.Perform(err, rights); err != nil {
		return finish(c, err)
	}
	var kind string
	if needsApproval {
		kind = c.T("admin.invite_link_that_requires_approval_to")
	} else {
		opt := c.T("admin.people")
		if limit == 1 {
			opt = c.T("admin.person")
		}
		kind = c.T("admin.invite_link_for", core.V{"limit": limit, "opt": opt})
	}
	_, err = c.ReplyOpts(c.T("admin.was_created", core.V{
		"kind": kind, "by": c.By(actor), "invite_link": text.Esc(created.InviteLink),
	}), nil, noPreview)
	return err
}

// --- /gtitle ------------------------------------------------------------------------

func cmdGtitle(c *core.Ctx) error {
	actor := c.BeginChatCommand(core.AuthorizeOpts{Right: "can_change_info"})
	if actor == nil {
		return nil
	}
	title := strings.TrimSpace(c.Args)
	if title == "" {
		return finish(c, refuse(c.T("admin.give_the_new_title_for_example")))
	}
	if len([]rune(title)) > maxChatTitle {
		return finish(c, refuse(c.T("admin.a_chat_title_can_have_at", core.V{"MAX_TITLE": maxChatTitle})))
	}
	me := c.GetMember(c.Msg.Chat.Id, c.Bot.Id)
	if me == nil || !(me.Status == "creator" || me.CanChangeInfo) {
		return finish(c, refuse(c.T("admin.i_need_the_admin_right_to_2")))
	}
	_, err := c.Bot.SetChatTitleWithContext(c.Ctx, c.Msg.Chat.Id, title, nil)
	if err = c.Perform(err, "admin.i_can_t_change_the_title"); err != nil {
		return finish(c, err)
	}
	_, err = c.ReplyText(c.T("admin.the_chat_title_was_changed_to", core.V{"title": text.Esc(title), "by": c.By(actor)}))
	return err
}

// --- /adminlist ---------------------------------------------------------------------

func cmdAdminlist(c *core.Ctx) error {
	if !core.IsGroup(c.Msg.Chat) {
		c.Say(c.T("common.this_command_only_works_in_groups"))
		return nil
	}
	admins, err := c.Bot.GetChatAdministratorsWithContext(c.Ctx, c.Msg.Chat.Id, nil)
	if err != nil {
		c.Say(c.T("admin.i_couldn_t_read_the_list"))
		return nil
	}
	line := func(a gotgbot.MergedChatMember) string {
		s := "• " + text.UserLink(a.User.Id, core.FullName(&a.User))
		if a.User.IsBot {
			s += c.T("admin.bot")
		}
		if a.IsAnonymous {
			s += c.T("admin.anonymous")
		}
		if a.CustomTitle != "" {
			s += " — " + text.Esc(a.CustomTitle)
		}
		return s
	}
	var owners, others []gotgbot.MergedChatMember
	for _, a := range admins {
		m := a.MergeChatMember()
		if m.Status == "creator" {
			owners = append(owners, m)
		} else {
			others = append(others, m)
		}
	}
	lines := []string{c.T("admin.admins_in", core.V{"title": text.Esc(c.Msg.Chat.Title)})}
	if len(owners) > 0 {
		lines = append(lines, "", c.T("admin.owner"))
		for _, a := range owners {
			lines = append(lines, line(a))
		}
	}
	if len(others) > 0 {
		lines = append(lines, "", c.T("admin.admins"))
		for i, a := range others {
			if i >= maxAdminsList {
				break
			}
			lines = append(lines, line(a))
		}
		if len(others) > maxAdminsList {
			lines = append(lines, fmt.Sprintf("…and %d more.", len(others)-maxAdminsList))
		}
	}
	_, err = c.ReplyOpts(strings.Join(lines, "\n"), nil, noPreview)
	return err
}

// --- /cleanbluetext: the deleting itself is Router.cleanCommand -----------------------

func botCanDelete(c *core.Ctx) bool {
	m := c.GetMember(c.Msg.Chat.Id, c.Bot.Id)
	return m != nil && (m.Status == "creator" || m.CanDeleteMessages)
}

func cmdCleanbluetext(c *core.Ctx) error {
	if !core.IsGroup(c.Msg.Chat) {
		c.Say(c.T("common.this_command_only_works_in_groups"))
		return nil
	}
	if c.BeginChatCommand(core.AuthorizeOpts{Right: cleanRight}) == nil {
		return nil
	}
	usage := c.T("admin.use_cleanbluetext_on_or_cleanbluetext_off")
	arg := strings.ToLower(strings.TrimSpace(c.Args))
	chatID := c.Msg.Chat.Id
	if arg == "" {
		opt := c.T("admin.kept")
		if c.App.Setting(c.Ctx, "clean_commands", chatID) {
			opt = c.T("admin.deleted_after_they_run")
		}
		c.Say(c.T("admin.commands_are", core.V{"opt": opt, "usage": usage}))
		return nil
	}
	enabled, ok := text.ParseSwitch(arg)
	if !ok {
		c.Say(usage)
		return nil
	}
	if err := c.App.SetSetting(c.Ctx, "clean_commands", chatID, enabled); err != nil {
		return err
	}
	if !enabled {
		c.Say(c.T("admin.commands_will_be_kept"))
		return nil
	}
	msg := c.T("admin.commands_will_be_deleted_after_they")
	if !botCanDelete(c) {
		msg += c.T("admin.i_can_t_delete_messages_here")
	}
	c.Say(msg)
	return nil
}

// --- /pin and /unpin ----------------------------------------------------------------

// pinProblem is a sentence for the failures Telegram reports as plain bad requests.
func pinProblem(c *core.Ctx, err error, action string) string {
	_, p := tgProblem(err)
	switch {
	case strings.Contains(p, "not found") || strings.Contains(p, "message_id_invalid"):
		if action == "unpin" {
			return c.T("admin.that_message_isn_t_pinned")
		}
		return c.T("admin.i_can_t_find_that_message")
	case strings.Contains(p, "chat_not_modified") || strings.Contains(p, "not modified"):
		return c.T("admin.that_message_is_already_pinned")
	}
	return c.T("admin.i_couldn_t_that_message", core.V{"action": c.L.Word(action)})
}

func cmdPin(c *core.Ctx) error {
	actor := c.BeginChatCommand(core.AuthorizeOpts{Right: pinRight})
	if actor == nil {
		return nil
	}
	arg := strings.ToLower(strings.TrimSpace(c.Args))
	if arg != "" && arg != "loud" && arg != "notify" && arg != "violent" {
		c.Say(c.T("admin.use_pin_as_a_reply_to"))
		return nil
	}
	target := c.RequireReply()
	if target == nil {
		return nil
	}
	_, err := c.Bot.PinChatMessageWithContext(c.Ctx, c.Msg.Chat.Id, target.MessageId, &gotgbot.PinChatMessageOpts{DisableNotification: arg == ""})
	if err = c.Perform(err, pinRefused); err != nil {
		return pinFailure(c, err, "pin")
	}
	tail := "."
	if arg != "" {
		tail = c.T("admin.and_everyone_was_notified")
	}
	_, err = c.ReplyText(c.T("admin.the_message_was_pinned") + c.By(actor) + tail)
	return err
}

// pinFailure answers a failed pin or unpin: a refusal, a known bad request, or a real failure that is returned.
func pinFailure(c *core.Ctx, err error, action string) error {
	var me *core.ModerationError
	if errors.As(err, &me) {
		c.Deny(err)
		return nil
	}
	if badRequest(err) {
		c.Say(text.Esc(pinProblem(c, err, action)))
		return nil
	}
	return err
}

func cmdUnpin(c *core.Ctx) error {
	actor := c.BeginChatCommand(core.AuthorizeOpts{Right: pinRight})
	if actor == nil {
		return nil
	}
	target := core.RepliedMessage(c.Msg)
	opts := &gotgbot.UnpinChatMessageOpts{}
	if target != nil {
		opts.MessageId = &target.MessageId
	}
	_, err := c.Bot.UnpinChatMessageWithContext(c.Ctx, c.Msg.Chat.Id, opts)
	if err = c.Perform(err, pinRefused); err != nil {
		return pinFailure(c, err, "unpin")
	}
	what := c.T("admin.the_most_recent_pinned_message")
	if target != nil {
		what = c.T("admin.the_message")
	}
	_, err = c.ReplyText(c.T("admin.was_unpinned", core.V{"what": what, "by": c.By(actor)}))
	return err
}

func cmdUnpinall(c *core.Ctx) error {
	if c.BeginChatCommand(core.AuthorizeOpts{Right: pinRight}) == nil {
		return nil
	}
	markup := core.Keyboard([]gotgbot.InlineKeyboardButton{
		core.Button(c.T("admin.unpin_all"), cbUnpinall+"yes"),
		core.Button(c.T("common.cancel"), cbUnpinall+"no"),
	})
	_, err := c.Reply(c.T("admin.unpin_every_pinned_message_of_this"), markup)
	return err
}

func cbUnpinallPress(c *core.Ctx) error {
	actor, ok := c.AuthorizeCallback(pinRight)
	if !ok {
		return nil
	}
	if c.Cb.Data == cbUnpinall+"no" {
		c.Answer("", false)
		return c.Edit(c.T("admin.cancelled_nothing_was_unpinned"), nil)
	}
	_, err := c.Bot.UnpinAllChatMessagesWithContext(c.Ctx, c.Msg.Chat.Id, nil)
	if err = c.Perform(err, pinRefused); err != nil {
		var me *core.ModerationError
		switch {
		case errors.As(err, &me):
			c.Answer(me.Msg, true)
			return nil
		case badRequest(err):
			c.Answer(pinProblem(c, err, "unpin"), true)
			return nil
		}
		return err
	}
	c.Answer("", false)
	return c.Edit(c.T("admin.every_pinned_message_was_unpinned", core.V{"by": c.By(actor), "staff_note": c.StaffNote(actor)}), nil)
}
