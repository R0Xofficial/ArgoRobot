package modules

import (
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/core"
	"argo/internal/text"
)

// Ban commands for chat admins. Usable by chat admins (with the right to ban users) and by the
// bot's Owner, Developers and Sudo users. See core/moderation.go.

func registerBans(r *core.Router) {
	r.Command(cmdBan, "ban", "dban", "sban")
	r.Command(cmdTban, "tban")
	r.Command(cmdUnban, "unban")
	r.Button(core.CBUndo+"ban:", cbUnban)
	r.Command(cmdKick, "kick", "punch")
	r.Command(cmdKickme, "kickme", "punchme")
}

// ban bans a user, or a channel that writes into the chat.
func ban(c *core.Ctx, chatID int64, t *core.Target) error {
	if t.IsChat() {
		_, err := c.Bot.BanChatSenderChatWithContext(c.Ctx, chatID, t.ID, nil)
		return c.Perform(err)
	}
	_, err := c.Bot.BanChatMemberWithContext(c.Ctx, chatID, t.ID, nil)
	return c.Perform(err)
}

func unban(c *core.Ctx, chatID int64, t *core.Target) error {
	if t.IsChat() {
		_, err := c.Bot.UnbanChatSenderChatWithContext(c.Ctx, chatID, t.ID, nil)
		return c.Perform(err)
	}
	_, err := c.Bot.UnbanChatMemberWithContext(c.Ctx, chatID, t.ID, &gotgbot.UnbanChatMemberOpts{OnlyIfBanned: true})
	return c.Perform(err)
}

// silently deletes the command (and the message it was about): silent actions leave no trace.
func silently(c *core.Ctx, replied *gotgbot.Message) {
	_, _ = c.Bot.DeleteMessageWithContext(c.Ctx, c.Msg.Chat.Id, c.Msg.MessageId, nil)
	if replied != nil {
		_, _ = c.Bot.DeleteMessageWithContext(c.Ctx, c.Msg.Chat.Id, replied.MessageId, nil)
	}
}

// failed answers a Telegram refusal with its message; anything else is a real error.
func failed(c *core.Ctx, err error) error {
	if me, ok := err.(*core.ModerationError); ok {
		c.Say(text.Esc(me.Msg))
		return nil
	}
	return err
}

func cmdBan(c *core.Ctx) error {
	deleting := c.Cmd == "dban" || c.Cmd == "sban"
	silent := c.Cmd == "sban"

	target, actor := c.Begin(core.BeginOpts{AllowChats: true})
	if target == nil {
		return nil
	}
	var replied *gotgbot.Message
	if deleting {
		if replied = c.RequireReply(); replied == nil {
			return nil
		}
	}
	if err := ban(c, c.Msg.Chat.Id, target); err != nil {
		return failed(c, err)
	}

	var details []core.Detail
	if silent {
		details = append(details, tagDetail("chatlog.silent", "common.yes"))
	}
	c.LogAction("BANNED", actor, target, target.Extra, details...)
	if silent {
		silently(c, replied)
		return nil
	}
	if deleting {
		// Best effort: the message may already be gone.
		_, _ = c.Bot.DeleteMessageWithContext(c.Ctx, c.Msg.Chat.Id, replied.MessageId, nil)
	}
	_, err := c.Reply(c.T("bans.was_banned", core.V{
		"who": c.Who(target), "by": c.By(actor), "reason_line": c.ReasonLine(target.Extra), "staff_note": c.StaffNote(actor),
	}), c.UndoKeyboard("ban", target))
	return err
}

func cmdTban(c *core.Ctx) error {
	target, actor := c.Begin(core.BeginOpts{})
	if target == nil {
		return nil
	}
	duration, reason, err := c.SplitDuration(target.Extra)
	if err == nil {
		_, err = c.Bot.BanChatMemberWithContext(c.Ctx, c.Msg.Chat.Id, target.ID, &gotgbot.BanChatMemberOpts{
			UntilDate: time.Now().Add(duration).Unix(),
		})
		err = c.Perform(err)
	}
	if err != nil {
		return failed(c, err)
	}
	c.LogAction("TBANNED", actor, target, reason, valueDetail("chatlog.duration", core.FormatDuration(duration)))
	_, err = c.Reply(c.T("bans.was_banned_for", core.V{
		"who": c.Who(target), "format_duration": core.FormatDuration(duration), "by": c.By(actor),
		"reason_line": c.ReasonLine(reason), "staff_note": c.StaffNote(actor),
	}), c.UndoKeyboard("ban", target))
	return err
}

