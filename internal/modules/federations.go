package modules

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/core"
	"argo/internal/i18n"
	"argo/internal/text"
)

// Federations: one ban list for many chats.

var fedIDRE = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

const (
	fedExportCooldown  = 30 * time.Second
	maxListChars       = 3500 // a longer /fedstat list is sent as a file
	rightChangeInfoFed = "can_change_info"
	cbFedDelete        = "fd:"
	cbFedTransfer      = "fx:"
	cbFedPromote       = "fp:"
	cbFedReason        = "fr:"
	cbFedAdmins        = "fa:"
	reasonTTL          = 5 * time.Minute // how long an "Update reason?" question stays valid
)

func registerFederations(r *core.Router) {
	r.WatchMembers(-95, "federations:member", watchFedMember)
	r.WatchReactions(-95, "federations:reaction", watchFedReaction)
	r.Watch(-95, "federations", watchFederations)
	r.WatchMyMember("federations:rights", watchFedRights)

	r.Command(cmdNewfed, "newfed")
	r.Command(cmdRenamefed, "renamefed")
	r.Command(cmdDelfed, "delfed")
	r.Command(cmdFedtransfer, "fedtransfer")
	r.Command(cmdFedpromote, "fedpromote", "fpromote")
	r.Command(cmdFeddemote, "feddemote", "fdemote")
	r.Command(cmdFeddemoteme, "feddemoteme")
	r.Command(cmdFednotif, "fednotif")
	r.Command(cmdFedreason, "fedreason")
	r.Command(cmdSubfed, "subfed")
	r.Command(cmdUnsubfed, "unsubfed")
	r.Command(cmdSetfedlog, "setfedlog")
	r.Command(cmdUnsetfedlog, "unsetfedlog")
	r.Command(cmdFedexport, "fedexport")
	r.Command(cmdFedimport, "fedimport")
	r.Command(cmdFban, "fban", "dfban")
	r.Command(cmdUnfban, "unfban")
	r.CommandRole(core.Developer, cmdClearfban, "clearfban")
	r.Command(cmdMyfeds, "myfeds")
	r.Command(cmdFedinfo, "fedinfo")
	r.Command(cmdFedadmins, "fedadmins")
	r.Command(cmdFedsubs, "fedsubs")
	r.Command(cmdChatfed, "chatfed")
	r.Command(cmdJoinfed, "joinfed")
	r.Command(cmdLeavefed, "leavefed")
	r.Command(cmdQuietfed, "quietfed")
	r.Command(cmdFedstat, "fedstat", "fbanstat", "fstat")
	r.Button(cbFedDelete, cbFedDeleteButton)
	r.Button(cbFedTransfer, cbFedTransferButton)
	r.Button(cbFedPromote, cbFedPromoteButton)
	r.Button(cbFedReason, cbFedReasonButton)
	r.Button(cbFedAdmins, cbFedAdminsButton)

	core.RegisterInfoNotes("federations", fedInfoNotes)
}

// --- Small helpers ----------------------------------------------------------------------

func fedItem(label, value string) string { return "• <b>" + label + ":</b> " + value }
func fedName(f *core.Federation) string  { return "<b>" + text.Esc(f.Name) + "</b>" }
func fedOwnLine(l i18n.Lang, f *core.Federation) string {
	return fedItem(l.T("federations.fed"), text.Esc(f.Name))
}
func fedOwnerLink(f *core.Federation) string { return text.UserLink(f.OwnerID, f.OwnerName) }

func codeOf(v any) string { return "<code>" + text.Esc(v) + "</code>" }

// otherLines is the other federation of a subscription, as two lines: <label>: its name and <label>ID: its id.
func otherLines(l i18n.Lang, label string, f *core.Federation, id string) string {
	name, ident := l.T("federations.that_federation"), id
	if f != nil {
		name, ident = text.Esc(f.Name), f.ID
	}
	return fedItem(label, name) + "\n" + fedItem(label+l.T("common.id"), "<code>"+text.Esc(ident)+"</code>")
}

// fedEvent tells the log chat of the federation, and its owner when they asked for private messages. build writes
// the message: each one is written in the language of the chat it goes to.
func fedEvent(c *core.Ctx, f *core.Federation, build func(l i18n.Lang) string) {
	noPreview := &gotgbot.SendMessageOpts{ParseMode: "HTML", LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true}}
	sent := false
	if f.LogChatID != 0 {
		l := c.App.UseChat(c.Ctx, f.LogChatID)
		if _, err := c.Bot.SendMessageWithContext(c.Ctx, f.LogChatID, build(l), noPreview); err == nil {
			sent = true
		} else {
			slog.Debug("Federation log unreachable", "chat", f.LogChatID, "err", err)
		}
	}
	if f.Notify && !(sent && f.LogChatID == f.OwnerID) {
		l := c.App.UseChat(c.Ctx, f.OwnerID)
		if _, err := c.Bot.SendMessageWithContext(c.Ctx, f.OwnerID, build(l), noPreview); err != nil {
			slog.Debug("Federation owner unreachable", "owner", f.OwnerID, "err", err)
		}
	}
}

func fedAdminEvent(c *core.Ctx, f *core.Federation, promoted bool, userID int64, who string) {
	fedEvent(c, f, func(l i18n.Lang) string {
		title, label := l.T("federations.new_fed_demotion"), l.T("federations.demoted_fedadmin")
		if promoted {
			title, label = l.T("federations.new_fed_promotion"), l.T("federations.promoted_fedadmin")
		}
		return "<b>" + title + "</b>\n" + fedOwnLine(l, f) + "\n" + fedItem(label, fmt.Sprintf("%s [<code>%d</code>]", who, userID))
	})
}

func fedErrText(c *core.Ctx, err error) (string, bool) {
	if fe, ok := err.(*core.FedError); ok {
		return c.T(fe.Tag, fe.Vals), true
	}
	return "", false
}

// person is whoever sent a command; nil (after saying why) when they wrote anonymously.
func person(c *core.Ctx) *gotgbot.User {
	if c.User == nil || c.Msg.SenderChat != nil {
		c.Say(c.T("common.send_this_as_yourself_not_as"))
		return nil
	}
	return c.User
}

// private: owner commands are only taken in a private chat with the bot.
func privateOnly(c *core.Ctx) bool {
	if c.Msg.Chat.Type == "private" {
		return true
	}
	c.Say(c.T("federations.this_command_only_works_in_a"))
	return false
}

func ownFed(c *core.Ctx, private bool) (*gotgbot.User, *core.Federation) {
	if private && !privateOnly(c) {
		return nil, nil
	}
	user := person(c)
	if user == nil {
		return nil, nil
	}
	f, err := c.App.FedOwnedBy(c.Ctx, user.Id)
	if err != nil {
		c.App.ReportError("/"+c.Cmd, err)
		return nil, nil
	}
	if f == nil {
		c.Say(c.T("federations.you_don_t_own_a_federation"))
		return nil, nil
	}
	return user, f
}

// resolveFed is the federation an info command is about: the ID given, else the chat's, else the person's own.
func resolveFed(c *core.Ctx, arg string) *core.Federation {
	arg = strings.TrimSpace(arg)
	if arg != "" {
		if !fedIDRE.MatchString(arg) {
			c.Say(c.T("federations.that_is_not_a_federation_id"))
			return nil
		}
		f, err := c.App.GetFed(c.Ctx, strings.ToLower(arg))
		if err != nil {
			c.App.ReportError("/"+c.Cmd, err)
			return nil
		}
		if f == nil {
			c.Say(c.T("federations.i_can_t_find_a_federation"))
		}
		return f
	}
	if isGroup(c.Msg.Chat.Type) {
		if f, _ := c.App.ChatFed(c.Ctx, c.Msg.Chat.Id); f != nil {
			return f
		}
	}
	if c.User != nil {
		mine, _ := c.App.FedsOf(c.Ctx, c.User.Id)
		switch {
		case len(mine) == 1:
			return mine[0]
		case len(mine) > 1:
			c.Say(c.T("federations.you_are_in_several_federations_give"))
			return nil
		}
	}
	c.Say(c.T("federations.this_chat_is_not_in_a"))
	return nil
}

