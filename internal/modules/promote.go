package modules

import (
	"errors"
	"strings"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/core"
	"argo/internal/text"
)

// Promote, demote, titles and tags (modules/promote.py).

const maxTitle = 16 // Telegram's limit for titles and tags

// Admin rights this module hands out, with the names shown to people (in the order Python lists them).
var rightNames = []struct{ right, tag string }{
	{"can_manage_chat", "promote.right_names.can_manage_chat"},
	{"can_change_info", "promote.right_names.can_change_info"},
	{"can_delete_messages", "promote.right_names.can_delete_messages"},
	{"can_invite_users", "common.invite_users"},
	{"can_restrict_members", "promote.right_names.can_restrict_members"},
	{"can_pin_messages", "promote.right_names.can_pin_messages"},
	{"can_manage_video_chats", "promote.right_names.can_manage_video"},
	{"can_manage_topics", "promote.right_names.can_manage_topics"},
	{"can_manage_tags", "promote.right_names.can_manage_tags"},
	{"can_promote_members", "promote.right_names.can_promote_members"},
}

func registerPromote(r *core.Router) {
	r.Command(func(c *core.Ctx) error { return cmdPromote(c, false) }, "promote")
	r.Command(func(c *core.Ctx) error { return cmdPromote(c, true) }, "fullPromote", "fullpromote")
	r.Command(cmdDemote, "demote")
	r.Command(cmdTitle, "title")
	r.Command(cmdTag, "tag")
}

// member is a chat member as the promotion commands see it. The bot's staff counts as somebody holding every right.
type member struct {
	m     gotgbot.MergedChatMember
	staff bool
}

var staffMember = &member{m: gotgbot.MergedChatMember{Status: "creator"}, staff: true}

func (m *member) status() string { return m.m.Status }

// has tells whether the member holds an admin right (the creator holds all of them).
func (m *member) has(right string) bool {
	if m.m.Status == "creator" {
		return true
	}
	switch right {
	case "can_manage_chat":
		return m.m.CanManageChat
	case "can_change_info":
		return m.m.CanChangeInfo
	case "can_delete_messages":
		return m.m.CanDeleteMessages
	case "can_invite_users":
		return m.m.CanInviteUsers
	case "can_restrict_members":
		return m.m.CanRestrictMembers
	case "can_pin_messages":
		return m.m.CanPinMessages
	case "can_manage_video_chats":
		return m.m.CanManageVideoChats
	case "can_manage_topics":
		return m.m.CanManageTopics
	case "can_manage_tags":
		return m.m.CanManageTags
	case "can_promote_members":
		return m.m.CanPromoteMembers
	}
	return false
}

func refuse(msg string) error     { return &core.ModerationError{Msg: msg} }
func notAllowed(msg string) error { return &core.ModerationError{Msg: msg, NotAllowed: true} }

// finish ends a command: refusals are answered, anything else is a real failure.
func finish(c *core.Ctx, err error) error {
	if err == nil {
		return nil
	}
	var me *core.ModerationError
	var te *core.TargetError
	if errors.As(err, &me) || errors.As(err, &te) {
		c.Deny(err)
		return nil
	}
	return err
}

func tgProblem(err error) (code int, problem string) {
	tg := core.TgError(err)
	if tg == nil {
		return 0, ""
	}
	return tg.Code, strings.ToLower(tg.Description)
}

