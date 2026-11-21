package core

import (
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/i18n"
	"argo/internal/text"
)

// Shared pieces of the moderation modules (core/moderation.py in the Python version).
//
// Who may use the commands: admins of the group (with the right to restrict users), and the bot's Owner,
// Developers and Sudo users, who are trusted to help keep order in any group without being admins there.
// Anonymous admins count as admins. Support users and regular members may not.
//
// A command handler starts with Begin(): it checks the person, finds the target user and refuses targets that
// must never be touched (yourself, the bot, immune accounts, chat admins). Everything it refuses is answered
// with a short plain message, so handlers only continue on success.
//
// Calls to Telegram go through Perform(), which turns the usual refusals ("not enough rights", "user is an
// administrator", ...) into messages meant for people. Unexpected errors are left to the normal error reporting.

// StaffRole is the lowest role that may moderate in any group without being an admin there.
const StaffRole = Sudo

const MaxReasonLength = 300

// ModerationError carries a message that is safe to show as it is (plain text).
type ModerationError struct {
	Msg string
	// NotAllowed marks "you may not use this command here": chats can choose not to answer these (/adminerror).
	NotAllowed bool
}

func (e *ModerationError) Error() string { return e.Msg }

func modErr(msg string) error     { return &ModerationError{Msg: msg} }
func notAllowed(msg string) error { return &ModerationError{Msg: msg, NotAllowed: true} }

// Deny answers a refusal, unless it is a "not allowed" and the chat asked not to hear those.
func (c *Ctx) Deny(err error) {
	var me *ModerationError
	if errors.As(err, &me) && me.NotAllowed && !c.App.Setting(c.Ctx, "admin_errors", c.Msg.Chat.Id) {
		return
	}
	c.Say(text.Esc(err.Error()))
}

// Actor is whoever gave the command. Anonymous admins have no id.
type Actor struct {
	ID        int64 // 0 for an anonymous admin
	Name      string
	Anonymous bool
	Staff     bool // acting as the bot's staff, not as an admin of the chat
}

func (a *Actor) Link(l i18n.Lang) string {
	if a.Anonymous {
		return l.T("moderation.anonymous_admin")
	}
	return text.UserLink(a.ID, a.Name)
}

// ActorOf is whoever sent the message.
func (c *Ctx) ActorOf() *Actor {
	m := c.Msg
	if sc := m.SenderChat; sc != nil && sc.Id == m.Chat.Id {
		// An admin writing as the group itself: Telegram hides who it is.
		return &Actor{Name: c.T("moderation.anonymous_admin"), Anonymous: true}
	}
	if c.User == nil {
		return &Actor{}
	}
	return &Actor{ID: c.User.Id, Name: FullName(c.User)}
}

func isGroupType(t string) bool { return t == "group" || t == "supergroup" }

var rightText = map[string]string{
	"can_restrict_members": "moderation.right_text.can_restrict_members",
	"can_invite_users":     "common.invite_users",
	"can_change_info":      "moderation.right_text.can_change_info",
	"can_promote_members":  "moderation.right_text.can_promote_members",
	"can_manage_chat":      "moderation.right_text.can_manage_chat",
}

func hasRight(m gotgbot.MergedChatMember, right string) bool {
	switch right {
	case "can_restrict_members":
		return m.CanRestrictMembers
	case "can_invite_users":
		return m.CanInviteUsers
	case "can_change_info":
		return m.CanChangeInfo
	case "can_promote_members":
		return m.CanPromoteMembers
	case "can_manage_chat":
		return m.CanManageChat
	case "can_delete_messages":
		return m.CanDeleteMessages
	case "can_pin_messages":
		return m.CanPinMessages
	case "can_manage_tags":
		return m.CanManageTags
	case "can_manage_video_chats":
		return m.CanManageVideoChats
	case "can_manage_topics":
		return m.CanManageTopics
	}
	return false
}

// AuthorizeOpts tune Authorize. The zero value means: staff may help, from Sudo up, needing can_restrict_members.
type AuthorizeOpts struct {
	NoStaff  bool
	StaffMin Role
	Right    string
}