// fedToBanIn is the federation a fed admin acts in: the chat's, or in private the only one they administer.
func fedToBanIn(c *core.Ctx, user *gotgbot.User) *core.Federation {
	if isGroup(c.Msg.Chat.Type) {
		f, _ := c.App.ChatFed(c.Ctx, c.Msg.Chat.Id)
		if f == nil {
			c.Say(c.T("federations.this_chat_is_not_in_a_2"))
			return nil
		}
		if ok, _ := c.App.IsFedAdmin(c.Ctx, f, user.Id); !ok {
			c.Say(c.T("federations.you_are_not_an_admin_of"))
			return nil
		}
		return f
	}
	mine, _ := c.App.FedsOf(c.Ctx, user.Id)
	switch {
	case len(mine) == 0:
		c.Say(c.T("federations.you_are_not_an_admin_of_2"))
		return nil
	case len(mine) > 1:
		c.Say(c.T("federations.you_administer_several_federations_use_this"))
		return nil
	}
	return mine[0]
}

func fedTarget(c *core.Ctx, allowBots bool) *core.Target {
	t, err := c.ResolveTarget(allowBots, false)
	if err != nil {
		targetError(c, err)
		return nil
	}
	return t
}

func fedBtn(label, data string) gotgbot.InlineKeyboardButton { return core.Button(label, data) }

func onOffWord(c *core.Ctx, on bool) string {
	if on {
		return c.T("common.on")
	}
	return c.T("common.off")
}

// --- Owner commands ---------------------------------------------------------------------

func cmdNewfed(c *core.Ctx) error {
	if !privateOnly(c) {
		return nil
	}
	user := person(c)
	if user == nil {
		return nil
	}
	if strings.TrimSpace(c.Args) == "" {
		c.Say(c.T("federations.use_newfed_lt_name_gt"))
		return nil
	}
	f, err := c.App.CreateFed(c.Ctx, c.Args, user.Id, core.FullName(user))
	if err != nil {
		if msg, ok := fedErrText(c, err); ok {
			c.Say(text.Esc(msg))
			return nil
		}
		return err
	}
	c.Say(c.T("federations.federation_created") +
		strings.Join([]string{fedItem(c.T("federations.name"), codeOf(f.Name)), fedItem(c.T("common.id"), "<code>"+f.ID+"</code>")}, "\n") +
		c.T("federations.join_a_chat_to_it_with"))
	c.DevFedCreated(f, user.Id, core.FullName(user))
	return nil
}

func cmdRenamefed(c *core.Ctx) error {
	_, f := ownFed(c, true)
	if f == nil {
		return nil
	}
	if strings.TrimSpace(c.Args) == "" {
		c.Say(c.T("federations.use_renamefed_lt_name_gt"))
		return nil
	}
	name, err := c.App.RenameFed(c.Ctx, f.ID, c.Args)
	if err != nil {
		if msg, ok := fedErrText(c, err); ok {
			c.Say(text.Esc(msg))
			return nil
		}
		return err
	}
	c.DevFedRenamed(f, name, c.User.Id, core.FullName(c.User))
	c.Say(c.T("federations.your_federation_is_now_called", core.V{"name": text.Esc(name)}))
	return nil
}

func cmdDelfed(c *core.Ctx) error {
	user, f := ownFed(c, true)
	if f == nil {
		return nil
	}
	_, err := c.Reply(fmt.Sprintf("Delete the federation %s? Its chats, admins, bans and subscriptions are forgotten. People it banned stay banned in their chats. This can't be undone.", fedName(f)),
		core.Keyboard([]gotgbot.InlineKeyboardButton{
			fedBtn("Delete", cbFedDelete+"y:"+strconv.FormatInt(user.Id, 10)),
			fedBtn(c.T("common.cancel"), cbFedDelete+"n:"+strconv.FormatInt(user.Id, 10)),
		}))
	return err
}

func cbFedDeleteButton(c *core.Ctx) error {
	parts := strings.Split(c.Cb.Data, ":")
	if c.Msg == nil || len(parts) != 3 {
		c.Answer(c.T("common.this_button_is_no_longer_valid"), true)
		return nil
	}
	if owner, _ := strconv.ParseInt(parts[2], 10, 64); c.User.Id != owner {
		c.Answer(c.T("federations.only_the_owner_of_the_federation"), true)
		return nil
	}
	f, err := c.App.FedOwnedBy(c.Ctx, c.User.Id)
	if err != nil {
		return err
	}
	c.Answer("", false)
	if parts[1] != "y" || f == nil {
		msg := c.T("federations.cancelled_nothing_was_deleted")
		if parts[1] == "y" {
			msg = c.T("federations.you_no_longer_own_a_federation")
		}
		return c.Edit(msg, nil)
	}
	if err := c.App.DeleteFed(c.Ctx, f.ID); err != nil {
		return err
	}
	_ = c.Edit(c.T("federations.the_federation_is_deleted", core.V{"fed": fedName(f)}), nil)
	c.DevFedDeleted(f, c.User.Id, core.FullName(c.User))
	return nil
}

func cmdFedtransfer(c *core.Ctx) error {
	user, f := ownFed(c, true)
	if f == nil {
		return nil
	}
	target := fedTarget(c, false)
	if target == nil {
		return nil
	}
	switch {
	case target.IsChat() || target.Kind == "bot":
		c.Say(c.T("federations.a_federation_can_only_belong_to"))
		return nil
	case target.ID == user.Id:
		c.Say(c.T("federations.you_already_own_it"))
		return nil
	}
	if other, _ := c.App.FedOwnedBy(c.Ctx, target.ID); other != nil {
		c.Say(c.T("common.that_person_already_owns_a_federation"))
		return nil
	}
	data := fmt.Sprintf("%d:%d", target.ID, user.Id)
	_, err := c.Reply(c.T("federations.wants_to_give_you_the_federation", core.V{"user_id": text.UserLink(target.ID, target.Name),
		"id": text.UserLink(user.Id, core.FullName(user)), "fed": fedName(f)}),
		core.Keyboard([]gotgbot.InlineKeyboardButton{
			fedBtn(c.T("federations.accept"), cbFedTransfer+"y:"+data), fedBtn(c.T("common.decline"), cbFedTransfer+"n:"+data)}))
	return err
}

func cbFedTransferButton(c *core.Ctx) error {
	parts := strings.Split(c.Cb.Data, ":")
	if c.Msg == nil || len(parts) != 4 {
		c.Answer(c.T("common.this_button_is_no_longer_valid"), true)
		return nil
	}
	newID, _ := strconv.ParseInt(parts[2], 10, 64)
	oldID, _ := strconv.ParseInt(parts[3], 10, 64)
	giver := c.User.Id == oldID
	if c.User.Id != newID && !(giver && parts[1] != "y") {
		c.Answer(c.T("federations.only_the_person_it_is_offered"), true)
		return nil
	}
	c.Answer("", false)
	f, err := c.App.FedOwnedBy(c.Ctx, oldID)
	if err != nil {
		return err
	}
	if parts[1] != "y" {
		msg := c.T("federations.the_transfer_was_declined")
		if giver {
			msg = c.T("federations.the_transfer_was_cancelled_by_the")
		}
		return c.Edit(msg, nil)
	}
	if f == nil {
		return c.Edit(c.T("federations.that_federation_is_no_longer_theirs"), nil)
	}
	name := core.FullName(c.User)
	if err := c.App.TransferFed(c.Ctx, f.ID, c.User.Id, name); err != nil {
		if msg, ok := fedErrText(c, err); ok {
			return c.Edit(text.Esc(msg), nil)
		}
		return err
	}
	_ = c.Edit(c.T("federations.the_federation_now_belongs_to", core.V{"fed": fedName(f), "id": text.UserLink(c.User.Id, name)}), nil)
	c.DevFedTransferred(f, c.User.Id, name)
	moved := *f
	moved.OwnerID = c.User.Id
	fedEvent(c, &moved, func(l i18n.Lang) string {
		return l.T("federations.new_fedowner", i18n.V{"v": fedOwnLine(l, f),
			"item":  fedItem(l.T("federations.previous_owner"), text.UserRef(f.OwnerID, f.OwnerName)),
			"item2": fedItem(l.T("federations.new_owner"), text.UserRef(c.User.Id, name))})
	})
	return nil
}