func hasAny(s string, keys ...string) bool {
	for _, k := range keys {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// promotionCall reads the answer of a promotion call. Unlike Ctx.Perform, "admin required" is not read as "I lack
// the rights" (I checked those): it means the admin was not promoted by me.
func promotionCall(c *core.Ctx, err error) error {
	code, p := tgProblem(err)
	switch {
	case err == nil:
		return nil
	case code == 403:
		return refuse(c.T("moderation.rights_message"))
	case code == 400 && hasAny(p, "user not found", "participant_id_invalid", "user_not_participant"):
		return refuse(c.T("common.that_user_isn_t_in_this"))
	}
	return err
}

func getMember(c *core.Ctx, userID int64) (*member, error) {
	m := c.GetMember(c.Msg.Chat.Id, userID)
	if m == nil {
		return nil, refuse(c.T("promote.i_couldn_t_check_that_user"))
	}
	return &member{m: *m}, nil
}

// staffMayHelp: the person acts as the bot's staff (Sudo and up) while the chat's owner has not turned that off.
func staffMayHelp(c *core.Ctx) bool {
	return c.Role >= core.StaffRole && c.App.Setting(c.Ctx, "staff_help", c.Msg.Chat.Id)
}

// actorMember is the person's membership when they hold the right, the staff stand-in when they act as the bot's
// staff, else whatever they are (possibly nil) for the caller to refuse.
func actorMember(c *core.Ctx, right string) *member {
	m, err := getMember(c, c.User.Id)
	if err != nil {
		m = nil
	}
	if m != nil && m.has(right) {
		return m
	}
	if staffMayHelp(c) {
		return staffMember
	}
	return m
}

// beginPromote is the common start: groups only, not anonymous, and the person must be the owner of the chat, an
// admin allowed to add admins, or one of the bot's staff.
func beginPromote(c *core.Ctx) (*member, *core.Actor, error) {
	if !core.IsGroup(c.Msg.Chat) {
		return nil, nil, refuse(c.T("common.this_command_only_works_in_groups"))
	}
	actor := c.ActorOf()
	if actor.Anonymous {
		return nil, nil, notAllowed(c.T("common.send_this_command_as_yourself_not"))
	}
	m := actorMember(c, "can_promote_members")
	if m == nil {
		return nil, nil, refuse(c.T("common.i_couldn_t_check_your_rights"))
	}
	if !m.has("can_promote_members") {
		return nil, nil, notAllowed(c.T("promote.you_need_the_right_to_add"))
	}
	actor.Staff = m.staff
	return m, actor, nil
}

func botMember(c *core.Ctx, needed ...string) (*member, error) {
	m, err := getMember(c, c.Bot.Id)
	if err != nil {
		return nil, err
	}
	for _, right := range needed {
		if !m.has(right) {
			tag := right
			for _, rn := range rightNames {
				if rn.right == right {
					tag = rn.tag
				}
			}
			return nil, refuse(c.T("promote.i_need_the_admin_right_to", core.V{"get": c.T(tag)}))
		}
	}
	return m, nil
}

// promoteTarget is the user a command is aimed at. With selfFallback, a command given with text but no user (no
// reply, no @username or id) is aimed at the person writing it: /title Boss.
func promoteTarget(c *core.Ctx, selfFallback bool) (*core.Target, error) {
	t, err := c.ResolveTarget(true, false)
	if err == nil {
		return t, nil
	}
	var te *core.TargetError
	if !errors.As(err, &te) {
		return nil, err
	}
	args := strings.TrimSpace(c.Args)
	first := ""
	if f := strings.Split(args, " "); len(f) > 0 {
		first = strings.TrimLeft(f[0], "-")
	}
	looksLikeUser := strings.HasPrefix(args, "@") || (first != "" && strings.Trim(first, "0123456789") == "")
	noReply := core.RepliedMessage(c.Msg) == nil
	if selfFallback && noReply && (!looksLikeUser || args == "") {
		return &core.Target{ID: c.User.Id, Name: core.FullName(c.User), Extra: args, Kind: "user"}, nil
	}
	return nil, err
}

func rightsText(c *core.Ctx, rights []string) string {
	var names []string
	for _, r := range rights {
		for _, rn := range rightNames {
			if rn.right == r {
				names = append(names, c.T(rn.tag))
			}
		}
	}
	if len(names) == 0 {
		return c.T("promote.none")
	}
	return strings.Join(names, ", ")
}

func promotionProblem(c *core.Ctx, err error) (string, bool) {
	code, p := tgProblem(err)
	if code != 400 {
		return "", false
	}
	switch {
	case hasAny(p, "administrator", "admin_rank", "chat_admin_required"):
		return c.T("promote.i_can_only_change_admins_that"), true
	case strings.Contains(p, "bot") && strings.Contains(p, "can't"):
		return c.T("promote.telegram_doesn_t_allow_that_for"), true
	}
	return "", false
}

func cmdPromote(c *core.Ctx, full bool) error {
	actorM, actor, err := beginPromote(c)
	if err != nil {
		return finish(c, err)
	}
	chatID := c.Msg.Chat.Id
	target, err := promoteTarget(c, false)
	if err != nil {
		return finish(c, err)
	}
	title := strings.TrimSpace(target.Extra)
	if len([]rune(title)) > maxTitle {
		return finish(c, refuse(c.T("promote.a_title_can_have_at_most", core.V{"MAX_LENGTH": maxTitle})))
	}
	if err := c.CheckTargetSelf(c.Msg.Chat, target, actor, false, true); err != nil {
		return finish(c, err)
	}
	botM, err := botMember(c, "can_promote_members")
	if err != nil {
		return finish(c, err)
	}
	targetM, err := getMember(c, target.ID)
	if err != nil {
		return finish(c, err)
	}
	status := targetM.status()
	if status == "creator" {
		return finish(c, refuse(c.T("promote.i_can_t_change_the_owner")))
	}
	if status == "left" || status == "kicked" || (status == "restricted" && !targetM.m.IsMember) {
		return finish(c, refuse(c.T("common.that_user_isn_t_in_this")))
	}

	// Nobody can hand out more than they hold themselves, and neither can I.
	var rights []string
	for _, rn := range rightNames {
		if !full && rn.right == "can_promote_members" {
			continue
		}
		if botM.has(rn.right) && actorM.has(rn.right) {
			rights = append(rights, rn.right)
		}
	}
	if len(rights) == 0 {
		return finish(c, refuse(c.T("promote.you_and_i_have_no_admin")))
	}
	opts := &gotgbot.PromoteChatMemberOpts{}
	f := false
	opts.CanRestrictMembers = &f
	for _, r := range rights {
		switch r {
		case "can_manage_chat":
			opts.CanManageChat = true
		case "can_change_info":
			opts.CanChangeInfo = true
		case "can_delete_messages":
			opts.CanDeleteMessages = true
		case "can_invite_users":
			opts.CanInviteUsers = true
		case "can_restrict_members":
			t := true
			opts.CanRestrictMembers = &t
		case "can_pin_messages":
			opts.CanPinMessages = true
		case "can_manage_video_chats":
			opts.CanManageVideoChats = true
		case "can_manage_topics":
			opts.CanManageTopics = true
		case "can_manage_tags":
			opts.CanManageTags = true
		case "can_promote_members":
			opts.CanPromoteMembers = true
		}
	}
	_, err = c.Bot.PromoteChatMemberWithContext(c.Ctx, chatID, target.ID, opts)
	if err = promotionCall(c, err); err != nil {
		if msg, ok := promotionProblem(c, err); ok {
			c.Say(msg)
			return nil
		}
		return finish(c, err)
	}

	// /promote @user Guy: the rest of the line is the new admin's title.
	titleLine := ""
	if title != "" {
		_, err := c.Bot.SetChatAdministratorCustomTitleWithContext(c.Ctx, chatID, target.ID, title, nil)
		if err = promotionCall(c, err); err != nil {
			_, p := tgProblem(err)
			var me *core.ModerationError
			if code, _ := tgProblem(err); code != 400 && !errors.As(err, &me) {
				return err
			}
			reason := c.T("promote.i_couldn_t_set_the_title")
			if strings.Contains(p, "emoji") {
				reason = c.T("promote.a_title_can_t_contain_emoji")
			}
			titleLine = c.T("promote.title_not_set", core.V{"reason": reason})
		} else {
			titleLine = c.T("promote.title", core.V{"title": text.Esc(title)})
		}
	}

	kind := c.T("promote.to_admin")
	if full {
		kind = c.T("promote.to_admin_with_full_rights")
	}
	details := []core.Detail{valueDetail("promote.rights", text.Esc(rightsText(c, rights)))}
	if title != "" {
		details = append(details, valueDetail("common.title", text.Esc(title)))
	}
	c.LogAction("PROMOTED", actor, target, "", details...)
	_, err = c.ReplyText(c.T("promote.was_promoted", core.V{
		"user_id": text.UserLink(target.ID, target.Name), "kind": kind, "by": c.By(actor),
	}) + titleLine + c.StaffNote(actor))
	return err
}

func cmdDemote(c *core.Ctx) error {
	_, actor, err := beginPromote(c)
	if err != nil {
		return finish(c, err)
	}
	target, err := promoteTarget(c, false)
	if err != nil {
		return finish(c, err)
	}
	if err := c.CheckTargetSelf(c.Msg.Chat, target, actor, false, true); err != nil {
		return finish(c, err)
	}
	if _, err := botMember(c, "can_promote_members"); err != nil {
		return finish(c, err)
	}
	targetM, err := getMember(c, target.ID)
	if err != nil {
		return finish(c, err)
	}
	switch targetM.status() {
	case "creator":
		return finish(c, refuse(c.T("promote.i_can_t_demote_the_owner")))
	case "administrator":
	default:
		return finish(c, refuse(c.T("promote.that_user_isn_t_an_admin")))
	}
	f := false
	// Everything Telegram has to be told "no" about for a demotion to take: only the rights that are sent as a
	// pointer can be said explicitly, the others are false when left out.
	_, err = c.Bot.PromoteChatMemberWithContext(c.Ctx, c.Msg.Chat.Id, target.ID, &gotgbot.PromoteChatMemberOpts{CanRestrictMembers: &f})
	if err = promotionCall(c, err); err != nil {
		if msg, ok := promotionProblem(c, err); ok {
			c.Say(msg)
			return nil
		}
		return finish(c, err)
	}
	c.LogAction("DEMOTED", actor, target, "")
	_, err = c.ReplyText(c.T("promote.was_demoted", core.V{
		"user_id": text.UserLink(target.ID, target.Name), "by": c.By(actor),
	}) + c.StaffNote(actor))
	return err
}

func cmdTitle(c *core.Ctx) error {
	actor := c.ActorOf()
	if !core.IsGroup(c.Msg.Chat) {
		return finish(c, refuse(c.T("common.this_command_only_works_in_groups")))
	}
	if actor.Anonymous {
		return finish(c, notAllowed(c.T("common.send_this_command_as_yourself_not")))
	}
	target, err := promoteTarget(c, true)
	if err != nil {
		return finish(c, err)
	}
	title := strings.TrimSpace(target.Extra)
	if len([]rune(title)) > maxTitle {
		return finish(c, refuse(c.T("promote.a_title_can_have_at_most", core.V{"MAX_LENGTH": maxTitle})))
	}
	if target.ID == actor.ID {
		me, err := getMember(c, actor.ID)
		if err != nil {
			return finish(c, err)
		}
		if s := me.status(); s != "administrator" && s != "creator" {
			return finish(c, notAllowed(c.T("promote.only_admins_can_have_a_title")))
		}
	} else {
		am := actorMember(c, "can_promote_members")
		if am == nil {
			return finish(c, refuse(c.T("common.i_couldn_t_check_your_rights")))
		}
		if err := c.CheckTargetSelf(c.Msg.Chat, target, actor, false, true); err != nil {
			return finish(c, err)
		}
		if !am.has("can_promote_members") {
			return finish(c, notAllowed(c.T("promote.you_need_the_right_to_add_2")))
		}
		actor.Staff = am.staff
	}
	if _, err := botMember(c, "can_promote_members"); err != nil {
		return finish(c, err)
	}
	targetM, err := getMember(c, target.ID)
	if err != nil {
		return finish(c, err)
	}
	switch targetM.status() {
	case "creator":
		return finish(c, refuse(c.T("promote.the_owner_of_the_chat_sets")))
	case "administrator":
	default:
		return finish(c, refuse(c.T("promote.that_user_isn_t_an_admin")))
	}
	_, err = c.Bot.SetChatAdministratorCustomTitleWithContext(c.Ctx, c.Msg.Chat.Id, target.ID, title, nil)
	if err = promotionCall(c, err); err != nil {
		code, p := tgProblem(err)
		switch {
		case code == 400 && strings.Contains(p, "emoji"):
			c.Say(c.T("promote.a_title_can_t_contain_emoji"))
			return nil
		case code == 400 && strings.Contains(p, "admin"):
			c.Say(c.T("promote.i_can_only_set_the_title"))
			return nil
		}
		return finish(c, err)
	}
	who := text.UserLink(target.ID, target.Name)
	if title != "" {
		_, err = c.ReplyText(c.T("promote.was_given_the_title", core.V{"who": who, "text": text.Esc(title), "by": c.By(actor)}))
	} else {
		_, err = c.ReplyText(c.T("promote.the_title_of_was_cleared", core.V{"who": who, "by": c.By(actor)}))
	}
	return err
}

func mayEditOwnTag(c *core.Ctx, m *member) bool {
	switch m.status() {
	case "administrator", "creator":
		return true
	case "restricted":
		return m.m.CanEditTag
	}
	chat, err := c.Bot.GetChatWithContext(c.Ctx, c.Msg.Chat.Id, nil)
	if err != nil || chat.Permissions == nil {
		return false
	}
	return chat.Permissions.CanEditTag != nil && *chat.Permissions.CanEditTag
}

func cmdTag(c *core.Ctx) error {
	actor := c.ActorOf()
	if !core.IsGroup(c.Msg.Chat) {
		return finish(c, refuse(c.T("common.this_command_only_works_in_groups")))
	}
	if actor.Anonymous {
		return finish(c, notAllowed(c.T("common.send_this_command_as_yourself_not")))
	}
	target, err := promoteTarget(c, true)
	if err != nil {
		return finish(c, err)
	}
	tag := strings.TrimSpace(target.Extra)
	if len([]rune(tag)) > maxTitle {
		return finish(c, refuse(c.T("promote.a_tag_can_have_at_most", core.V{"MAX_LENGTH": maxTitle})))
	}
	if target.ID == actor.ID {
		me, err := getMember(c, actor.ID)
		if err != nil {
			return finish(c, err)
		}
		if !mayEditOwnTag(c, me) {
			return finish(c, notAllowed(c.T("promote.this_chat_doesn_t_let_members")))
		}
	} else {
		am := actorMember(c, "can_manage_tags")
		if am == nil {
			return finish(c, refuse(c.T("common.i_couldn_t_check_your_rights")))
		}
		if err := c.CheckTargetSelf(c.Msg.Chat, target, actor, false, true); err != nil {
			return finish(c, err)
		}
		if !am.has("can_manage_tags") {
			return finish(c, notAllowed(c.T("promote.you_need_the_right_to_manage")))
		}
		actor.Staff = am.staff
	}
	if _, err := botMember(c, "can_manage_tags"); err != nil {
		return finish(c, err)
	}
	targetM, err := getMember(c, target.ID)
	if err != nil {
		return finish(c, err)
	}
	status := targetM.status()
	if status == "administrator" || status == "creator" {
		return finish(c, refuse(c.T("promote.admins_have_a_title_instead_use")))
	}
	if status == "left" || status == "kicked" || (status == "restricted" && !targetM.m.IsMember) {
		return finish(c, refuse(c.T("common.that_user_isn_t_in_this")))
	}
	_, err = c.Bot.SetChatMemberTagWithContext(c.Ctx, c.Msg.Chat.Id, target.ID, &gotgbot.SetChatMemberTagOpts{Tag: tag})
	if err = c.Perform(err); err != nil {
		if code, p := tgProblem(err); code == 400 && strings.Contains(p, "emoji") {
			c.Say(c.T("promote.a_tag_can_t_contain_emoji"))
			return nil
		}
		return finish(c, err)
	}
	who := text.UserLink(target.ID, target.Name)
	if tag != "" {
		_, err = c.ReplyText(c.T("promote.was_given_the_tag", core.V{"who": who, "text": text.Esc(tag), "by": c.By(actor)}))
	} else {
		_, err = c.ReplyText(c.T("promote.the_tag_of_was_cleared", core.V{"who": who, "by": c.By(actor)}))
	}
	return err
}