func cmdUnban(c *core.Ctx) error {
	target, actor := c.Begin(core.BeginOpts{AllowChats: true, Unpunitive: true})
	if target == nil {
		return nil
	}
	if !target.IsChat() {
		status := c.MemberStatus(c.Msg.Chat.Id, target.ID)
		if status == "creator" || status == "administrator" {
			c.Say(c.T("common.i_can_t_take_action_against_2"))
			return nil
		}
		if status != "" && status != "kicked" {
			c.Say(c.T("bans.isn_t_banned", core.V{"who": c.Who(target)}))
			return nil
		}
	}
	if err := unban(c, c.Msg.Chat.Id, target); err != nil {
		return failed(c, err)
	}
	c.LogAction("UNBANNED", actor, target, "")
	_, err := c.ReplyText(c.T("bans.was_unbanned", core.V{
		"who": c.Who(target), "by": c.By(actor), "staff_note": c.StaffNote(actor),
	}))
	return err
}

// cbUnban is the Unban button under a ban.
func cbUnban(c *core.Ctx) error {
	userID, valid := core.ParseUndo(c.Cb.Data, "ban")
	actor, ok := c.AuthorizeCallback("")
	if !ok {
		return nil
	}
	if !valid {
		c.Answer(c.T("common.this_button_is_not_valid"), true)
		return nil
	}
	chatID := c.Msg.Chat.Id
	target := c.TargetByID(chatID, userID)
	if !target.IsChat() {
		if status := c.MemberStatus(chatID, userID); status != "" && status != "kicked" {
			c.Answer(c.T("bans.they_aren_t_banned_any_more"), true)
			c.RemoveButtons()
			return nil
		}
	}
	if err := unban(c, chatID, target); err != nil {
		if me, ok := err.(*core.ModerationError); ok {
			c.Answer(me.Msg, true)
			return nil
		}
		return err
	}
	c.Answer("", false)
	c.LogAction("UNBANNED", actor, target, "", tagDetail("chatlog.undo", "common.yes"))
	return c.Edit(c.T("bans.was_unbanned", core.V{
		"who": c.Who(target), "by": c.By(actor), "staff_note": c.StaffNote(actor),
	}), nil)
}

func cmdKick(c *core.Ctx) error {
	target, actor := c.Begin(core.BeginOpts{})
	if target == nil {
		return nil
	}
	chatID := c.Msg.Chat.Id
	// A ban followed by an unban removes the user but lets them come back.
	_, err := c.Bot.BanChatMemberWithContext(c.Ctx, chatID, target.ID, nil)
	if err = c.Perform(err); err == nil {
		_, err = c.Bot.UnbanChatMemberWithContext(c.Ctx, chatID, target.ID, &gotgbot.UnbanChatMemberOpts{OnlyIfBanned: true})
		err = c.Perform(err)
	}
	if err != nil {
		return failed(c, err)
	}
	c.LogAction("KICKED", actor, target, target.Extra)
	_, err = c.ReplyText(c.T("bans.was_kicked", core.V{
		"who": c.Who(target), "by": c.By(actor), "reason_line": c.ReasonLine(target.Extra), "staff_note": c.StaffNote(actor),
	}))
	return err
}

// cmdKickme: anyone may remove themselves. Admins can't be removed by a bot, so they are told so.
func cmdKickme(c *core.Ctx) error {
	user, m := c.User, c.Msg
	if m.Chat.Type != "group" && m.Chat.Type != "supergroup" {
		c.Say(c.T("common.this_command_only_works_in_groups"))
		return nil
	}
	if user == nil || m.SenderChat != nil {
		c.Say(c.T("common.send_this_as_yourself_not_as"))
		return nil
	}
	if c.IsChatAdmin(m.Chat.Id, user.Id) {
		c.Say(c.T("bans.i_can_t_remove_an_admin"))
		return nil
	}
	_, err := c.Bot.BanChatMemberWithContext(c.Ctx, m.Chat.Id, user.Id, nil)
	if err = c.Perform(err); err == nil {
		_, err = c.Bot.UnbanChatMemberWithContext(c.Ctx, m.Chat.Id, user.Id, &gotgbot.UnbanChatMemberOpts{OnlyIfBanned: true})
		err = c.Perform(err)
	}
	if err != nil {
		return failed(c, err)
	}
	c.LogSelf("KICKED", user.Id, core.FullName(user), "bans.used_kickme")
	// Not attached to the message: the message is gone with the user's access.
	_, err = c.SendPlain(c.T("bans.kicked_themselves_out_they_can_come", core.V{"id": text.UserLink(user.Id, core.FullName(user))}))
	return err
}