func cmdFedpromote(c *core.Ctx) error {
	user, f := ownFed(c, false)
	if f == nil {
		return nil
	}
	target := fedTarget(c, false)
	if target == nil {
		return nil
	}
	if target.IsChat() || target.Kind == "bot" {
		c.Say(c.T("federations.only_people_can_be_federation_admins"))
		return nil
	}
	if ok, _ := c.App.IsFedAdmin(c.Ctx, f, target.ID); ok {
		c.Say(c.T("federations.that_person_already_is_an_admin"))
		return nil
	}
	data := fmt.Sprintf("%d:%s", target.ID, f.ID)
	_, err := c.Reply(c.T("federations.wants_to_make_you_an_admin", core.V{"user_id": text.UserLink(target.ID, target.Name),
		"id": text.UserLink(user.Id, core.FullName(user)), "fed": fedName(f)}),
		core.Keyboard([]gotgbot.InlineKeyboardButton{
			fedBtn(c.T("federations.accept"), cbFedPromote+"y:"+data), fedBtn(c.T("common.decline"), cbFedPromote+"n:"+data)}))
	return err
}

func cbFedPromoteButton(c *core.Ctx) error {
	parts := strings.Split(c.Cb.Data, ":")
	if c.Msg == nil || len(parts) != 4 {
		c.Answer(c.T("common.this_button_is_no_longer_valid"), true)
		return nil
	}
	userID, _ := strconv.ParseInt(parts[2], 10, 64)
	f, err := c.App.GetFed(c.Ctx, parts[3])
	if err != nil {
		return err
	}
	giver := f != nil && c.User.Id == f.OwnerID
	if c.User.Id != userID && !(giver && parts[1] != "y") {
		c.Answer(c.T("federations.only_the_person_it_is_offered"), true)
		return nil
	}
	c.Answer("", false)
	if parts[1] != "y" {
		msg := c.T("federations.the_offer_was_declined")
		if giver {
			msg = c.T("federations.the_offer_was_cancelled_by_the")
		}
		return c.Edit(msg, nil)
	}
	if f == nil {
		return c.Edit(c.T("federations.that_federation_no_longer_exists"), nil)
	}
	name := core.FullName(c.User)
	if err := c.App.AddFedAdmin(c.Ctx, f.ID, c.User.Id, name); err != nil {
		if msg, ok := fedErrText(c, err); ok {
			return c.Edit(text.Esc(msg), nil)
		}
		return err
	}
	who := text.UserLink(c.User.Id, name)
	_ = c.Edit(c.T("federations.is_now_an_admin_of_the", core.V{"who": who, "fed": fedName(f)}), nil)
	fedAdminEvent(c, f, true, c.User.Id, who)
	return nil
}

func cmdFeddemote(c *core.Ctx) error {
	_, f := ownFed(c, false)
	if f == nil {
		return nil
	}
	target := fedTarget(c, false)
	if target == nil {
		return nil
	}
	if removed, err := c.App.RemoveFedAdmin(c.Ctx, f.ID, target.ID); err != nil {
		return err
	} else if !removed {
		c.Say(c.T("federations.that_person_is_not_an_admin"))
		return nil
	}
	who := text.UserLink(target.ID, target.Name)
	c.Say(c.T("federations.is_no_longer_an_admin_of", core.V{"who": who, "fed": fedName(f)}))
	fedAdminEvent(c, f, false, target.ID, who)
	return nil
}

func cmdFeddemoteme(c *core.Ctx) error {
	if !privateOnly(c) {
		return nil
	}
	user := person(c)
	if user == nil {
		return nil
	}
	arg := strings.TrimSpace(c.Args)
	if !fedIDRE.MatchString(arg) {
		c.Say(c.T("federations.use_feddemoteme_lt_fedid_gt"))
		return nil
	}
	f, err := c.App.GetFed(c.Ctx, strings.ToLower(arg))
	if err != nil {
		return err
	}
	switch {
	case f == nil:
		c.Say(c.T("federations.i_can_t_find_a_federation"))
		return nil
	case f.OwnerID == user.Id:
		c.Say(c.T("federations.you_own_that_federation_transfer_it"))
		return nil
	}
	if removed, err := c.App.RemoveFedAdmin(c.Ctx, f.ID, user.Id); err != nil {
		return err
	} else if !removed {
		c.Say(c.T("federations.you_are_not_an_admin_of_3"))
		return nil
	}
	c.Say(c.T("federations.you_are_no_longer_an_admin", core.V{"fed": fedName(f)}))
	fedAdminEvent(c, f, false, user.Id, text.UserLink(user.Id, core.FullName(user)))
	return nil
}

func fedToggle(c *core.Ctx, field, name string, current func(*core.Federation) bool) error {
	_, f := ownFed(c, field == "require_reason")
	if f == nil {
		return nil
	}
	arg := strings.ToLower(strings.TrimSpace(c.Args))
	if arg == "" {
		c.Say(c.T("federations.is_for", core.V{"name": name, "opt": onOffWord(c, current(f)), "fed": fedName(f)}))
		return nil
	}
	on, ok := text.ParseSwitch(arg)
	if !ok {
		c.Say(c.T("federations.use_on_or_off"))
		return nil
	}
	if err := c.App.SetFedFlag(c.Ctx, f.ID, field, on); err != nil {
		return err
	}
	c.Say(c.T("federations.is_now_for", core.V{"name": name, "opt": onOffWord(c, on), "fed": fedName(f)}))
	return nil
}

func cmdFednotif(c *core.Ctx) error {
	return fedToggle(c, "notify", c.T("federations.private_notifications"), func(f *core.Federation) bool { return f.Notify })
}

func cmdFedreason(c *core.Ctx) error {
	return fedToggle(c, "require_reason", c.T("federations.requiring_a_reason"), func(f *core.Federation) bool { return f.RequireReason })
}

func cmdSubfed(c *core.Ctx) error {
	user, f := ownFed(c, false)
	if f == nil {
		return nil
	}
	arg := strings.TrimSpace(c.Args)
	if !fedIDRE.MatchString(arg) {
		c.Say(c.T("federations.use_subfed_lt_fedid_gt"))
		return nil
	}
	other, err := c.App.GetFed(c.Ctx, strings.ToLower(arg))
	if err != nil {
		return err
	}
	if other == nil {
		c.Say(c.T("federations.i_can_t_find_a_federation"))
		return nil
	}
	if err := c.App.SubscribeFed(c.Ctx, f.ID, other.ID); err != nil {
		if msg, ok := fedErrText(c, err); ok {
			c.Say(text.Esc(msg))
			return nil
		}
		return err
	}
	c.Say(c.T("federations.now_inherits_the_bans_of_your", core.V{"fed": fedName(f), "other": fedName(other)}))
	byRef := text.UserRef(user.Id, core.FullName(user))
	fedEvent(c, f, func(l i18n.Lang) string {
		return l.T("federations.new_fedsubscribed", i18n.V{"v": fedOwnLine(l, f), "v2": otherLines(l, "SubbedFed", other, ""),
			"by": fedItem(l.T("federations.by"), byRef)})
	})
	fedEvent(c, other, func(l i18n.Lang) string {
		return l.T("federations.new_fedsubscriber", i18n.V{"v": fedOwnLine(l, other), "v2": otherLines(l, "SubbedFed", f, ""),
			"by": fedItem(l.T("federations.by"), byRef)})
	})
	return nil
}

func cmdUnsubfed(c *core.Ctx) error {
	user, f := ownFed(c, false)
	if f == nil {
		return nil
	}
	arg := strings.TrimSpace(c.Args)
	if !fedIDRE.MatchString(arg) {
		c.Say(c.T("federations.use_unsubfed_lt_fedid_gt"))
		return nil
	}
	id := strings.ToLower(arg)
	other, err := c.App.GetFed(c.Ctx, id)
	if err != nil {
		return err
	}
	if removed, err := c.App.UnsubscribeFed(c.Ctx, f.ID, id); err != nil {
		return err
	} else if !removed {
		c.Say(c.T("federations.your_federation_is_not_subscribed_to"))
		return nil
	}
	name := c.T("federations.that_federation")
	if other != nil {
		name = fedName(other)
	}
	c.Say(c.T("federations.no_longer_inherits_the_bans_of", core.V{"fed": fedName(f), "name": name}))
	byRef := text.UserRef(user.Id, core.FullName(user))
	fedEvent(c, f, func(l i18n.Lang) string {
		return l.T("federations.un_fedsubscribed", i18n.V{"v": fedOwnLine(l, f), "v2": otherLines(l, "UnsubbedFed", other, id),
			"by": fedItem(l.T("federations.by"), byRef)})
	})
	if other != nil {
		fedEvent(c, other, func(l i18n.Lang) string {
			return l.T("federations.un_fedsubscriber", i18n.V{"v": fedOwnLine(l, other), "v2": otherLines(l, "UnsubbedFed", f, ""),
				"by": fedItem(l.T("federations.by"), byRef)})
		})
	}
	return nil
}