// Authorize checks that user may moderate in chat. It returns "admin" or "staff": the capacity they act in.
// A chat admin (with the right) acts as an admin, even when they also hold a role in the bot. Bot staff who are
// not admins of the chat act as "staff", but only for commands that moderate users and only while the chat's
// owner has not turned their help off. Anonymous admins can't be checked (Telegram hides who they are), so they
// are refused unless the chat's owner trusts them all with /anonadmin.
func (c *Ctx) Authorize(chat gotgbot.Chat, user *gotgbot.User, role Role, anonymous bool, o AuthorizeOpts) (string, error) {
	if o.StaffMin == NoRole {
		o.StaffMin = StaffRole
	}
	if o.Right == "" {
		o.Right = "can_restrict_members"
	}
	if !isGroupType(chat.Type) {
		return "", modErr(c.T("common.this_command_only_works_in_groups"))
	}
	if anonymous {
		if c.App.Setting(c.Ctx, "anon_admin", chat.Id) {
			return "admin", nil
		}
		return "", notAllowed(c.T("moderation.i_can_t_check_the_rights"))
	}
	staffMayHelp := func() bool { return !o.NoStaff && role != NoRole && role >= o.StaffMin }

	var uid int64
	if user != nil {
		uid = user.Id
	}
	member, err := c.Bot.GetChatMemberWithContext(c.Ctx, chat.Id, uid, nil)
	if err != nil {
		if staffMayHelp() && c.App.Setting(c.Ctx, "staff_help", chat.Id) {
			return "staff", nil
		}
		return "", modErr(c.T("common.i_couldn_t_check_your_rights"))
	}
	m := member.MergeChatMember()
	if m.Status == "creator" {
		return "admin", nil
	}
	if m.Status == "administrator" && hasRight(m, o.Right) {
		return "admin", nil
	}
	if staffMayHelp() && c.App.Setting(c.Ctx, "staff_help", chat.Id) {
		return "staff", nil
	}
	if m.Status == "administrator" {
		tag, ok := rightText[o.Right]
		if !ok {
			tag = o.Right
		}
		return "", notAllowed(c.T("moderation.you_need_the_admin_right_to", V{"get": c.T(tag)}))
	}
	return "", notAllowed(c.T("moderation.only_chat_admins_can_use_this"))
}

// MemberStatus is the user's status in the chat (creator, administrator, member, restricted, left, kicked),
// or "" when Telegram can't tell.
func (c *Ctx) MemberStatus(chatID, userID int64) string {
	m := c.GetMember(chatID, userID)
	if m == nil {
		return ""
	}
	return m.Status
}

// GetMember is the chat member, or nil when Telegram can't tell. A user Telegram has never seen in the chat is "left".
func (c *Ctx) GetMember(chatID, userID int64) *gotgbot.MergedChatMember {
	member, err := c.Bot.GetChatMemberWithContext(c.Ctx, chatID, userID, nil)
	if err != nil {
		if p := problem(err); strings.Contains(p, "user not found") || strings.Contains(p, "participant_id_invalid") {
			return &gotgbot.MergedChatMember{Status: "left"}
		}
		return nil
	}
	m := member.MergeChatMember()
	return &m
}

func (c *Ctx) IsChatAdmin(chatID, userID int64) bool {
	s := c.MemberStatus(chatID, userID)
	return s == "creator" || s == "administrator"
}

// CheckTarget refuses targets that must not be touched. punitive is for commands that restrict someone (ban,
// mute, warn): they also refuse chat admins. Undoing a punishment is never blocked that way.
func (c *Ctx) CheckTarget(chat gotgbot.Chat, t *Target, actor *Actor, punitive bool) error {
	return c.CheckTargetSelf(chat, t, actor, punitive, false)
}

