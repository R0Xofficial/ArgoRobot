package modules

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/core"
	"argo/internal/i18n"
	"argo/internal/text"
)

// Lock kinds of messages: /lock, /unlock, the allowlist and the watcher that takes locked
// messages away.

const (
	cbLocksClear   = "locks:clear:" // locks:clear:yes / locks:clear:no
	rightLookChat  = "can_manage_chat"
	lockFailureCD  = 300 * time.Second
	lockWarnCD     = 5 * time.Second // an album or a burst of messages earns one warn
	maxLockMessage = 3500
)

func registerLocks(r *core.Router) {
	r.Command(cmdLock, "lock")
	r.Command(cmdUnlock, "unlock")
	r.Command(cmdLocks, "locks")
	r.Command(cmdLocktypes, "locktypes")
	r.Command(cmdLockwarns, "lockwarns")
	r.Command(cmdLockmode, "lockmode")
	r.Command(cmdAllowlist, "allowlist")
	r.Command(cmdRmallowlist, "rmallowlist")
	r.Command(cmdRmallowlistall, "rmallowlistall")
	r.Button(cbLocksClear, cbRmallowlistall)
	// After antiflood (-60), before blacklists (-55): a locked message should be gone first.
	r.Watch(-58, "locks", watchLocks)
	r.Watch(-57, "locks-bots", watchBotsAdded)
}

// lockAdmin: an admin with the right to change the chat (change) or to look at it (a look-up).
func lockAdmin(c *core.Ctx, change bool) bool {
	right := rightLookChat
	if change {
		right = rightChangeInfo
	}
	return c.BeginChatCommand(core.AuthorizeOpts{Right: right}) != nil
}

func groupOnly(c *core.Ctx) bool {
	if !isGroup(c.Msg.Chat.Type) {
		c.Say(c.T("common.this_command_only_works_in_groups"))
		return false
	}
	return true
}

func uniqueWords(s string) []string {
	var seen []string
	for _, w := range strings.Fields(strings.ReplaceAll(s, ",", " ")) {
		if !contains(seen, w) {
			seen = append(seen, w)
		}
	}
	return seen
}