func cmdSetfedlog(c *core.Ctx) error {
	user, f := ownFed(c, false)
	if f == nil {
		return nil
	}
	if !isGroup(c.Msg.Chat.Type) {
		c.Say(c.T("federations.send_this_in_the_group_that"))
		return nil
	}
	// Whoever owns a federation must also be an admin here, or the log could be pointed at anyone's chat.
	if !c.IsChatAdmin(c.Msg.Chat.Id, user.Id) {
		c.Say(c.T("federations.only_an_admin_of_this_chat"))
		return nil
	}
	if err := c.App.SetFedLogChat(c.Ctx, f.ID, c.Msg.Chat.Id); err != nil {
		return err
	}
	c.Say(c.T("federations.this_chat_is_now_the_log", core.V{"fed": fedName(f)}))
	return nil
}

func cmdUnsetfedlog(c *core.Ctx) error {
	_, f := ownFed(c, false)
	if f == nil {
		return nil
	}
	if f.LogChatID == 0 {
		c.Say(c.T("federations.your_federation_has_no_log_chat"))
		return nil
	}
	if err := c.App.SetFedLogChat(c.Ctx, f.ID, 0); err != nil {
		return err
	}
	c.Say(c.T("federations.is_no_longer_logged_in_a", core.V{"fed": fedName(f)}))
	return nil
}

// --- Export and import --------------------------------------------------------------------

var (
	fedExportMu sync.Mutex
	fedExported = map[int64]time.Time{}
)

func fedFileRows(bans []*core.FedBan) []core.BanFileRow {
	rows := make([]core.BanFileRow, len(bans))
	for i, b := range bans {
		rows[i] = core.BanFileRow{UserID: b.UserID, Name: b.Name, Reason: b.Reason, BannedBy: b.ByName, CreatedAt: b.BannedAt}
	}
	return rows
}

func cmdFedexport(c *core.Ctx) error {
	user, f := ownFed(c, true)
	if f == nil {
		return nil
	}
	format := strings.ToLower(strings.TrimSpace(c.Args))
	if format == "" {
		format = "csv"
	}
	if !core.ValidBanFormat(format) {
		c.Say(c.T("federations.use_fedexport_csv_minicsv_json_or"))
		return nil
	}
	fedExportMu.Lock()
	if time.Since(fedExported[user.Id]) < fedExportCooldown {
		fedExportMu.Unlock()
		c.Say(c.T("common.wait_a_little_before_exporting_again"))
		return nil
	}
	fedExportMu.Unlock()
	bans, err := c.App.FedBans(c.Ctx, f.ID)
	if err != nil {
		return err
	}
	if len(bans) == 0 {
		c.Say(c.T("federations.your_federation_has_no_bans_to"))
		return nil
	}
	fedExportMu.Lock()
	fedExported[user.Id] = time.Now()
	fedExportMu.Unlock()
	data, ext := core.RenderBanExport(fedFileRows(bans), format)
	return c.SendDocument("fedbans_"+f.ID[:8]+"."+ext, data,
		c.T("federations.bans_of", core.V{"rows": len(bans), "name": text.Esc(f.Name)}))
}

func cmdFedimport(c *core.Ctx) error {
	user, f := ownFed(c, true)
	if f == nil {
		return nil
	}
	overwrite, format := false, ""
	for _, w := range strings.Fields(strings.ToLower(c.Args)) {
		switch {
		case w == "overwrite":
			overwrite = true
		case w == "keep":
		case core.ValidBanFormat(w):
			if format == "" {
				format = w
			}
		default:
			c.Say(c.T("federations.use_fedimport_overwrite_keep_csv_minicsv"))
			return nil
		}
	}
	doc := c.Msg.Document
	if doc == nil {
		if r := core.RepliedMessage(c.Msg); r != nil {
			doc = r.Document
		}
	}
	if doc == nil {
		c.Say(c.T("federations.reply_to_a_file_with_the"))
		return nil
	}
	if doc.FileSize > core.MaxImportBytes {
		c.Say(c.T("federations.that_file_is_too_big_at"))
		return nil
	}
	data, err := c.Download(doc.FileId)
	if err != nil {
		c.Say(c.T("common.i_couldn_t_download_that_file"))
		return nil
	}
	rows, err := core.ParseBanImport(string(data), format)
	if err != nil {
		c.Say(text.Esc(err.Error()))
		return nil
	}
	var status *gotgbot.Message
	if len(rows) > 2000 {
		status, _ = c.ReplyText(c.T("federations.importing_bans_please_wait", core.V{"rows": len(rows)}))
	}
	protected := map[int64]bool{f.OwnerID: true, c.Bot.Id: true}
	admins, _ := c.App.FedAdmins(c.Ctx, f.ID)
	for _, a := range admins {
		protected[a.UserID] = true
	}
	var kept []core.GbanRow
	for _, r := range rows {
		if !protected[r.UserID] && !core.IsImmune(r.UserID) {
			kept = append(kept, r)
		}
	}
	added, err := c.App.ImportFedBans(c.Ctx, f.ID, kept, overwrite, user.Id, core.FullName(user))
	if err != nil {
		return err
	}
	msg := c.T("federations.imported_ban_into", core.V{"added": added, "opt": plural(added), "fed": fedName(f)})
	if skipped := len(rows) - added; skipped > 0 {
		msg += c.T("common.skipped_already_banned_or_protected", core.V{"skipped": skipped})
	}
	if status != nil {
		_ = c.EditSent(status, msg, nil)
	} else {
		c.Say(msg)
	}
	fedEvent(c, f, func(l i18n.Lang) string {
		return l.T("federations.new_fedbans", i18n.V{"v": fedOwnLine(l, f), "item": fedItem(l.T("common.added"), fmt.Sprintf("<code>%d</code>", added))})
	})
	return nil
}

// --- Fed admin commands -------------------------------------------------------------------

// fedProtected says why this target can't be banned in the federation, or "".
func fedProtected(c *core.Ctx, f *core.Federation, t *core.Target) string {
	switch {
	case t.IsChat():
		return c.T("federations.only_people_can_be_banned_in")
	case t.ID == c.Bot.Id:
		return c.T("federations.i_m_not_banning_myself")
	case core.IsImmune(t.ID) || c.App.GetRole(c.Ctx, t.ID) != core.NoRole:
		return c.T("federations.that_account_is_protected_and_can")
	}
	if ok, _ := c.App.IsFedAdmin(c.Ctx, f, t.ID); ok {
		return c.T("federations.that_person_is_an_admin_of")
	}
	return ""
}

type banTextArgs struct {
	title      string
	f          *core.Federation
	adminID    int64
	adminName  string
	targetID   int64
	targetName string
	reason     string
	hasPrev    bool
	prev       string
}

func fedBanText(l i18n.Lang, a banTextArgs) string {
	lines := []string{
		"<b>" + a.title + "</b>",
		fedOwnLine(l, a.f),
		fedItem(l.T("federations.fedadmin"), text.UserRef(a.adminID, a.adminName)),
		fedItem(l.T("common.user"), text.UserRef(a.targetID, a.targetName)),
	}
	if a.hasPrev {
		prev := l.T("federations.none")
		if a.prev != "" {
			prev = codeOf(a.prev)
		}
		lines = append(lines, fedItem(l.T("federations.previous_reason"), prev))
	}
	if a.reason != "" {
		label := l.T("federations.reason")
		if a.hasPrev {
			label = l.T("federations.new_reason")
		}
		lines = append(lines, fedItem(label, codeOf(a.reason)))
	}
	return strings.Join(lines, "\n")
}

// fedBanHere: a fban given in a chat takes effect there at once if the person is in it; in the other chats of the
// federation it does when the person next acts.
func fedBanHere(c *core.Ctx, userID int64) {
	chatID := c.Msg.Chat.Id
	if !isGroup(c.Msg.Chat.Type) {
		return
	}
	if m := c.GetMember(chatID, userID); m == nil || !memberPresent(*m) || c.Spared(chatID, userID) {
		return
	}
	fedBanIn(c, chatID, userID)
}

