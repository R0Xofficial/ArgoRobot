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
	r.WatchMembers(-99, "bans:saved-member", watchSavedMember)
	r.Watch(-99, "bans:saved", watchSaved)
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
	return c.PerformBan(err)
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
	saved := false
	if err := ban(c, c.Msg.Chat.Id, target); err != nil {
		if !isUnknown(err) || deleting && replied != nil && !silent || target.IsChat() {
			return failed(c, err)
		}
		// A ban works on people who are not here: it is saved, and applied when they show up.
		if err := savePreban(c, target, target.Extra, nil, core.PrebanBan); err != nil {
			return err
		}
		saved = true
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
	if saved {
		return saidSaved(c, target, actor, target.Extra)
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
		err = c.PerformBan(err)
	}
	if isUnknown(err) {
		until := time.Now().Add(duration)
		if err := savePreban(c, target, reason, &until, core.PrebanBan); err != nil {
			return err
		}
		c.LogAction("TBANNED", actor, target, reason, valueDetail("chatlog.duration", core.FormatDuration(duration)))
		return saidSaved(c, target, actor, reason)
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
	if !target.IsChat() && c.App.RemovePreban(c.Ctx, c.Msg.Chat.Id, target.ID, core.PrebanBan) {
		// Only saved so far (Telegram never let me touch them): forgetting it is the unban.
		c.LogAction("UNBANNED", actor, target, "")
		_, err := c.ReplyText(c.T("bans.was_unbanned", core.V{
			"who": c.Who(target), "by": c.By(actor), "staff_note": c.StaffNote(actor),
		}))
		return err
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
	if !target.IsChat() && c.App.RemovePreban(c.Ctx, chatID, userID, core.PrebanBan) { // only saved so far
		c.Answer("", false)
		c.LogAction("UNBANNED", actor, target, "", tagDetail("chatlog.undo", "common.yes"))
		return c.Edit(c.T("bans.was_unbanned", core.V{
			"who": c.Who(target), "by": c.By(actor), "staff_note": c.StaffNote(actor),
		}), nil)
	}
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

// --- Saved bans -----------------------------------------------------------------------------

// isUnknown: Telegram can't find the user (the bot has never seen them).
func isUnknown(err error) bool {
	me, ok := err.(*core.ModerationError)
	return ok && me.Unknown
}

func savePreban(c *core.Ctx, target *core.Target, reason string, until *time.Time, kind string) error {
	p := core.Preban{Reason: reason, Until: until, Kind: kind}
	if c.User != nil {
		p.ByID, p.ByName = c.User.Id, core.FullName(c.User)
	}
	return c.App.AddPreban(c.Ctx, c.Msg.Chat.Id, target.ID, p)
}

func saidSaved(c *core.Ctx, target *core.Target, actor *core.Actor, reason string) error {
	_, err := c.Reply(c.T("bans.saved_for_later", core.V{
		"who": c.Who(target), "by": c.By(actor), "reason_line": c.ReasonLine(reason), "staff_note": c.StaffNote(actor),
	}), c.UndoKeyboard("ban", target))
	return err
}

func saidMuteSaved(c *core.Ctx, target *core.Target, actor *core.Actor, reason string) error {
	_, err := c.Reply(c.T("mutes.saved_for_later", core.V{
		"who": c.Who(target), "by": c.By(actor), "reason_line": c.ReasonLine(reason), "staff_note": c.StaffNote(actor),
	}), c.UndoKeyboard("mute", target))
	return err
}

// applySaved bans (or mutes) someone whose ban (or mute) was saved. The saved ban is used up either way: without the right to ban there
// is nothing to wait for.
func applySaved(c *core.Ctx, chatID, userID int64, name string, thread int64) (banned, done bool) {
	p, err := c.App.TakePreban(c.Ctx, chatID, userID)
	if err != nil || p == nil {
		return false, false
	}
	notice := "bans.saved_ban_applied"
	if p.Kind == core.PrebanMute {
		notice = "mutes.saved_mute_applied"
		opts := &gotgbot.RestrictChatMemberOpts{}
		if p.Until != nil {
			opts.UntilDate = p.Until.Unix()
		}
		if _, err := c.Bot.RestrictChatMemberWithContext(c.Ctx, chatID, userID, muted, opts); err != nil {
			return false, false
		}
	} else {
		opts := &gotgbot.BanChatMemberOpts{}
		if p.Until != nil {
			opts.UntilDate = p.Until.Unix()
		}
		if _, err := c.Bot.BanChatMemberWithContext(c.Ctx, chatID, userID, opts); err != nil {
			return false, false
		}
		banned = true
	}
	by := ""
	if p.ByID != 0 {
		by = c.T("moderation.by_2", core.V{"link": text.UserLink(p.ByID, p.ByName)})
	}
	notifyChat(c.Ctx, c.Bot, place{chat: chatID, thread: thread}, c.T(notice, core.V{
		"who": text.UserLink(userID, name), "by": by, "reason_line": c.ReasonLine(p.Reason),
	}))
	return banned, true
}

// watchSaved applies a saved ban when the person writes (or joins: the service message counts) in the chat.
func watchSaved(c *core.Ctx) (bool, error) {
	m := c.Msg
	if m == nil || c.Cb != nil || !isGroup(m.Chat.Type) {
		return false, nil
	}
	users := []gotgbot.User{}
	if len(m.NewChatMembers) > 0 {
		users = m.NewChatMembers
	} else if m.From != nil {
		users = []gotgbot.User{*m.From}
	}
	banned := false
	for _, u := range users {
		if c.App.HasPreban(c.Ctx, m.Chat.Id, u.Id) {
			if b, _ := applySaved(c, m.Chat.Id, u.Id, core.FullName(&u), threadOf(m)); b {
				banned = true
			}
		}
	}
	if banned {
		_, _ = c.Bot.DeleteMessageWithContext(c.Ctx, m.Chat.Id, m.MessageId, nil)
	}
	return banned, nil
}

// watchSavedMember is the same for somebody who joined without a service message.
func watchSavedMember(c *core.Ctx, e *gotgbot.ChatMemberUpdated) (bool, error) {
	if !isGroup(e.Chat.Type) {
		return false, nil
	}
	after := e.NewChatMember.MergeChatMember()
	if after.Status != "member" && after.Status != "restricted" {
		return false, nil
	}
	if !c.App.HasPreban(c.Ctx, e.Chat.Id, after.User.Id) {
		return false, nil
	}
	banned, _ := applySaved(c, e.Chat.Id, after.User.Id, core.FullName(&after.User), 0)
	return banned, nil
}