// chunkLines joins lines into messages that fit in one Telegram message.
func chunkLines(lines []string) []string {
	var out []string
	cur := ""
	for _, line := range lines {
		if cur != "" && len(cur)+len(line)+1 > maxLockMessage {
			out = append(out, cur)
			cur = ""
		}
		if cur != "" {
			cur += "\n"
		}
		cur += line
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func yesNo(c *core.Ctx, v bool) string {
	if v {
		return c.T("common.yes")
	}
	return c.T("common.no")
}

// --- Commands ---

func cmdLock(c *core.Ctx) error   { return changeLocks(c, true) }
func cmdUnlock(c *core.Ctx) error { return changeLocks(c, false) }

func changeLocks(c *core.Ctx, lock bool) error {
	verb := c.T("locks.unlock")
	if lock {
		verb = c.T("locks.lock")
	}
	if !groupOnly(c) || !lockAdmin(c, true) {
		return nil
	}
	var items []string
	for _, w := range uniqueWords(c.Args) {
		if w = strings.ToLower(w); !contains(items, w) {
			items = append(items, w)
		}
	}
	if len(items) == 0 {
		c.Say(c.T("locks.use_lt_item_gt_locktypes_lists", core.V{"verb": verb}))
		return nil
	}
	var unknown []string
	for _, it := range items {
		if !core.IsLock(it) {
			unknown = append(unknown, it)
		}
	}
	if len(unknown) > 0 {
		if len(unknown) > 10 {
			unknown = unknown[:10]
		}
		shown := make([]string, len(unknown))
		for i, u := range unknown {
			shown[i] = "<code>" + text.Esc(cutRunes(u, 30)) + "</code>"
		}
		c.Say(c.T("locks.i_don_t_know_locktypes_lists", core.V{"shown": strings.Join(shown, ", ")}))
		return nil
	}
	var err error
	if lock {
		err = c.App.Lock(c.Ctx, c.Msg.Chat.Id, items)
	} else {
		err = c.App.Unlock(c.Ctx, c.Msg.Chat.Id, items)
	}
	if err != nil {
		return err
	}
	names := make([]string, len(items))
	for i, it := range items {
		names[i] = "<code>" + it + "</code>"
	}
	joined := strings.Join(names, ", ")
	tag := "UNLOCKED"
	word := c.T("locks.unlocked")
	if lock {
		tag, word = "LOCKED", c.T("locks.locked")
	}
	c.LogAction(tag, c.ActorOf(), nil, "", valueDetail("common.items", joined))
	c.Say(word + " " + joined + ".")
	return nil
}

func cutRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}

func cmdLocks(c *core.Ctx) error {
	if !groupOnly(c) || !lockAdmin(c, false) {
		return nil
	}
	cfg, err := c.App.LockConfig(c.Ctx, c.Msg.Chat.Id)
	if err != nil {
		return err
	}
	title := c.Msg.Chat.Title
	if title == "" {
		title = c.T("common.this_chat")
	}
	lines := []string{
		c.T("locks.locks_in", core.V{"v": text.Esc(title)}),
		c.T("locks.warn_on_lock", core.V{"yes_no": yesNo(c, cfg.Warn)}),
		c.T("locks.allowlisted", core.V{"allow": len(cfg.Allow)}),
		"",
	}
	var ordered []string
	for _, n := range core.LockNames {
		if cfg.Locked[n] {
			ordered = append(ordered, n)
		}
	}
	if len(ordered) > 0 {
		lines = append(lines, "Locked:")
		for _, n := range ordered {
			lines = append(lines, "• <code>"+n+"</code>")
		}
	} else {
		lines = append(lines, "Nothing is locked. /locktypes lists what can be.")
	}
	c.Say(strings.Join(lines, "\n"))
	return nil
}

func lockTypesPages(c *core.Ctx) []string {
	lines := []string{c.T("locks.lockable_items"), c.T("locks.items_marked_can_be_eased_with")}
	for _, g := range core.LockGroups {
		lines = append(lines, "", "<b>"+g.Title+"</b>")
		for _, k := range g.Kinds {
			star := ""
			if k.Allowable {
				star = "*"
			}
			lines = append(lines, "• <code>"+k.Name+"</code>"+star+" - "+text.Esc(k.Description))
		}
	}
	return chunkLines(lines)
}

func cmdLocktypes(c *core.Ctx) error {
	if !groupOnly(c) || !lockAdmin(c, false) {
		return nil
	}
	for _, part := range lockTypesPages(c) {
		c.Say(part)
	}
	return nil
}

func cmdLockwarns(c *core.Ctx) error {
	if !groupOnly(c) {
		return nil
	}
	arg := strings.ToLower(strings.TrimSpace(c.Args))
	if arg == "" {
		if !lockAdmin(c, false) {
			return nil
		}
		cfg, err := c.App.LockConfig(c.Ctx, c.Msg.Chat.Id)
		if err != nil {
			return err
		}
		opt := c.T("common.off")
		if cfg.Warn {
			opt = c.T("common.on")
		}
		c.Say(c.T("locks.warning_on_locks_is_in_this", core.V{"opt": opt}))
		return nil
	}
	on, ok := text.ParseSwitch(arg)
	if !ok {
		c.Say(c.T("locks.use_lockwarns_on_or_lockwarns_off"))
		return nil
	}
	if !lockAdmin(c, true) {
		return nil
	}
	mode := "del"
	if on {
		mode = "warn"
	}
	if err := c.App.SetLockMode(c.Ctx, c.Msg.Chat.Id, mode, 0); err != nil {
		return err
	}
	opt := c.T("locks.only_have_it_deleted")
	if on {
		opt = c.T("locks.now_be_warned")
	}
	c.Say(c.T("locks.people_who_send_a_locked_item", core.V{"opt": opt}))
	return nil
}

func lockModeText(l i18n.Lang, mode string, seconds int) string {
	if mode == "del" {
		return l.T("locks.the_message_deleted")
	}
	if core.IsTimedMode(mode) {
		return l.T("common.for", core.V{"mode": l.Word(mode), "duration_text": text.DurationText(l, seconds)})
	}
	return l.Word(mode)
}

func cmdLockmode(c *core.Ctx) error {
	if !groupOnly(c) {
		return nil
	}
	spec := strings.TrimSpace(c.Args)
	usage := c.T("locks.use_lockmode_lt_del_warn_ban")
	if spec == "" {
		if !lockAdmin(c, false) {
			return nil
		}
		cfg, err := c.App.LockConfig(c.Ctx, c.Msg.Chat.Id)
		if err != nil {
			return err
		}
		c.Say(c.T("locks.someone_who_sends_a_locked_item", core.V{
			"mode_text": text.Esc(lockModeText(c.L, cfg.Action(), cfg.Seconds)), "usage": usage,
		}))
		return nil
	}
	if !lockAdmin(c, true) {
		return nil
	}
	mode, seconds, err := ParseAction(c.L, spec)
	if err != nil {
		if msg, ok := asTriggerError(err); ok {
			c.Say(text.Esc(msg) + "\n" + usage)
			return nil
		}
		return err
	}
	if err := c.App.SetLockMode(c.Ctx, c.Msg.Chat.Id, mode, seconds); err != nil {
		return err
	}
	c.Say(c.T("locks.someone_who_sends_a_locked_item_2", core.V{"mode_text": text.Esc(lockModeText(c.L, mode, seconds))}))
	return nil
}

// --- The allowlist ---

// allowEntries are the normalized entries typed, and a sentence about the first bad one.
func allowEntries(c *core.Ctx) ([]string, string) {
	var out []string
	for _, w := range uniqueWords(c.Args) {
		entry, err := core.NormalizeEntry(c.L, w)
		if err != nil {
			var ee *core.EntryError
			if errors.As(err, &ee) {
				return out, ee.Msg
			}
			return out, err.Error()
		}
		if !contains(out, entry) {
			out = append(out, entry)
		}
	}
	return out, ""
}

func cmdAllowlist(c *core.Ctx) error {
	if !groupOnly(c) {
		return nil
	}
	chatID := c.Msg.Chat.Id
	if strings.TrimSpace(c.Args) == "" {
		if !lockAdmin(c, false) {
			return nil
		}
		cfg, err := c.App.LockConfig(c.Ctx, chatID)
		if err != nil {
			return err
		}
		if len(cfg.Allow) == 0 {
			c.Say(c.T("locks.the_allowlist_is_empty_add_to"))
			return nil
		}
		lines := []string{c.T("locks.allowlist")}
		for _, e := range cfg.SortedAllow() {
			lines = append(lines, "• <code>"+text.Esc(e)+"</code>")
		}
		for _, part := range chunkLines(lines) {
			c.Say(part)
		}
		return nil
	}
	if !lockAdmin(c, true) {
		return nil
	}
	entries, problem := allowEntries(c)
	if problem != "" {
		c.Say(text.Esc(problem))
		return nil
	}
	cfg, err := c.App.LockConfig(c.Ctx, chatID)
	if err != nil {
		return err
	}
	total := len(cfg.Allow)
	for _, e := range entries {
		if !cfg.Allow[e] {
			total++
		}
	}
	if total > core.MaxAllowed {
		c.Say(c.T("locks.the_allowlist_is_full_items_remove", core.V{"MAX_ALLOWED": core.MaxAllowed}))
		return nil
	}
	added, err := c.App.AllowAdd(c.Ctx, chatID, entries)
	if err != nil {
		return err
	}
	if added == 0 {
		c.Say(c.T("locks.those_were_already_allowlisted"))
		return nil
	}
	c.Say(c.T("locks.allowlisted_new_item", core.V{"added": added, "opt": plural(added)}))
	return nil
}

func cmdRmallowlist(c *core.Ctx) error {
	if !groupOnly(c) || !lockAdmin(c, true) {
		return nil
	}
	if strings.TrimSpace(c.Args) == "" {
		c.Say(c.T("locks.use_rmallowlist_lt_items_gt_allowlist"))
		return nil
	}
	entries, problem := allowEntries(c)
	if problem != "" {
		c.Say(text.Esc(problem))
		return nil
	}
	removed, err := c.App.AllowRemove(c.Ctx, c.Msg.Chat.Id, entries)
	if err != nil {
		return err
	}
	if removed == 0 {
		c.Say(c.T("locks.none_of_those_were_on_the"))
		return nil
	}
	c.Say(c.T("locks.removed_item_from_the_allowlist", core.V{"removed": removed, "opt": plural(removed)}))
	return nil
}

func cmdRmallowlistall(c *core.Ctx) error {
	if !c.RequireChatOwner() {
		return nil
	}
	cfg, err := c.App.LockConfig(c.Ctx, c.Msg.Chat.Id)
	if err != nil {
		return err
	}
	if len(cfg.Allow) == 0 {
		c.Say(c.T("locks.the_allowlist_of_this_chat_is"))
		return nil
	}
	_, err = c.Reply(c.T("locks.remove_all_items_from_the_allowlist", core.V{"count": len(cfg.Allow)}), core.Keyboard(
		[]gotgbot.InlineKeyboardButton{core.Button(c.T("common.remove_all"), cbLocksClear+"yes"), core.Button(c.T("common.cancel"), cbLocksClear+"no")}))
	return err
}

func cbRmallowlistall(c *core.Ctx) error {
	if c.Msg == nil {
		c.Answer(c.T("common.this_button_is_no_longer_valid"), true)
		return nil
	}
	if c.MemberStatus(c.Msg.Chat.Id, c.User.Id) != "creator" {
		c.Answer(c.T("common.only_the_owner_of_this_chat"), true)
		return nil
	}
	if c.Cb.Data == cbLocksClear+"yes" {
		removed, err := c.App.AllowClear(c.Ctx, c.Msg.Chat.Id)
		if err != nil {
			return err
		}
		c.Answer("", false)
		return c.Edit(c.T("locks.done_items_removed", core.V{"removed": removed}), nil)
	}
	c.Answer("", false)
	return c.Edit(c.T("locks.cancelled_the_allowlist_is_unchanged"), nil)
}

// --- Enforcement ---

// lockExempt: nobody here is touched by locks: the bot's owners and staff, approved users and chat admins.
func lockExempt(c *core.Ctx, chatID, userID int64) (bool, error) {
	if core.IsImmune(userID) {
		return true, nil
	}
	if role := c.App.GetRole(c.Ctx, userID); role != core.NoRole {
		return true, nil
	}
	if approved, err := c.App.IsApproved(c.Ctx, chatID, userID); err != nil || approved {
		return approved, err
	}
	return c.IsChatAdmin(chatID, userID), nil
}

func isServiceMessage(m *gotgbot.Message) bool {
	return len(m.NewChatMembers) > 0 || m.LeftChatMember != nil || m.NewChatTitle != "" || len(m.NewChatPhoto) > 0 ||
		m.DeleteChatPhoto || m.GroupChatCreated || m.SupergroupChatCreated || m.ChannelChatCreated ||
		m.MigrateToChatId != 0 || m.MigrateFromChatId != 0 || m.PinnedMessage != nil ||
		m.ForumTopicCreated != nil || m.ForumTopicEdited != nil || m.ForumTopicClosed != nil || m.ForumTopicReopened != nil ||
		m.GeneralForumTopicHidden != nil || m.GeneralForumTopicUnhidden != nil ||
		m.VideoChatStarted != nil || m.VideoChatEnded != nil || m.VideoChatScheduled != nil ||
		m.VideoChatParticipantsInvited != nil || m.MessageAutoDeleteTimerChanged != nil || m.BoostAdded != nil
}

// lockWarn warns the sender; at the chat's warn limit, punish them and start the count over.
func lockWarn(c *core.Ctx, reason string) (string, error) {
	chatID, user := c.Msg.Chat.Id, c.User
	target := &core.Target{ID: user.Id, Name: core.FullName(user)}
	if _, err := addWarn(c, chatID, target, reason, &core.Actor{ID: c.Bot.Id, Name: c.T("locks.locks")}); err != nil {
		return "", err
	}
	warns, err := listWarns(c, chatID, user.Id)
	if err != nil {
		return "", err
	}
	count := len(warns)
	limit, err := warnLimit(c, chatID)
	if err != nil {
		return "", err
	}
	if count < limit {
		return c.T("common.warned", core.V{"count": count, "limit": limit}), nil
	}
	mode, seconds, err := warnMode(c, chatID)
	if err != nil {
		return "", err
	}
	done, err := c.Punish(chatID, user.Id, mode, seconds)
	if err != nil {
		return "", err
	}
	if _, err := clearWarns(c, chatID, user.Id); err != nil {
		return "", err
	}
	return c.T("common.after_reaching_the_warn_limit", core.V{"done": done, "count": count, "limit": limit}), nil
}

func watchLocks(c *core.Ctx) (bool, error) {
	m, user := c.Msg, c.User
	if c.Cb != nil || m == nil || user == nil || !isGroup(m.Chat.Type) || m.IsAutomaticForward || isServiceMessage(m) {
		return false, nil
	}
	channel := m.SenderChat != nil && m.SenderChat.Id != m.Chat.Id
	if m.SenderChat != nil && !channel {
		return false, nil // an anonymous admin
	}
	if user.IsBot && !channel {
		return false, nil
	}
	cfg, err := c.App.LockConfig(c.Ctx, m.Chat.Id)
	if err != nil || len(cfg.Locked) == 0 {
		return false, err
	}
	kind := c.FindLock(m, cfg)
	if kind == "" {
		return false, nil
	}
	if !channel {
		if exempt, err := lockExempt(c, m.Chat.Id, user.Id); err != nil || exempt {
			return false, err
		}
	}
	action := cfg.Action()
	if channel {
		action = "del" // a channel can't be warned or muted as a user
	}

	_, _ = c.Bot.DeleteMessageWithContext(c.Ctx, m.Chat.Id, m.MessageId, nil)
	if action == "del" {
		return true, nil
	}
	if !c.App.Cooldown(fmt.Sprintf("lock-warn:%d:%d", m.Chat.Id, user.Id), lockWarnCD) {
		return true, nil
	}
	var done string
	if action == "warn" {
		done, err = lockWarn(c, c.T("locks.sent_a_locked_item", core.V{"kind": kind}))
	} else {
		done, err = c.Punish(m.Chat.Id, user.Id, action, cfg.Seconds)
	}
	if err != nil {
		var me *core.ModerationError
		if !errors.As(err, &me) {
			return true, err
		}
		if c.App.Cooldown(fmt.Sprintf("lock-failed:%d", m.Chat.Id), lockFailureCD) {
			_, _ = c.SendPlain(c.T("locks.sent_a_locked_item_but_i", core.V{
				"id": text.UserLink(user.Id, core.FullName(user)), "exc": text.Esc(me.Msg),
			}))
		}
		return true, nil
	}
	_, err = c.SendPlain(c.T("locks.has_been_reason_sent_a_locked", core.V{
		"id": text.UserLink(user.Id, core.FullName(user)), "done": done, "kind": kind,
	}))
	return true, err
}

// ServiceCleaner is set by the greetings module: whether the chat wants join messages deleted.
var cleanServiceHook func(c *core.Ctx, chatID int64) bool

func watchBotsAdded(c *core.Ctx) (bool, error) {
	m, adder := c.Msg, c.User
	if c.Cb != nil || c.Edited || m == nil || len(m.NewChatMembers) == 0 || adder == nil || !isGroup(m.Chat.Type) {
		return false, nil
	}
	var bots []gotgbot.User
	for _, member := range m.NewChatMembers {
		if member.IsBot && member.Id != c.Bot.Id && member.Id != adder.Id {
			bots = append(bots, member)
		}
	}
	if len(bots) == 0 {
		return false, nil
	}
	cfg, err := c.App.LockConfig(c.Ctx, m.Chat.Id)
	if err != nil || !cfg.Locked["bot"] {
		return false, err
	}
	if exempt, err := lockExempt(c, m.Chat.Id, adder.Id); err != nil || exempt {
		return false, err
	}
	chatID := m.Chat.Id
	for _, member := range bots {
		_, err := c.Bot.BanChatMemberWithContext(c.Ctx, chatID, member.Id, nil)
		if err = c.Perform(err); err != nil {
			var me *core.ModerationError
			if !errors.As(err, &me) {
				return true, err
			}
			if c.App.Cooldown(fmt.Sprintf("lock-failed:%d", chatID), lockFailureCD) {
				_, _ = c.SendPlain(c.T("locks.bots_are_locked_here_but_i", core.V{"exc": text.Esc(me.Msg)}))
			}
			return true, nil
		}
		_, _ = c.Bot.UnbanChatMemberWithContext(c.Ctx, chatID, member.Id, &gotgbot.UnbanChatMemberOpts{OnlyIfBanned: true})
	}
	names := make([]string, len(bots))
	for i := range bots {
		names[i] = text.Esc(core.FullName(&bots[i]))
	}
	_, err = c.SendPlain(c.T("locks.bots_are_locked_in_this_chat", core.V{"names": strings.Join(names, ", ")}))
	if err != nil {
		return true, err
	}
	// This handler takes the message away from the greetings one.
	if cleanServiceHook != nil && cleanServiceHook(c, chatID) {
		_, _ = c.Bot.DeleteMessageWithContext(c.Ctx, chatID, m.MessageId, nil)
	}
	return true, nil
}
