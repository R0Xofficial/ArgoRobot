package modules

import (
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/core"
)

// Mute commands for chat admins. Same rules as the ban commands.

func registerMutes(r *core.Router) {
	r.Command(cmdMute, "mute", "smute")
	r.Command(cmdTmute, "tmute", "stmute", "stempmute")
	r.Command(cmdUnmute, "unmute", "sunmute")
	r.Button(core.CBUndo+"mute:", cbUnmute)
}

// muted is no right to write: an empty set of permissions.
var muted = gotgbot.ChatPermissions{}

// defaultPermissions is what every ordinary member of the chat may do.
func defaultPermissions(c *core.Ctx, chatID int64) gotgbot.ChatPermissions {
	if chat, err := c.Bot.GetChatWithContext(c.Ctx, chatID, nil); err == nil && chat.Permissions != nil {
		return *chat.Permissions
	}
	return gotgbot.ChatPermissions{CanSendMessages: true}
}

// isMuted: restricted without the right to write.
func isMuted(m *gotgbot.MergedChatMember) bool { return m.Status == "restricted" && !m.CanSendMessages }

func silentDetail(silent bool) []core.Detail {
	if silent {
		return []core.Detail{tagDetail("chatlog.silent", "common.yes")}
	}
	return nil
}

func deleteCommand(c *core.Ctx) {
	_, _ = c.Bot.DeleteMessageWithContext(c.Ctx, c.Msg.Chat.Id, c.Msg.MessageId, nil)
}

func cmdMute(c *core.Ctx) error {
	silent := c.Cmd == "smute"
	target, actor := c.Begin(core.BeginOpts{})
	if target == nil {
		return nil
	}
	saved := false
	_, err := c.Bot.RestrictChatMemberWithContext(c.Ctx, c.Msg.Chat.Id, target.ID, muted, nil)
	if err = c.PerformBan(err); err != nil {
		if !isUnknown(err) {
			return failed(c, err)
		}
		// A mute works on people who are not here: it is saved, and applied when they show up.
		if err := savePreban(c, target, target.Extra, nil, core.PrebanMute); err != nil {
			return err
		}
		saved = true
	}
	c.LogAction("MUTED", actor, target, target.Extra, silentDetail(silent)...)
	if silent {
		deleteCommand(c)
		return nil
	}
	if saved {
		return saidMuteSaved(c, target, actor, target.Extra)
	}
	_, err = c.Reply(c.T("mutes.was_muted", core.V{
		"who": c.Who(target), "by": c.By(actor), "reason_line": c.ReasonLine(target.Extra), "staff_note": c.StaffNote(actor),
	}), c.UndoKeyboard("mute", target))
	return err
}

func cmdTmute(c *core.Ctx) error {
	silent := c.Cmd == "stmute" || c.Cmd == "stempmute"
	target, actor := c.Begin(core.BeginOpts{})
	if target == nil {
		return nil
	}
	duration, reason, err := c.SplitDuration(target.Extra)
	if err == nil {
		_, err = c.Bot.RestrictChatMemberWithContext(c.Ctx, c.Msg.Chat.Id, target.ID, muted, &gotgbot.RestrictChatMemberOpts{
			UntilDate: time.Now().Add(duration).Unix(),
		})
		err = c.PerformBan(err)
	}
	if isUnknown(err) {
		until := time.Now().Add(duration)
		if err := savePreban(c, target, reason, &until, core.PrebanMute); err != nil {
			return err
		}
		c.LogAction("TMUTED", actor, target, reason, valueDetail("chatlog.duration", core.FormatDuration(duration)))
		if silent {
			deleteCommand(c)
			return nil
		}
		return saidMuteSaved(c, target, actor, reason)
	}
	if err != nil {
		return failed(c, err)
	}
	details := append([]core.Detail{valueDetail("chatlog.duration", core.FormatDuration(duration))}, silentDetail(silent)...)
	c.LogAction("TMUTED", actor, target, reason, details...)
	if silent {
		deleteCommand(c)
		return nil
	}
	_, err = c.Reply(c.T("mutes.was_muted_for", core.V{
		"who": c.Who(target), "format_duration": core.FormatDuration(duration), "by": c.By(actor),
		"reason_line": c.ReasonLine(reason), "staff_note": c.StaffNote(actor),
	}), c.UndoKeyboard("mute", target))
	return err
}