type pendingReason struct {
	name, reason string
	byID         int64
	byName       string
	at           time.Time
}

var pendingReasons = struct {
	sync.Mutex
	m map[[2]int64]pendingReason
}{m: map[[2]int64]pendingReason{}}

func cmdFban(c *core.Ctx) error {
	deleting := c.Cmd == "dfban"
	user := person(c)
	if user == nil {
		return nil
	}
	f := fedToBanIn(c, user)
	if f == nil {
		return nil
	}
	replied := core.RepliedMessage(c.Msg)
	var target *core.Target
	if deleting {
		if replied == nil {
			c.Say(c.T("common.reply_to_the_message_of_the"))
			return nil
		}
		if target = core.SenderTarget(replied); target == nil {
			c.Say(c.T("common.that_message_was_not_sent_by"))
			return nil
		}
		target.Extra = strings.TrimSpace(c.Args)
	} else if target = fedTarget(c, true); target == nil {
		return nil
	}
	if problem := fedProtected(c, f, target); problem != "" {
		c.Say(problem)
		return nil
	}
	reason := strings.TrimSpace(target.Extra)
	if f.RequireReason && reason == "" {
		if deleting {
			c.Say(c.T("federations.this_federation_requires_a_reason_dfban"))
		} else {
			c.Say(c.T("federations.this_federation_requires_a_reason_fban"))
		}
		return nil
	}
	if deleting {
		_, _ = c.Bot.DeleteMessageWithContext(c.Ctx, c.Msg.Chat.Id, replied.MessageId, nil) // the message may be gone already
	}
	existing, err := c.App.GetFedBan(c.Ctx, f.ID, target.ID)
	if err != nil {
		return err
	}
	if existing != nil {
		who := text.UserLink(target.ID, target.Name)
		head := c.T("federations.user_is_already_fbanned_in", core.V{"who": who, "fed": fedName(f)})
		if reason == "" || reason == existing.Reason {
			fedBanHere(c, target.ID) // they may still be in this chat
			if existing.Reason != "" {
				head += c.T("federations.with_reason", core.V{"reason": text.Esc(existing.Reason)})
			} else {
				head += "."
			}
			c.Say(head)
			return nil
		}
		if existing.Reason != "" {
			head += c.T("federations.with_reason", core.V{"reason": text.Esc(existing.Reason)})
		} else {
			head += c.T("federations.with_no_reason")
		}
		head += c.T("federations.would_you_like_to_update_this", core.V{"reason": text.Esc(reason)})
		data := ":" + f.ID + ":" + strconv.FormatInt(target.ID, 10)
		sent, err := c.Reply(head, core.Keyboard([]gotgbot.InlineKeyboardButton{
			fedBtn("Update reason", cbFedReason+"y"+data), fedBtn(c.T("common.cancel"), cbFedReason+"n"+data)}))
		if err != nil || sent == nil {
			return err
		}
		pendingReasons.Lock()
		for k, v := range pendingReasons.m {
			if time.Since(v.at) > reasonTTL {
				delete(pendingReasons.m, k)
			}
		}
		if len(pendingReasons.m) > 500 {
			pendingReasons.m = map[[2]int64]pendingReason{}
		}
		pendingReasons.m[[2]int64{c.Msg.Chat.Id, sent.MessageId}] = pendingReason{target.Name, reason, user.Id, core.FullName(user), time.Now()}
		pendingReasons.Unlock()
		return nil
	}
	if _, err := c.App.BanInFed(c.Ctx, f.ID, target.ID, target.Name, reason, user.Id, core.FullName(user)); err != nil {
		return err
	}
	fedBanHere(c, target.ID)
	build := func(l i18n.Lang) string {
		return fedBanText(l, banTextArgs{title: l.T("federations.new_fedban"), f: f, adminID: user.Id, adminName: core.FullName(user),
			targetID: target.ID, targetName: target.Name, reason: reason})
	}
	c.Say(build(c.L))
	fedEvent(c, f, build)
	return nil
}

func cbFedReasonButton(c *core.Ctx) error {
	parts := strings.Split(c.Cb.Data, ":")
	if c.Msg == nil || len(parts) != 4 {
		c.Answer(c.T("common.this_button_is_no_longer_valid"), true)
		return nil
	}
	f, err := c.App.GetFed(c.Ctx, parts[2])
	if err != nil {
		return err
	}
	if f == nil {
		c.Answer(c.T("federations.only_the_admins_of_this_federation"), true)
		return nil
	}
	if ok, _ := c.App.IsFedAdmin(c.Ctx, f, c.User.Id); !ok {
		c.Answer(c.T("federations.only_the_admins_of_this_federation"), true)
		return nil
	}
	key := [2]int64{c.Msg.Chat.Id, c.Msg.MessageId}
	pendingReasons.Lock()
	pending, found := pendingReasons.m[key]
	delete(pendingReasons.m, key)
	pendingReasons.Unlock()
	c.Answer("", false)
	if parts[1] != "y" {
		return c.Edit(c.T("federations.the_reason_was_not_changed"), nil)
	}
	if !found || time.Since(pending.at) > reasonTTL {
		return c.Edit(c.T("federations.this_request_has_expired_send_fban"), nil)
	}
	userID, _ := strconv.ParseInt(parts[3], 10, 64)
	old, err := c.App.BanInFed(c.Ctx, f.ID, userID, pending.name, pending.reason, pending.byID, pending.byName)
	if err != nil {
		return err
	}
	prev := ""
	if old != nil {
		prev = old.Reason
	}
	build := func(l i18n.Lang) string {
		return fedBanText(l, banTextArgs{title: l.T("federations.fedban_reason_update"), f: f, adminID: pending.byID, adminName: pending.byName,
			targetID: userID, targetName: pending.name, reason: pending.reason, hasPrev: true, prev: prev})
	}
	_ = c.Edit(build(c.L), nil)
	fedEvent(c, f, build)
	return nil
}