// CheckTargetSelf is CheckTarget; allowSelf lets people aim the command at themselves (/promote, /demote).
func (c *Ctx) CheckTargetSelf(chat gotgbot.Chat, t *Target, actor *Actor, punitive, allowSelf bool) error {
	if t.IsChat() {
		// Only channels writing into the chat can be banned; never the group itself.
		if t.Kind != "channel" || t.ID == chat.Id {
			return modErr(c.T("moderation.only_channels_can_be_banned_that"))
		}
		return nil
	}
	if !allowSelf && actor.ID != 0 && actor.ID == t.ID {
		return modErr(c.T("moderation.you_can_t_do_that_to"))
	}
	if t.ID == c.Bot.Id {
		return modErr(c.T("moderation.i_can_t_take_action_against"))
	}
	if IsImmune(t.ID) {
		return modErr(c.T("common.i_can_t_take_action_against"))
	}
	if !punitive {
		return nil
	}
	if c.IsChatAdmin(chat.Id, t.ID) {
		return modErr(c.T("common.i_can_t_take_action_against_2"))
	}
	return nil
}

// BeginOpts tune Begin.
type BeginOpts struct {
	AllowChats bool
	Unpunitive bool // the command only undoes or reads (unban, warns): chat admins may be its target
}

// Begin is the start of every moderation command: check the person and the target. It returns nil, nil after
// answering why the command can't run.
func (c *Ctx) Begin(o BeginOpts) (*Target, *Actor) {
	actor := c.ActorOf()
	capacity, err := c.Authorize(c.Msg.Chat, c.User, c.Role, actor.Anonymous, AuthorizeOpts{})
	var target *Target
	if err == nil {
		actor.Staff = capacity == "staff"
		target, err = c.ResolveTarget(true, o.AllowChats)
		if err != nil {
			var te *TargetError
			if errors.As(err, &te) {
				err = modErr(te.Msg)
			}
		}
	}
	if err == nil {
		err = c.CheckTarget(c.Msg.Chat, target, actor, !o.Unpunitive)
	}
	if err != nil {
		c.denyOrFail(err)
		return nil, nil
	}
	return target, actor
}

// denyOrFail answers a refusal; anything that is not a refusal is a real failure and is only logged.
func (c *Ctx) denyOrFail(err error) {
	var me *ModerationError
	if errors.As(err, &me) {
		c.Deny(err)
		return
	}
	slog.Error("Moderation command failed", "cmd", c.Cmd, "err", err)
}

// BeginChatCommand is like Begin, for commands about the chat itself (no target user). The bot's staff (Sudo and
// up) may use these like an admin with every right, unless the chat's owner turned their help off with /staffhelp.
func (c *Ctx) BeginChatCommand(o AuthorizeOpts) *Actor {
	actor := c.ActorOf()
	capacity, err := c.Authorize(c.Msg.Chat, c.User, c.Role, actor.Anonymous, o)
	if err != nil {
		c.denyOrFail(err)
		return nil
	}
	actor.Staff = capacity == "staff"
	return actor
}

// RequireReply is the message a command replies to; it answers with a hint when there is none.
func (c *Ctx) RequireReply() *gotgbot.Message {
	found := RepliedMessage(c.Msg)
	if found == nil {
		c.Say(c.T("moderation.reply_to_the_message_you_want"))
	}
	return found
}

const rightsMessage = "moderation.rights_message"

// Perform turns the usual Telegram refusals into a *ModerationError. Anything else is returned as it is.
func (c *Ctx) Perform(err error, rightsTag ...string) error {
	if err == nil {
		return nil
	}
	tag := rightsMessage
	if len(rightsTag) > 0 {
		tag = rightsTag[0]
	}
	tg := TgError(err)
	if tg == nil {
		return err
	}
	p := strings.ToLower(tg.Description)
	has := func(keys ...string) bool {
		for _, k := range keys {
			if strings.Contains(p, k) {
				return true
			}
		}
		return false
	}
	switch {
	case tg.Code == 403:
		return modErr(c.T(tag))
	case tg.Code != 400:
		return err
	case has("not enough rights", "need administrator rights", "chat_admin_required", "have no rights"):
		return modErr(c.T(tag))
	case has("can't remove chat owner", "can't restrict self", "user is an administrator", "administrator of the chat"):
		return modErr(c.T("common.i_can_t_take_action_against_2"))
	case has("user not found", "participant_id_invalid", "user_not_participant", "member not found"):
		return modErr(c.T("common.that_user_isn_t_in_this"))
	case strings.Contains(p, "supergroup") && strings.Contains(p, "only"):
		return modErr(c.T("moderation.this_only_works_in_supergroups"))
	}
	return err
}

// --- Buttons -------------------------------------------------------------------