func cmdUnmute(c *core.Ctx) error {
	silent := c.Cmd == "sunmute"
	target, actor := c.Begin(core.BeginOpts{Unpunitive: true})
	if target == nil {
		return nil
	}
	chatID := c.Msg.Chat.Id
	if c.App.RemovePreban(c.Ctx, chatID, target.ID, core.PrebanMute) {
		// Only saved so far: forgetting it is the unmute.
		c.LogAction("UNMUTED", actor, target, "", silentDetail(silent)...)
		if silent {
			deleteCommand(c)
			return nil
		}
		_, err := c.ReplyText(c.T("mutes.was_unmuted", core.V{"who": c.Who(target), "by": c.By(actor), "staff_note": c.StaffNote(actor)}))
		return err
	}
	if m := c.GetMember(chatID, target.ID); m != nil {
		if m.Status == "creator" || m.Status == "administrator" {
			c.Say(c.T("common.i_can_t_take_action_against_2"))
			return nil
		}
		if !isMuted(m) {
			c.Say(c.T("mutes.isn_t_muted", core.V{"who": c.Who(target)}))
			return nil
		}
	}
	_, err := c.Bot.RestrictChatMemberWithContext(c.Ctx, chatID, target.ID, defaultPermissions(c, chatID), nil)
	if err = c.Perform(err); err != nil {
		return failed(c, err)
	}
	c.LogAction("UNMUTED", actor, target, "", silentDetail(silent)...)
	if silent {
		deleteCommand(c)
		return nil
	}
	_, err = c.ReplyText(c.T("mutes.was_unmuted", core.V{"who": c.Who(target), "by": c.By(actor), "staff_note": c.StaffNote(actor)}))
	return err
}

// cbUnmute is the Unmute button under a mute.
func cbUnmute(c *core.Ctx) error {
	userID, valid := core.ParseUndo(c.Cb.Data, "mute")
	actor, ok := c.AuthorizeCallback("")
	if !ok {
		return nil
	}
	if !valid || userID < 0 {
		c.Answer(c.T("common.this_button_is_not_valid"), true)
		return nil
	}
	chatID := c.Msg.Chat.Id
	target := c.TargetByID(chatID, userID)
	if c.App.RemovePreban(c.Ctx, chatID, userID, core.PrebanMute) { // only saved so far
		c.Answer("", false)
		c.LogAction("UNMUTED", actor, target, "", tagDetail("chatlog.undo", "common.yes"))
		return c.Edit(c.T("mutes.was_unmuted", core.V{"who": c.Who(target), "by": c.By(actor), "staff_note": c.StaffNote(actor)}), nil)
	}
	if m := c.GetMember(chatID, userID); m != nil && !isMuted(m) {
		c.Answer(c.T("mutes.they_aren_t_muted_any_more"), true)
		c.RemoveButtons()
		return nil
	}
	_, err := c.Bot.RestrictChatMemberWithContext(c.Ctx, chatID, userID, defaultPermissions(c, chatID), nil)
	if err = c.Perform(err); err != nil {
		if me, ok := err.(*core.ModerationError); ok {
			c.Answer(me.Msg, true)
			return nil
		}
		return err
	}
	c.Answer("", false)
	c.LogAction("UNMUTED", actor, target, "", tagDetail("chatlog.undo", "common.yes"))
	return c.Edit(c.T("mutes.was_unmuted", core.V{"who": c.Who(target), "by": c.By(actor), "staff_note": c.StaffNote(actor)}), nil)
}