func cmdUnfban(c *core.Ctx) error {
	user := person(c)
	if user == nil {
		return nil
	}
	f := fedToBanIn(c, user)
	if f == nil {
		return nil
	}
	target := fedTarget(c, true)
	if target == nil {
		return nil
	}
	old, err := c.App.UnbanInFed(c.Ctx, f.ID, target.ID)
	if err != nil {
		return err
	}
	if old == nil {
		c.Say(c.T("federations.is_not_banned_in", core.V{"user_id": text.UserLink(target.ID, target.Name), "fed": fedName(f)}))
		return nil
	}
	build := func(l i18n.Lang) string {
		return fedBanText(l, banTextArgs{title: l.T("federations.un_fedban"), f: f, adminID: user.Id, adminName: core.FullName(user),
			targetID: target.ID, targetName: target.Name})
	}
	c.Say(build(c.L))
	fedEvent(c, f, build)
	spawnFedLift(c, f.ID, target.ID)
	return nil
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}(-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$`)

// cmdClearfban is the staff override: remove a (possibly bogus) ban from federations the staff member doesn't own.
func cmdClearfban(c *core.Ctx) error {
	user := person(c)
	if user == nil {
		return nil
	}
	tokens := strings.Fields(c.Args)
	var userPart []string
	if core.RepliedMessage(c.Msg) == nil && len(tokens) > 0 {
		userPart = tokens[:1]
	}
	rest := tokens[len(userPart):]
	fedID := ""
	if len(rest) > 0 && uuidRE.MatchString(rest[0]) {
		fedID, rest = strings.ToLower(rest[0]), rest[1:]
	}
	reason := strings.Join(rest, " ")
	if r := []rune(reason); len(r) > core.MaxFedReason {
		reason = string(r[:core.MaxFedReason])
	}
	if fedID != "" {
		if f, _ := c.App.GetFed(c.Ctx, fedID); f == nil {
			c.Say(c.T("federations.no_federation_has_that_id"))
			return nil
		}
	}
	c.Args = strings.Join(userPart, " ")
	target := fedTarget(c, true)
	if target == nil {
		return nil
	}
	rows, err := c.App.FedsBanning(c.Ctx, target.ID)
	if err != nil {
		return err
	}
	if fedID != "" {
		var kept []*core.FedBan
		for _, r := range rows {
			if r.FedID == fedID {
				kept = append(kept, r)
			}
		}
		rows = kept
	}
	if len(rows) == 0 {
		c.Say(c.T("federations.has_no_federation_ban_to_clear", core.V{"user_id": text.UserLink(target.ID, target.Name)}))
		return nil
	}
	lines := []string{c.T("federations.federation_bans_cleared"), fedItem(c.T("common.user"), text.UserLink(target.ID, target.Name))}
	if reason != "" {
		lines = append(lines, fedItem(c.T("federations.reason"), codeOf(reason)))
	}
	for _, row := range rows {
		if old, err := c.App.UnbanInFed(c.Ctx, row.FedID, target.ID); err != nil {
			return err
		} else if old == nil {
			continue
		}
		f, _ := c.App.GetFed(c.Ctx, row.FedID)
		lines = append(lines, fedItem(c.T("federations.fed"), text.Esc(row.FedName)))
		if f != nil {
			fedEvent(c, f, func(l i18n.Lang) string {
				return fedBanText(l, banTextArgs{title: l.T("federations.un_fedban"), f: f, adminID: user.Id, adminName: core.FullName(user),
					targetID: target.ID, targetName: target.Name, reason: reason}) + "\n\n<i>" + l.T(core.StaffNoteTag) + "</i>"
			})
		}
		spawnFedLift(c, row.FedID, target.ID)
	}
	c.Say(strings.Join(lines, "\n"))
	return nil
}

func cmdMyfeds(c *core.Ctx) error {
	if !privateOnly(c) {
		return nil
	}
	user := person(c)
	if user == nil {
		return nil
	}
	mine, err := c.App.FedsOf(c.Ctx, user.Id)
	if err != nil {
		return err
	}
	if len(mine) == 0 {
		c.Say(c.T("federations.you_are_not_an_admin_of_2"))
		return nil
	}
	lines := []string{c.T("federations.your_federations")}
	for _, f := range mine {
		role := c.T("common.admin")
		if f.OwnerID == user.Id {
			role = c.T("common.owner")
		}
		lines = append(lines, fmt.Sprintf("• <b>%s</b> (%s)\n  <code>%s</code>", text.Esc(f.Name), role, f.ID))
	}
	c.Say(strings.Join(lines, "\n"))
	return nil
}

// --- Everyone's commands ------------------------------------------------------------------

func cmdFedinfo(c *core.Ctx) error {
	var f *core.Federation
	if strings.TrimSpace(c.Args) != "" {
		f = resolveFed(c, c.Args)
	} else {
		// Without an ID: the federation this person owns, whatever chat it is sent in.
		f, _ = c.App.FedOwnedBy(c.Ctx, c.User.Id)
		if f == nil {
			c.Say(c.T("federations.you_don_t_own_a_federation_2"))
		}
	}
	if f == nil {
		return nil
	}
	subs, _ := c.App.FedSubs(c.Ctx, f.ID)
	admins, _ := c.App.FedAdmins(c.Ctx, f.ID)
	banned, _ := c.App.FedBanCount(c.Ctx, f.ID)
	chats, _ := c.App.FedChatCount(c.Ctx, f.ID)
	followers, _ := c.App.FedSubscribers(c.Ctx, f.ID)
	lines := []string{
		c.T("federations.fed_info"),
		fedItem(c.T("federations.fedid"), "<code>"+f.ID+"</code>"),
		fedItem(c.T("federations.name"), text.Esc(f.Name)),
		fedItem(c.T("common.owner"), fmt.Sprintf("%s [<code>%d</code>]", fedOwnerLink(f), f.OwnerID)),
		fedItem(c.T("federations.admins"), fmt.Sprintf("<code>%d</code>", len(admins))),
		fedItem(c.T("federations.banned_users"), fmt.Sprintf("<code>%d</code>", banned)),
		fedItem(c.T("federations.connected_chats"), fmt.Sprintf("<code>%d</code>", chats)),
		fedItem(c.T("federations.subscribers"), fmt.Sprintf("<code>%d</code>", len(followers))),
		"",
	}
	if len(subs) > 0 {
		lines = append(lines, "<b>Subscribed to:</b>")
		for _, id := range subs {
			lines = append(lines, fmt.Sprintf("• %s (<code>%s</code>)", subName(c, id), id))
		}
	} else {
		lines = append(lines, "Not subscribed to any federation.")
	}
	_, err := c.Reply(strings.Join(lines, "\n"), core.Keyboard([]gotgbot.InlineKeyboardButton{
		fedBtn(c.T("federations.button.federation_admins"), cbFedAdmins+f.ID),
	}))
	return err
}

func subName(c *core.Ctx, id string) string {
	if other, _ := c.App.GetFed(c.Ctx, id); other != nil {
		return text.Esc(other.Name)
	}
	return c.T("common.unknown")
}

// fedAdminsText is the owner and the admins of a federation, as /fedadmins and the Federation Admins button of
// /fedinfo show them.
func fedAdminsText(c *core.Ctx, f *core.Federation) string {
	lines := []string{c.T("federations.admins_of", core.V{"name": text.Esc(f.Name)}), fedItem(c.T("common.owner"), fedOwnerLink(f))}
	admins, _ := c.App.FedAdmins(c.Ctx, f.ID)
	for _, a := range admins {
		lines = append(lines, "• "+text.UserLink(a.UserID, a.Name))
	}
	if len(lines) == 2 {
		lines = append(lines, "It has no other admins.")
	}
	return strings.Join(lines, "\n")
}

func cmdFedadmins(c *core.Ctx) error {
	f := resolveFed(c, c.Args)
	if f == nil {
		return nil
	}
	c.Say(fedAdminsText(c, f))
	return nil
}

func cbFedAdminsButton(c *core.Ctx) error {
	if c.Msg == nil {
		c.Answer(c.T("common.this_button_is_no_longer_valid"), true)
		return nil
	}
	f, err := c.App.GetFed(c.Ctx, strings.ToLower(strings.TrimPrefix(c.Cb.Data, cbFedAdmins)))
	if err != nil {
		return err
	}
	if f == nil {
		c.Answer(c.T("federations.that_federation_no_longer_exists"), true)
		return nil
	}
	c.Answer("", false)
	c.Say(fedAdminsText(c, f))
	return nil
}

func cmdFedsubs(c *core.Ctx) error {
	f := resolveFed(c, c.Args)
	if f == nil {
		return nil
	}
	subs, _ := c.App.FedSubs(c.Ctx, f.ID)
	followers, _ := c.App.FedSubscribers(c.Ctx, f.ID)
	lines := []string{c.T("federations.number_of_subscribed_feds", core.V{"followers": len(followers)}), ""}
	if len(subs) == 0 {
		lines = append(lines, fedName(f)+" is not subscribed to any federation.")
	} else {
		lines = append(lines, "Subscribed to the following feds:")
		for _, id := range subs {
			lines = append(lines, fmt.Sprintf("- %s (<code>%s</code>)", subName(c, id), id))
		}
	}
	c.Say(strings.Join(lines, "\n"))
	return nil
}

func cmdChatfed(c *core.Ctx) error {
	if !isGroup(c.Msg.Chat.Type) {
		c.Say(c.T("common.this_command_only_works_in_groups"))
		return nil
	}
	f, err := c.App.ChatFed(c.Ctx, c.Msg.Chat.Id)
	if err != nil {
		return err
	}
	if f == nil {
		c.Say(c.T("federations.this_chat_is_not_in_a_3"))
		return nil
	}
	c.Say(strings.Join([]string{c.T("federations.chat_federation"), fedItem(c.T("federations.name"), codeOf(f.Name)),
		fedItem(c.T("common.id"), "<code>"+f.ID+"</code>"), fedItem(c.T("common.owner"), fedOwnerLink(f))}, "\n"))
	return nil
}

func chatEventLines(l i18n.Lang, chat gotgbot.Chat, byRef string) (string, string, string) {
	return fedItem(l.T("federations.chat"), core.ChatLink(chat.Title, chat.Username, chat.Id)),
		fedItem(l.T("common.chat_id"), fmt.Sprintf("<code>%d</code>", chat.Id)),
		fedItem(l.T("federations.by"), byRef)
}

func cmdJoinfed(c *core.Ctx) error {
	if !c.RequireChatOwner() {
		return nil
	}
	arg := strings.TrimSpace(c.Args)
	if !fedIDRE.MatchString(arg) {
		c.Say(c.T("federations.use_joinfed_lt_fedid_gt"))
		return nil
	}
	chat := c.Msg.Chat
	if current, _ := c.App.ChatFed(c.Ctx, chat.Id); current != nil {
		c.Say(c.T("federations.this_chat_is_already_in_leave", core.V{"current": fedName(current)}))
		return nil
	}
	f, err := c.App.GetFed(c.Ctx, strings.ToLower(arg))
	if err != nil {
		return err
	}
	if f == nil {
		c.Say(c.T("federations.i_can_t_find_a_federation"))
		return nil
	}
	if err := c.App.JoinFed(c.Ctx, chat.Id, f.ID); err != nil {
		return err
	}
	c.Say(c.T("federations.this_chat_joined_the_federation_people", core.V{"fed": fedName(f)}))
	byRef := text.UserRef(c.User.Id, core.FullName(c.User))
	fedEvent(c, f, func(l i18n.Lang) string {
		a, b, d := chatEventLines(l, chat, byRef)
		return l.T("federations.new_fedchat", i18n.V{"v": fedOwnLine(l, f), "item": a, "item2": b, "item3": d})
	})
	return nil
}

func cmdLeavefed(c *core.Ctx) error {
	if !c.RequireChatOwner() {
		return nil
	}
	chat := c.Msg.Chat
	f, err := c.App.ChatFed(c.Ctx, chat.Id)
	if err != nil {
		return err
	}
	if f == nil {
		c.Say(c.T("federations.this_chat_is_not_in_a_3"))
		return nil
	}
	if err := c.App.LeaveFed(c.Ctx, chat.Id); err != nil {
		return err
	}
	c.Say(c.T("federations.this_chat_left_the_federation_nobody", core.V{"fed": fedName(f)}))
	byRef := text.UserRef(c.User.Id, core.FullName(c.User))
	fedEvent(c, f, func(l i18n.Lang) string {
		a, b, d := chatEventLines(l, chat, byRef)
		return l.T("federations.un_fedchat", i18n.V{"v": fedOwnLine(l, f), "item": a, "item2": b, "item3": d})
	})
	return nil
}

func cmdQuietfed(c *core.Ctx) error {
	if !isGroup(c.Msg.Chat.Type) {
		c.Say(c.T("common.this_command_only_works_in_groups"))
		return nil
	}
	arg := strings.ToLower(strings.TrimSpace(c.Args))
	var on bool
	if arg != "" {
		var ok bool
		if on, ok = text.ParseSwitch(arg); !ok {
			c.Say(c.T("federations.use_quietfed_on_or_quietfed_off"))
			return nil
		}
	}
	right := rightLookChat
	if arg != "" {
		right = rightChangeInfoFed
	}
	if c.BeginChatCommand(core.AuthorizeOpts{Right: right}) == nil {
		return nil
	}
	chatID := c.Msg.Chat.Id
	if f, _ := c.App.ChatFed(c.Ctx, chatID); f == nil {
		c.Say(c.T("federations.this_chat_is_not_in_a_3"))
		return nil
	}
	if arg == "" {
		quiet, _ := c.App.FedQuiet(c.Ctx, chatID)
		do := c.T("federations.announce")
		if quiet {
			do = c.T("federations.do_not_announce")
		}
		c.Say(c.T("federations.quiet_mode_is_i_removals_of", core.V{"opt": onOffWord(c, quiet), "opt2": do}))
		return nil
	}
	if err := c.App.SetFedQuiet(c.Ctx, chatID, on); err != nil {
		return err
	}
	c.Say(c.T("federations.quiet_mode_is_now", core.V{"opt": onOffWord(c, on)}))
	return nil
}

func cmdFedstat(c *core.Ctx) error {
	words := strings.Fields(c.Args)
	fedID := ""
	if n := len(words); n > 0 && fedIDRE.MatchString(words[n-1]) {
		fedID, words = strings.ToLower(words[n-1]), words[:n-1]
	}
	var userID int64
	var name string
	mine := false
	if len(words) > 0 || (core.RepliedMessage(c.Msg) != nil && fedID == "") {
		c.Args = strings.Join(words, " ")
		target := fedTarget(c, true)
		if target == nil {
			return nil
		}
		userID, name = target.ID, target.Name
	} else {
		if c.User == nil {
			return nil
		}
		userID, name, mine = c.User.Id, core.FullName(c.User), true
	}
	who := c.T("federations.you_are")
	if !mine {
		who = c.T("federations.is", core.V{"user_id": text.UserLink(userID, name)})
	}
	if fedID != "" {
		f, err := c.App.GetFed(c.Ctx, fedID)
		if err != nil {
			return err
		}
		if f == nil {
			c.Say(c.T("federations.i_can_t_find_a_federation"))
			return nil
		}
		ban, err := c.App.GetFedBan(c.Ctx, fedID, userID)
		if err != nil {
			return err
		}
		if ban == nil {
			c.Say(c.T("federations.not_banned_in", core.V{"who": who, "fed": fedName(f)}))
			return nil
		}
		reason, by := "none", "unknown"
		if ban.Reason != "" {
			reason = ban.Reason
		}
		if ban.ByName != "" {
			by = ban.ByName
		}
		c.Say(strings.Join([]string{c.T("federations.banned_in", core.V{"who": who, "fed": fedName(f)}),
			fedItem(c.T("federations.reason"), codeOf(reason)), fedItem(c.T("federations.banned_by"), text.Esc(by)),
			fedItem(c.T("federations.date"), "<code>"+ban.BannedAt.UTC().Format("2006-01-02 15:04:05")+"</code>")}, "\n"))
		return nil
	}
	found, err := c.App.FedsBanning(c.Ctx, userID)
	if err != nil {
		return err
	}
	if len(found) == 0 {
		c.Say(c.T("federations.not_banned_in_any_federation", core.V{"who": who}))
		return nil
	}
	head := c.T("federations.banned_in_federation", core.V{"who": who, "found": len(found), "opt": plural(len(found))})
	tail := c.T("federations.for_the_reason_use_fedstat_lt")
	if mine {
		tail = c.T("federations.for_the_reason_use_fedstat_lt_2")
	}
	var body []string
	for _, r := range found {
		body = append(body, fmt.Sprintf("• <b>%s</b>\n  <code>%s</code>", text.Esc(r.FedName), r.FedID))
	}
	msg := head + "\n" + strings.Join(body, "\n") + tail
	if len([]rune(msg)) <= maxListChars {
		c.Say(msg)
		return nil
	}
	// Too long for one message: the whole list goes in a file.
	var lines []string
	for _, r := range found {
		lines = append(lines, r.FedName+"\n  "+r.FedID)
	}
	return c.SendDocument(fmt.Sprintf("FedBans_%d.txt", userID), []byte(strings.Join(lines, "\n")+"\n"),
		c.T("federations.banned_in_federations_the_list_is", core.V{"who": who, "found": len(found)}))
}

// --- Enforcement --------------------------------------------------------------------------

// fedBanIn bans in one chat. False when Telegram refuses (usually: the bot is not an admin there).
func fedBanIn(c *core.Ctx, chatID, userID int64) bool {
	for attempt := 1; attempt <= 2; attempt++ {
		_, err := c.Bot.BanChatMemberWithContext(c.Ctx, chatID, userID, nil)
		if err == nil {
			_ = c.App.RecordFedApplied(c.Ctx, chatID, userID)
			return true
		}
		if wait := retryAfter(err); wait > 0 && attempt == 1 {
			sleepFor(wait)
			continue
		}
		slog.Debug("Could not fedban", "user", userID, "chat", chatID, "err", err)
		return false
	}
	return false
}

var fedTasks sync.WaitGroup

func spawnFedLift(c *core.Ctx, fedID string, userID int64) {
	fedTasks.Add(1)
	go func() {
		defer fedTasks.Done()
		liftFedBan(c, fedID, userID)
	}()
}

// liftFedBan unbans where the bot banned because of this federation, unless another federation still bans them there.
func liftFedBan(c *core.Ctx, fedID string, userID int64) {
	ctx := context.Background()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("Lifting a federation ban failed", "panic", r)
		}
	}()
	reachIDs, err := c.App.FedReach(ctx, fedID)
	if err != nil {
		slog.Error("Lifting a federation ban failed", "err", err)
		return
	}
	reach, err := c.App.FedChats(ctx, reachIDs...)
	if err != nil {
		slog.Error("Lifting a federation ban failed", "err", err)
		return
	}
	inReach := map[int64]bool{}
	for _, id := range reach {
		inReach[id] = true
	}
	// Every chat the ban reached, not only those where the bot remembers banning them.
	applied, _ := c.App.FedAppliedIn(ctx, userID)
	targets := append([]int64{}, applied...)
	seen := map[int64]bool{}
	for _, id := range applied {
		seen[id] = true
	}
	for _, id := range reach {
		if !seen[id] {
			targets = append(targets, id)
		}
	}
	for _, id := range targets {
		if !inReach[id] {
			continue
		}
		if _, ban, err := c.App.EffectiveFedBan(ctx, id, userID); err != nil || ban != nil {
			continue
		}
		_, _ = c.Bot.UnbanChatMemberWithContext(ctx, id, userID, &gotgbot.UnbanChatMemberOpts{OnlyIfBanned: true})
		_ = c.App.ForgetFedApplied(ctx, id, userID)
		time.Sleep(unbanPause)
	}
}

// enforceFed removes them and says so. False when that failed (the bot probably can't ban here).
func enforceFed(c *core.Ctx, chatID int64, user *gotgbot.User, f *core.Federation, ban *core.FedBan, thread int64) bool {
	if !firstRecently(chatID, user.Id) {
		return true // another update of theirs is dealing with it
	}
	if !fedBanIn(c, chatID, user.Id) {
		forgetRecent(chatID, user.Id) // try again with their next message
		leaveIfPowerless(c, chatID)
		return false
	}
	if quiet, _ := c.App.FedQuiet(c.Ctx, chatID); quiet {
		return true
	}
	lines := []string{c.T("federations.is_banned_in_this_chat_s", core.V{"id": text.UserRef(user.Id, core.FullName(user)), "name": text.Esc(f.Name)})}
	if ban.Reason != "" {
		lines = append(lines, c.T("common.reason_label")+": "+codeOf(ban.Reason))
	}
	notifyChat(c.Ctx, c.Bot, place{chat: chatID, thread: thread}, strings.Join(lines, "\n"))
	return true
}

func fedBanOf(c *core.Ctx, chatID int64, user *gotgbot.User) (*core.Federation, *core.FedBan) {
	f, ban, err := c.App.EffectiveFedBan(c.Ctx, chatID, user.Id)
	if err != nil || ban == nil || c.Spared(chatID, user.Id) {
		return nil, nil
	}
	return f, ban
}

func watchFederations(c *core.Ctx) (bool, error) {
	m := c.Msg
	if m == nil || c.Cb != nil || !isGroup(m.Chat.Type) {
		return false, nil
	}
	chatID := m.Chat.Id
	if len(m.NewChatMembers) > 0 {
		removed := 0
		for i := range m.NewChatMembers {
			u := &m.NewChatMembers[i]
			if u.Id == c.Bot.Id {
				continue
			}
			if f, ban := fedBanOf(c, chatID, u); ban != nil && enforceFed(c, chatID, u, f, ban, threadOf(m)) {
				removed++
			}
		}
		if removed > 0 && removed == len(m.NewChatMembers) {
			return true, nil
		}
		return false, nil
	}
	if c.User == nil || m.SenderChat != nil || m.LeftChatMember != nil {
		return false, nil
	}
	f, ban := fedBanOf(c, chatID, c.User)
	if ban == nil {
		return false, nil
	}
	_, _ = c.Bot.DeleteMessageWithContext(c.Ctx, chatID, m.MessageId, nil)
	enforceFed(c, chatID, c.User, f, ban, threadOf(m))
	return true, nil
}

func watchFedMember(c *core.Ctx, e *gotgbot.ChatMemberUpdated) (bool, error) {
	if !isGroup(e.Chat.Type) {
		return false, nil
	}
	before, after := e.OldChatMember.MergeChatMember(), e.NewChatMember.MergeChatMember()
	user := after.User
	moved := (after.Status == "member" && before.Status != "member") || (after.Status == "left" && before.Status == "member")
	if user.Id == c.Bot.Id || !moved {
		return false, nil
	}
	f, ban := fedBanOf(c, e.Chat.Id, &user)
	if ban == nil {
		return false, nil
	}
	enforceFed(c, e.Chat.Id, &user, f, ban, 0)
	return true, nil
}

// watchFedReaction removes a person banned in the chat's federation who reacts to a message in a group.
func watchFedReaction(c *core.Ctx, e *gotgbot.MessageReactionUpdated) (bool, error) {
	user := e.User
	if user == nil || !isGroup(e.Chat.Type) || user.Id == c.Bot.Id {
		return false, nil
	}
	f, ban := fedBanOf(c, e.Chat.Id, user)
	if ban == nil {
		return false, nil
	}
	enforceFed(c, e.Chat.Id, user, f, ban, 0)
	return true, nil
}

// --- Leaving the federation when I can no longer ban ------------------------------------------

// canBan: this membership lets the bot remove people: the owner, or an admin with that right.
func canBan(m gotgbot.MergedChatMember) bool {
	return m.Status == "creator" || (m.Status == "administrator" && m.CanRestrictMembers)
}

// leaveFederation takes the chat out of its federation because I can't act for it. False when it was in none.
func leaveFederation(c *core.Ctx, chatID int64, title, username string) bool {
	f, err := c.App.ChatFed(c.Ctx, chatID)
	if err != nil || f == nil {
		return false
	}
	if err := c.App.LeaveFed(c.Ctx, chatID); err != nil {
		slog.Error("Could not leave a federation", "chat", chatID, "err", err)
		return false
	}
	if title == "" {
		if found, err := c.Bot.GetChatWithContext(c.Ctx, chatID, nil); err == nil {
			title, username = found.Title, found.Username
		}
	}
	l := c.App.UseChat(c.Ctx, chatID)
	_ = c.SayTo(chatID, l.T("federations.i_can_t_remove_people_from", i18n.V{"fed": fedName(f)}))
	fedEvent(c, f, func(l i18n.Lang) string {
		return l.T("federations.un_fedchat", i18n.V{"v": fedOwnLine(l, f),
			"item":  fedItem(l.T("federations.chat"), core.ChatLink(title, username, chatID)),
			"item2": fedItem(l.T("common.chat_id"), fmt.Sprintf("<code>%d</code>", chatID)),
			"item3": fedItem(l.T("federations.reason"), l.T("federations.i_lost_the_right_to_ban"))})
	})
	return true
}

// leaveIfPowerless: a ban just failed; if that is because I lost my rights here, the chat leaves its federation.
func leaveIfPowerless(c *core.Ctx, chatID int64) {
	m := c.GetMember(chatID, c.Bot.Id)
	if m == nil || canBan(*m) {
		return // can't tell, or the failure was something else (flood limit, an admin, ...)
	}
	leaveFederation(c, chatID, "", "")
}

func watchFedRights(c *core.Ctx, e *gotgbot.ChatMemberUpdated) (bool, error) {
	now := e.NewChatMember.MergeChatMember()
	if !isGroup(e.Chat.Type) || now.User.Id != c.Bot.Id || canBan(now) {
		return false, nil
	}
	leaveFederation(c, e.Chat.Id, e.Chat.Title, e.Chat.Username)
	return false, nil
}

// --- /info --------------------------------------------------------------------------------

// fedInfoNotes is what /info says about a person in a chat that belongs to a federation.
func fedInfoNotes(c *core.Ctx, userID, chatID int64) []string {
	if chatID == 0 {
		return nil
	}
	f, _ := c.App.ChatFed(c.Ctx, chatID)
	if f == nil {
		return nil
	}
	if ok, _ := c.App.IsFedAdmin(c.Ctx, f, userID); ok {
		return []string{c.T("federations.this_user_is_a_fed_admin", core.V{"fed": fedName(f)})}
	}
	if c.App.GetRole(c.Ctx, userID) != core.NoRole || c.IsChatAdmin(chatID, userID) {
		return nil // staff and admins are not touched by federation bans: nothing to say
	}
	banned, ban, _ := c.App.EffectiveFedBan(c.Ctx, chatID, userID)
	if ban == nil {
		return []string{c.T("federations.this_user_is_not_fbanned_in")}
	}
	out := c.T("federations.this_user_has_been_fbanned_in", core.V{"banned_in": fedName(banned)})
	if ban.Reason != "" {
		out += c.T("common.reason", core.V{"reason": text.Esc(ban.Reason)})
	}
	return []string{out}
}