const CBUndo = "undo:" // undo:<ban|mute>:<user or channel id>, the button under a ban or a mute

func Button(label, data string) gotgbot.InlineKeyboardButton {
	return gotgbot.InlineKeyboardButton{Text: label, CallbackData: data}
}

func Keyboard(rows ...[]gotgbot.InlineKeyboardButton) *gotgbot.InlineKeyboardMarkup {
	return &gotgbot.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// UndoKeyboard is the "Unban" / "Unmute" button under a ban or a mute: no need to look up the id.
func (c *Ctx) UndoKeyboard(kind string, t *Target) *gotgbot.InlineKeyboardMarkup {
	label := c.T("moderation.unmute")
	if kind == "ban" {
		label = c.T("moderation.unban")
	}
	return Keyboard([]gotgbot.InlineKeyboardButton{Button(label, CBUndo+kind+":"+itoa(t.ID))})
}

// ParseUndo is the id in the data of an undo button of this kind, if it is one.
func ParseUndo(data, kind string) (int64, bool) {
	prefix := CBUndo + kind + ":"
	if !strings.HasPrefix(data, prefix) {
		return 0, false
	}
	id, err := strconv.ParseInt(data[len(prefix):], 10, 64)
	return id, err == nil
}

// AuthorizeCallback is the message a button is under and who pressed it, if they may moderate there.
func (c *Ctx) AuthorizeCallback(right string) (*Actor, bool) {
	if c.Msg == nil {
		c.Answer(c.T("moderation.this_message_is_too_old"), true)
		return nil, false
	}
	capacity, err := c.Authorize(c.Msg.Chat, c.User, c.Role, false, AuthorizeOpts{Right: right})
	if err != nil {
		var me *ModerationError
		if errors.As(err, &me) {
			c.Answer(me.Msg, true)
		} else {
			slog.Error("Authorizing a button failed", "err", err)
		}
		return nil, false
	}
	return &Actor{ID: c.User.Id, Name: FullName(c.User), Staff: capacity == "staff"}, true
}

// TargetByID is a target made from an id alone (a button): the name is looked up, a channel when the id says so.
func (c *Ctx) TargetByID(chatID, id int64) *Target {
	if id < 0 {
		title := ""
		if chat, err := c.Bot.GetChatWithContext(c.Ctx, id, nil); err == nil {
			title = chat.Title
		}
		return &Target{ID: id, Name: title, Kind: "channel", Title: title}
	}
	t := &Target{ID: id, Kind: "user"}
	if m := c.GetMember(chatID, id); m != nil {
		t.Name = FullName(&m.User)
	}
	return t
}

// --- Wording -------------------------------------------------------------------

// Who is the target as a link: a user by account, a channel by its public link.
func (c *Ctx) Who(t *Target) string { return who(c.L, t) }

func who(l i18n.Lang, t *Target) string {
	if t.IsChat() {
		title := t.Title
		if title == "" {
			title = t.Name
		}
		if title == "" {
			title = l.T("common.channel")
		}
		if t.Username != "" {
			return fmt.Sprintf(`<a href="https://t.me/%s">%s</a>`, text.Esc(t.Username), text.Esc(title))
		}
		return "<b>" + text.Esc(title) + "</b>"
	}
	return text.UserLink(t.ID, t.Name)
}

const staffNoteTag = "moderation.staff_note"

// StaffNoteTag is the tag of the note that says the bot's staff did it.
const StaffNoteTag = staffNoteTag

// By is " by Name": who did it, written into the sentence ("King was banned by Jey"). No ids.
func (c *Ctx) By(a *Actor) string { return c.T("moderation.by_2", V{"link": a.Link(c.L)}) }

// StaffNote is a short italic note on its own line when the bot's staff did it (added at the end of the message).
func (c *Ctx) StaffNote(a *Actor) string {
	if !a.Staff {
		return ""
	}
	return "\n\n<i>" + c.T(staffNoteTag) + "</i>"
}

// AdminRef is who did it, for a log entry: a link and the id (an anonymous admin has none).
func AdminRef(l i18n.Lang, a *Actor) string {
	link := a.Link(l)
	switch {
	case a.ID == 0:
		return link
	case link != "":
		return fmt.Sprintf("%s [<code>%d</code>]", link, a.ID)
	}
	return fmt.Sprintf("[<code>%d</code>]", a.ID)
}

// ReasonLine is the "Reason: ..." line of a message, or "".
func (c *Ctx) ReasonLine(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ""
	}
	return c.T("common.reason", V{"reason": text.Esc(text.Truncate(reason, MaxReasonLength))})
}

