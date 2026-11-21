package modules

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/blacklist"
	"argo/internal/core"
	"argo/internal/i18n"
	"argo/internal/text"
)

// Forbidden words and phrases (modules/blacklists.py). Messages that match a trigger are deleted, or get a custom
// action. Blacklists do not affect group admins, or approved users.

const (
	cbBlacklistClear = "blacklist:clear:" // blacklist:clear:yes / blacklist:clear:no
	failureCooldown  = 300 * time.Second
	maxShownTriggers = 80
	maxShownAdded    = 25
)

var (
	actionSuffixRE = regexp.MustCompile(`\{([^{}]*)\}\s*$`)
	quotes         = "\"'“”‘’"
)

func registerBlacklists(r *core.Router) {
	r.Command(cmdBlacklists, "blacklists")
	r.Command(cmdAddblacklist, "addblacklist", "blacklist")
	r.Command(cmdUnblacklist, "unblacklist", "rmblacklist")
	r.Command(cmdBlacklistmode, "blacklistmode", "blocklistmode")
	r.Command(cmdSetblacklistmode, "setblacklistmode", "setblocklistmode")
	r.Command(cmdBlocklistdelete, "blocklistdelete", "blacklistdelete")
	r.Command(cmdUnblacklistall, "unblacklistall")
	r.Button(cbBlacklistClear, cbUnblacklistall)
	// After antiflood (-60), before the modules that answer commands: a blacklisted message should be gone whatever
	// else it asks for.
	r.Watch(-55, "blacklist", watchBlacklist)
}

// --- Wording and parsing ---

func actionName(action string) string {
	if action == "del" {
		return "delete"
	}
	return action
}

func blModeText(l i18n.Lang, mode string, seconds int) string {
	switch {
	case mode == "off":
		return l.T("common.off")
	case core.IsTimedMode(mode):
		return l.T("common.for", core.V{"mode": l.Word(mode), "duration_text": text.DurationText(l, seconds)})
	}
	return l.Word(actionName(mode))
}

func triggerLine(l i18n.Lang, t *blacklist.Trigger) string {
	line := "<code>" + text.Esc(t.Pattern) + "</code>"
	if t.Action != "" {
		line += " {" + text.Esc(blModeText(l, t.Action, t.Seconds)) + "}"
	}
	return line
}

// splitAction: 'trigger {ban}' -> ('trigger', 'ban', true).
func splitAction(s string) (string, string, bool) {
	loc := actionSuffixRE.FindStringSubmatchIndex(s)
	if loc == nil {
		return strings.TrimSpace(s), "", false
	}
	return strings.TrimSpace(s[:loc[0]]), strings.TrimSpace(s[loc[2]:loc[3]]), true
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) >= 2 && strings.ContainsRune(quotes, r[0]) && strings.ContainsRune(quotes, r[len(r)-1]) {
		return string(r[1 : len(r)-1])
	}
	return s
}

