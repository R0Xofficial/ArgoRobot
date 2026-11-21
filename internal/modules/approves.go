package modules

import (
	"errors"
	"fmt"
	"strings"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/core"
	"argo/internal/text"
)

// Approved users: trusted people that automatic measures skip.

const (
	maxShown  = 60
	cbApprove = "approve:clear:" // approve:clear:yes / approve:clear:no
)

func registerApproves(r *core.Router) {
	core.RegisterInfo("approves", false, func(c *core.Ctx, userID, chatID int64) []core.InfoFlag {
		if chatID == 0 {
			return nil // approval belongs to a chat
		}
		ok, _ := c.App.IsApproved(c.Ctx, chatID, userID)
		return []core.InfoFlag{{Label: c.T("approves.approved"), Value: ok}}
	})
	r.Command(cmdApprove, "approve")
	r.Command(cmdUnapprove, "unapprove")
	r.Command(cmdApproval, "approval")
	r.Command(cmdApproved, "approved", "approvals", "approvedlist")
	r.Command(cmdDisapproveall, "disapproveall")
	r.Button(cbApprove, cbDisapproveall)
}

var noPreview = &gotgbot.LinkPreviewOptions{IsDisabled: true}

// replyNoPreview answers without a link preview.
func replyNoPreview(c *core.Ctx, msg string) error {
	_, err := c.ReplyOpts(msg, nil, noPreview)
	return err
}

// beginApprove checks the person (chat admins, or the bot's staff from Support up) and finds the target.
func beginApprove(c *core.Ctx) (*core.Target, *core.Actor) {
	actor := c.BeginChatCommand(core.AuthorizeOpts{StaffMin: core.Support})
	if actor == nil {
		return nil, nil
	}
	target, err := c.ResolveTarget(true, false)
	if err != nil {
		var te *core.TargetError
		if errors.As(err, &te) {
			c.Say(text.Esc(te.Msg))
			return nil, nil
		}
		c.App.ReportError("/"+c.Cmd, err)
		return nil, nil
	}
	if core.IsImmune(target.ID) {
		c.Say(c.T("common.i_can_t_take_action_against"))
		return nil, nil
	}
	return target, actor
}

func cmdApprove(c *core.Ctx) error {
	target, actor := beginApprove(c)
	if target == nil {
		return nil
	}
	chatID := c.Msg.Chat.Id
	if target.ID == c.Bot.Id || c.IsChatAdmin(chatID, target.ID) {
		c.Say(c.T("approves.that_user_can_t_be_approved"))
		return nil
	}
	added, err := c.App.Approve(c.Ctx, chatID, target.ID, target.Name, actor.ID, actor.Name)
	if err != nil {
		return err
	}
	if !added {
		c.Say(c.T("approves.this_user_is_already_approved"))
		return nil
	}
	for _, hook := range core.ApproveHooks {
		hook(c, chatID, target.ID)
	}
	_, err = c.ReplyText(c.T("approves.was_approved_locks_blacklists_antiflood_and", core.V{
		"who": text.UserLink(target.ID, target.Name), "by": c.By(actor),
	}) + c.StaffNote(actor))
	return err
}

func cmdUnapprove(c *core.Ctx) error {
	target, actor := beginApprove(c)
	if target == nil {
		return nil
	}
	removed, err := c.App.Unapprove(c.Ctx, c.Msg.Chat.Id, target.ID)
	if err != nil {
		return err
	}
	if !removed {
		c.Say(c.T("approves.this_user_was_not_approved_in"))
		return nil
	}
	_, err = c.ReplyText(c.T("approves.is_no_longer_approved", core.V{
		"user_id": text.UserLink(target.ID, target.Name), "by": c.By(actor),
	}) + c.StaffNote(actor))
	return err
}

func cmdApproval(c *core.Ctx) error {
	var userID int64
	var name string
	if strings.TrimSpace(c.Args) != "" || core.RepliedMessage(c.Msg) != nil {
		target, _ := beginApprove(c)
		if target == nil {
			return nil
		}
		userID, name = target.ID, target.Name
	} else {
		if c.BeginChatCommand(core.AuthorizeOpts{StaffMin: core.Support}) == nil {
			return nil
		}
		userID, name = c.User.Id, core.FullName(c.User)
	}
	who := text.UserRef(userID, name)
	ap, err := c.App.GetApproval(c.Ctx, c.Msg.Chat.Id, userID)
	if err != nil {
		return err
	}
	if ap == nil {
		c.Say(c.T("approves.is_not_approved_in_this_chat", core.V{"who": who}))
		return nil
	}
	by := ""
	if ap.ApprovedBy != 0 {
		by = c.T("approves.by", core.V{"approved_by": text.UserRef(ap.ApprovedBy, ap.ByName)})
	}
	return replyNoPreview(c, c.T("approves.is_approved_in_this_chat", core.V{"who": who, "by": by}))
}

func cmdApproved(c *core.Ctx) error {
	if c.BeginChatCommand(core.AuthorizeOpts{StaffMin: core.Support}) == nil {
		return nil
	}
	rows, err := c.App.ListApprovals(c.Ctx, c.Msg.Chat.Id)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		c.Say(c.T("approves.there_are_no_approved_users_in"))
		return nil
	}
	lines := []string{
		c.T("approves.approved_users_in", core.V{"title": text.Esc(c.Msg.Chat.Title)}),
		c.T("approves.automatic_measures_leave_them_alone_in"),
		"",
	}
	for i, row := range rows {
		if i >= maxShown {
			break
		}
		lines = append(lines, "• "+text.UserRef(row.UserID, row.Name))
	}
	if len(rows) > maxShown {
		lines = append(lines, fmt.Sprintf("…and %d more.", len(rows)-maxShown))
	}
	return replyNoPreview(c, strings.Join(lines, "\n"))
}

func cmdDisapproveall(c *core.Ctx) error {
	if !c.RequireChatOwner() {
		return nil
	}
	rows, err := c.App.ListApprovals(c.Ctx, c.Msg.Chat.Id)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		c.Say(c.T("approves.there_are_no_approved_users_in"))
		return nil
	}
	_, err = c.Reply(c.T("approves.unapprove_all_approved_users_of_this", core.V{"count": len(rows)}), core.Keyboard(
		[]gotgbot.InlineKeyboardButton{
			core.Button(c.T("approves.unapprove_all"), cbApprove+"yes"),
			core.Button(c.T("common.cancel"), cbApprove+"no"),
		}))
	return err
}

func cbDisapproveall(c *core.Ctx) error {
	if c.Msg == nil {
		c.Answer(c.T("common.this_button_is_no_longer_valid"), true)
		return nil
	}
	if c.MemberStatus(c.Msg.Chat.Id, c.User.Id) != "creator" {
		c.Answer(c.T("common.only_the_owner_of_this_chat"), true)
		return nil
	}
	if c.Cb.Data == cbApprove+"yes" {
		removed, err := c.App.ClearApprovals(c.Ctx, c.Msg.Chat.Id)
		if err != nil {
			return err
		}
		c.Answer("", false)
		return c.Edit(c.T("approves.done_users_are_no_longer_approved", core.V{"removed": removed}), nil)
	}
	c.Answer("", false)
	return c.Edit(c.T("approves.cancelled_nobody_was_unapproved"), nil)
}