// LogAction copies a moderation action to the chat's log (/setlog), if it has one. It never fails.
func (c *Ctx) LogAction(tag string, actor *Actor, target *Target, reason string, details ...Detail) {
	// The admin and user lines are made in the language of the log chat inside RecordLog; they only use ids and names.
	userRef := ""
	if target != nil {
		if target.IsChat() {
			userRef = who(i18n.English, target)
		} else {
			userRef = text.UserRef(target.ID, target.Name)
		}
	}
	note := ""
	if actor.Staff {
		note = staffNoteTag
	}
	c.RecordLog(c.Msg.Chat, tag, AdminRef(c.L, actor), userRef, reason, "", details, note)
}

// LogSelf copies an action somebody did to themselves (/kickme) to the chat's log: no admin line, as there is no admin.
func (c *Ctx) LogSelf(tag string, userID int64, name, reasonTag string) {
	c.RecordLog(c.Msg.Chat, tag, "", text.UserRef(userID, name), "", reasonTag, nil, "")
}

// --- Durations -----------------------------------------------------------------

var durationRE = regexp.MustCompile(`(?i)^(\d{1,6})([mhdw])$`)

const (
	MinDuration = time.Minute
	MaxDuration = 365 * 24 * time.Hour // Telegram treats anything longer as "forever"
)

// ParseDuration: 30m, 2h, 3d, 1w -> a duration; ok is false when the text is not a duration.
func ParseDuration(s string) (time.Duration, bool) {
	m := durationRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, false
	}
	n, _ := strconv.Atoi(m[1])
	unit := map[string]time.Duration{"m": time.Minute, "h": time.Hour, "d": 24 * time.Hour, "w": 7 * 24 * time.Hour}[strings.ToLower(m[2])]
	return time.Duration(n) * unit, true
}

// SplitDuration reads the extra data of a timed command: a duration, then the reason.
func (c *Ctx) SplitDuration(extra string) (time.Duration, string, error) {
	extra = strings.TrimSpace(extra)
	first, rest, _ := strings.Cut(extra, " ")
	var d time.Duration
	ok := false
	if first != "" {
		d, ok = ParseDuration(first)
	}
	if !ok || d < MinDuration || d > MaxDuration {
		return 0, "", modErr(c.T("moderation.duration_help"))
	}
	return d, strings.TrimSpace(rest), nil
}

// FormatDuration: 90 minutes -> '1 hour 30 minutes' (English: it is used in logs).
func FormatDuration(d time.Duration) string {
	minutes := int(d.Minutes())
	var parts []string
	for _, u := range []struct {
		name string
		size int
	}{{"day", 1440}, {"hour", 60}, {"minute", 1}} {
		n := minutes / u.size
		minutes %= u.size
		if n > 0 {
			s := ""
			if n != 1 {
				s = "s"
			}
			parts = append(parts, fmt.Sprintf("%d %s%s", n, u.name, s))
		}
	}
	return strings.Join(parts, " ")
}

// RequireChatOwner is true when the command was given by the owner of the chat; otherwise it answers why not.
func (c *Ctx) RequireChatOwner() bool {
	switch {
	case !isGroupType(c.Msg.Chat.Type):
		c.Deny(modErr(c.T("common.this_command_only_works_in_groups")))
		return false
	case c.ActorOf().Anonymous || c.User == nil:
		c.Deny(modErr(c.T("common.send_this_command_as_yourself_not")))
		return false
	}
	m := c.GetMember(c.Msg.Chat.Id, c.User.Id)
	switch {
	case m == nil:
		c.Deny(modErr(c.T("common.i_couldn_t_check_your_rights")))
		return false
	case m.Status != "creator":
		c.Deny(notAllowed(c.T("moderation.only_the_owner_of_this_chat")))
		return false
	}
	return true
}