func stickerTrigger(l i18n.Lang, s *gotgbot.Sticker) (string, error) {
	switch {
	case s.SetName != "":
		return "stickerpack:" + s.SetName, nil
	case s.Emoji != "":
		return "sticker:" + s.Emoji, nil
	}
	return "", &blacklist.Error{Msg: l.T("blacklists.that_sticker_has_neither_a_pack")}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// asTriggerError reads an error that says what is wrong with a trigger or an action.
func asTriggerError(err error) (string, bool) {
	var te *blacklist.Error
	if errors.As(err, &te) {
		return te.Msg, true
	}
	var ae *ActionError
	if errors.As(err, &ae) {
		return ae.Msg, true
	}
	return "", false
}

func messageText(m *gotgbot.Message) string {
	if m.Text != "" {
		return m.Text
	}
	return m.Caption
}

// --- Commands ---

func cmdBlacklists(c *core.Ctx) error {
	if !isGroup(c.Msg.Chat.Type) {
		c.Say(c.T("common.this_command_only_works_in_groups"))
		return nil
	}
	found, err := c.App.Triggers(c.Ctx, c.Msg.Chat.Id)
	if err != nil {
		return err
	}
	cfg, err := c.App.BlacklistSettings(c.Ctx, c.Msg.Chat.Id)
	if err != nil {
		return err
	}
	chatTitle := c.Msg.Chat.Title
	if chatTitle == "" {
		chatTitle = c.T("common.this_chat")
	}
	title := c.T("blacklists.blacklisted_triggers_in", core.V{"v": text.Esc(chatTitle)})
	if len(found) == 0 {
		c.Say(c.T("blacklists.there_are_none", core.V{"title": title}))
		return nil
	}
	lines := []string{title, c.T("blacklists.default_action", core.V{"mode_text": text.Esc(blModeText(c.L, cfg.Mode, cfg.ModeSeconds))}), ""}
	for i, t := range found {
		if i >= maxShownTriggers {
			break
		}
		lines = append(lines, "• "+triggerLine(c.L, t))
	}
	if len(found) > maxShownTriggers {
		lines = append(lines, fmt.Sprintf("…and %d more.", len(found)-maxShownTriggers))
	}
	c.Say(strings.Join(lines, "\n"))
	return nil
}

type entry struct {
	text, spec, reason string
	hasSpec            bool
}

func cmdAddblacklist(c *core.Ctx) error {
	if !adminOnly(c) {
		return nil
	}
	args := strings.TrimSpace(c.Args)
	replied := core.RepliedMessage(c.Msg)

	var entries []entry
	var problems []string
	switch {
	case replied != nil:
		// The replied message is the trigger; what follows the command is a reason and/or {action}.
		reason, spec, has := splitAction(args)
		var triggerText string
		if replied.Sticker != nil {
			var err error
			if triggerText, err = stickerTrigger(c.L, replied.Sticker); err != nil {
				c.Say(text.Esc(err))
				return nil
			}
		} else {
			triggerText = blacklist.EscapeWildcards(blacklist.CollapseSpace(messageText(replied)))
			if triggerText == "" {
				c.Say(text.Esc(c.T("blacklists.that_message_has_no_text_to")))
				return nil
			}
		}
		entries = append(entries, entry{triggerText, spec, reason, has})
	case args != "":
		for _, line := range blacklist.SplitLines(args) {
			if strings.TrimSpace(line) != "" {
				t, spec, has := splitAction(line)
				entries = append(entries, entry{unquote(t), spec, "", has})
			}
		}
	default:
		c.Say(c.T("blacklists.give_me_triggers_one_per_line"))
		return nil
	}

	var added, updated []string
	for _, e := range entries {
		pattern, err := blacklist.Check(c.L, e.text)
		var action string
		var seconds int
		var isNew, full bool
		if err == nil && e.spec != "" {
			action, seconds, err = ParseAction(c.L, e.spec)
		}
		reason := e.reason
		if err == nil {
			if len([]rune(reason)) > 200 {
				reason = text.Truncate(reason, 200)
			}
			isNew, full, err = c.App.AddTrigger(c.Ctx, c.L, c.Msg.Chat.Id, pattern, action, seconds, reason, c.User.Id)
		}
		if err != nil {
			msg, ok := asTriggerError(err)
			if !ok {
				return err
			}
			problems = append(problems, "<code>"+text.Esc(text.Truncate(e.text, 40))+"</code>: "+text.Esc(msg))
			continue
		}
		if full {
			fullText := c.T("common.the_blacklist_is_full_triggers", core.V{"MAX_TRIGGERS": blacklist.MaxTriggers})
			if !contains(problems, fullText) {
				problems = append(problems, fullText)
			}
			continue
		}
		shown := "<code>" + text.Esc(pattern) + "</code>"
		if action != "" {
			shown += " {" + text.Esc(blModeText(c.L, action, seconds)) + "}"
		}
		if isNew {
			added = append(added, shown)
		} else {
			updated = append(updated, shown)
		}
	}

	var lines []string
	for _, group := range []struct {
		label string
		items []string
	}{{c.T("common.added"), added}, {c.T("common.updated"), updated}} {
		if len(group.items) == 0 {
			continue
		}
		lines = append(lines, fmt.Sprintf("<b>%s %d trigger%s:</b>", group.label, len(group.items), plural(len(group.items))))
		lines = append(lines, bullets(group.items, maxShownAdded)...)
		if len(group.items) > maxShownAdded {
			lines = append(lines, fmt.Sprintf("…and %d more.", len(group.items)-maxShownAdded))
		}
	}
	if len(problems) > 0 {
		lines = append(lines, "<b>Skipped:</b>")
		lines = append(lines, bullets(problems, maxShownAdded)...)
	}
	c.Say(strings.Join(lines, "\n"))
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func bullets(items []string, limit int) []string {
	var out []string
	for i, item := range items {
		if i >= limit {
			break
		}
		out = append(out, "• "+item)
	}
	return out
}

func cmdUnblacklist(c *core.Ctx) error {
	if !adminOnly(c) {
		return nil
	}
	args := strings.TrimSpace(c.Args)
	replied := core.RepliedMessage(c.Msg)
	var texts []string
	switch {
	case args != "":
		for _, line := range blacklist.SplitLines(args) {
			if strings.TrimSpace(line) != "" {
				t, _, _ := splitAction(line)
				texts = append(texts, unquote(t))
			}
		}
	case replied != nil:
		if replied.Sticker != nil {
			t, err := stickerTrigger(c.L, replied.Sticker)
			if err != nil {
				c.Say(text.Esc(err))
				return nil
			}
			texts = []string{t}
		} else {
			texts = []string{blacklist.EscapeWildcards(blacklist.CollapseSpace(messageText(replied)))}
		}
	}
	if len(texts) == 0 {
		c.Say(c.T("blacklists.give_me_the_triggers_to_remove"))
		return nil
	}
	var removed, missing []string
	for _, t := range texts {
		pattern, err := blacklist.Check(c.L, t)
		if err != nil {
			missing = append(missing, "<code>"+text.Esc(text.Truncate(t, 40))+"</code>")
			continue
		}
		ok, err := c.App.RemoveTrigger(c.Ctx, c.Msg.Chat.Id, pattern)
		if err != nil {
			return err
		}
		item := "<code>" + text.Esc(pattern) + "</code>"
		if ok {
			removed = append(removed, item)
		} else {
			missing = append(missing, item)
		}
	}
	var lines []string
	if len(removed) > 0 {
		lines = append(lines, fmt.Sprintf("<b>Removed %d trigger%s:</b>", len(removed), plural(len(removed))))
		lines = append(lines, bullets(removed, maxShownAdded)...)
	}
	if len(missing) > 0 {
		lines = append(lines, "<b>Not in the blacklist:</b>")
		lines = append(lines, bullets(missing, maxShownAdded)...)
	}
	c.Say(strings.Join(lines, "\n"))
	return nil
}

func cmdBlacklistmode(c *core.Ctx) error {
	if !adminOnly(c) {
		return nil
	}
	cfg, err := c.App.BlacklistSettings(c.Ctx, c.Msg.Chat.Id)
	if err != nil {
		return err
	}
	opt := c.T("common.off")
	if cfg.DeleteMatches {
		opt = c.T("common.on")
	}
	c.Say(c.T("blacklists.default_action_delete_blacklisted_messages_c", core.V{
		"mode_text": text.Esc(blModeText(c.L, cfg.Mode, cfg.ModeSeconds)), "opt": opt,
	}))
	return nil
}

func cmdSetblacklistmode(c *core.Ctx) error {
	if !adminOnly(c) {
		return nil
	}
	spec := strings.TrimSpace(c.Args)
	usage := c.T("blacklists.use_setblacklistmode_lt_off_del_warn")
	if spec == "" {
		c.Say(usage)
		return nil
	}
	mode, seconds, err := parseAction(c.L, spec, true)
	if err != nil {
		if msg, ok := asTriggerError(err); ok {
			c.Say(text.Esc(msg) + "\n" + usage)
			return nil
		}
		return err
	}
	if err := c.App.SetBlacklistMode(c.Ctx, c.Msg.Chat.Id, mode, seconds); err != nil {
		return err
	}
	if mode == "off" {
		c.Say(c.T("blacklists.the_default_action_is_now_off"))
	} else {
		c.Say(c.T("blacklists.the_default_action_is_now", core.V{"mode_text": text.Esc(blModeText(c.L, mode, seconds))}))
	}
	return nil
}

func cmdBlocklistdelete(c *core.Ctx) error {
	if !adminOnly(c) {
		return nil
	}
	argument := strings.ToLower(strings.TrimSpace(c.Args))
	describe := func(on bool) string {
		if on {
			return c.T("blacklists.blacklisted_messages_will_be_deleted")
		}
		return c.T("blacklists.blacklisted_messages_will_be_kept_unless")
	}
	if argument == "" {
		cfg, err := c.App.BlacklistSettings(c.Ctx, c.Msg.Chat.Id)
		if err != nil {
			return err
		}
		c.Say(c.T("blacklists.use_blocklistdelete_on_or_off", core.V{"text": describe(cfg.DeleteMatches)}))
		return nil
	}
	on, ok := text.ParseSwitch(argument)
	if !ok {
		c.Say(c.T("blacklists.use_blocklistdelete_on_or_blocklistdelete_of"))
		return nil
	}
	if err := c.App.SetBlacklistDelete(c.Ctx, c.Msg.Chat.Id, on); err != nil {
		return err
	}
	c.Say(describe(on))
	return nil
}

func cmdUnblacklistall(c *core.Ctx) error {
	if !c.RequireChatOwner() {
		return nil
	}
	found, err := c.App.Triggers(c.Ctx, c.Msg.Chat.Id)
	if err != nil {
		return err
	}
	if len(found) == 0 {
		c.Say(c.T("blacklists.the_blacklist_of_this_chat_is"))
		return nil
	}
	_, err = c.Reply(c.T("blacklists.remove_all_triggers_of_this_chat", core.V{"count": len(found)}), core.Keyboard(
		[]gotgbot.InlineKeyboardButton{core.Button(c.T("common.remove_all"), cbBlacklistClear+"yes"), core.Button(c.T("common.cancel"), cbBlacklistClear+"no")}))
	return err
}

func cbUnblacklistall(c *core.Ctx) error {
	if c.Msg == nil {
		c.Answer(c.T("common.this_button_is_no_longer_valid"), true)
		return nil
	}
	if c.MemberStatus(c.Msg.Chat.Id, c.User.Id) != "creator" {
		c.Answer(c.T("common.only_the_owner_of_this_chat"), true)
		return nil
	}
	if c.Cb.Data == cbBlacklistClear+"yes" {
		removed, err := c.App.ClearTriggers(c.Ctx, c.Msg.Chat.Id)
		if err != nil {
			return err
		}
		c.Answer("", false)
		return c.Edit(c.T("blacklists.done_triggers_removed", core.V{"removed": removed}), nil)
	}
	c.Answer("", false)
	return c.Edit(c.T("blacklists.cancelled_the_blacklist_is_unchanged"), nil)
}

// --- Enforcement ---

type blHit struct {
	trigger *blacklist.Trigger
	action  string
	seconds int
	del     bool
}

// blacklisted finds the trigger a message matches, when its sender is not exempt.
func blacklisted(c *core.Ctx) (*blHit, error) {
	m, user := c.Msg, c.User
	if user == nil || user.IsBot || m.SenderChat != nil || !isGroup(m.Chat.Type) || m.IsAutomaticForward {
		return nil, nil
	}
	triggers, err := c.App.Triggers(c.Ctx, m.Chat.Id)
	if err != nil || len(triggers) == 0 {
		return nil, err
	}
	cfg, err := c.App.BlacklistSettings(c.Ctx, m.Chat.Id)
	if err != nil {
		return nil, err
	}
	var sticker *blacklist.Sticker
	if m.Sticker != nil {
		sticker = &blacklist.Sticker{Emoji: m.Sticker.Emoji, Pack: m.Sticker.SetName}
	}
	var hit *blHit
	blacklist.Find(triggers, messageText(m), sticker, func(t *blacklist.Trigger) bool {
		action := t.Action
		if action == "" {
			action = cfg.Mode
		}
		if action == "off" {
			return false
		}
		seconds := cfg.ModeSeconds
		if t.Action != "" {
			seconds = t.Seconds
		}
		hit = &blHit{trigger: t, action: action, seconds: seconds}
		return true
	})
	if hit == nil {
		return nil, nil
	}
	// Only now, with a match in hand, the (slower) checks of who is exempt.
	if core.IsImmune(user.Id) || c.Role != core.NoRole { // anybody with a role in the bot
		return nil, nil
	}
	if approved, err := c.App.IsApproved(c.Ctx, m.Chat.Id, user.Id); err != nil || approved {
		return nil, err
	}
	if c.IsChatAdmin(m.Chat.Id, user.Id) {
		return nil, nil
	}
	hit.del = hit.action == "del" || cfg.DeleteMatches
	return hit, nil
}

// blacklistWarn warns the sender; at the chat's warn limit, ban them and start the count over.
func blacklistWarn(c *core.Ctx, reason string) (string, error) {
	chatID, user := c.Msg.Chat.Id, c.User
	target := &core.Target{ID: user.Id, Name: core.FullName(user)}
	if _, err := addWarn(c, chatID, target, reason, &core.Actor{ID: c.Bot.Id, Name: c.T("blacklists.blacklist")}); err != nil {
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

func watchBlacklist(c *core.Ctx) (bool, error) {
	hit, err := blacklisted(c)
	if err != nil || hit == nil {
		return false, err
	}
	chatID, user := c.Msg.Chat.Id, c.User
	if hit.del {
		_, _ = c.Bot.DeleteMessageWithContext(c.Ctx, chatID, c.Msg.MessageId, nil)
	}
	if hit.action == "del" {
		return true, nil
	}

	reason := hit.trigger.Reason
	if reason == "" {
		reason = c.T("blacklists.blacklisted_trigger", core.V{"pattern": hit.trigger.Pattern})
	}
	var done string
	if hit.action == "warn" {
		done, err = blacklistWarn(c, reason)
	} else {
		done, err = c.Punish(chatID, user.Id, hit.action, hit.seconds)
	}
	if err != nil {
		var me *core.ModerationError
		if !errors.As(err, &me) {
			return true, err
		}
		if c.App.Cooldown(fmt.Sprintf("blacklist-failed:%d", chatID), failureCooldown) {
			_, _ = c.SendPlain(c.T("blacklists.used_a_blacklisted_trigger_but_i", core.V{
				"id": text.UserLink(user.Id, core.FullName(user)), "exc": text.Esc(me.Msg),
			}))
		}
		return true, nil
	}
	msg := c.T("blacklists.has_been", core.V{
		"id": text.UserLink(user.Id, core.FullName(user)), "done": done, "reason_line": c.ReasonLine(hit.trigger.Reason),
	})
	if hit.trigger.Reason == "" {
		msg += c.T("blacklists.reason_used_a_blacklisted_trigger")
	}
	_, err = c.SendPlain(msg)
	return true, err
}
