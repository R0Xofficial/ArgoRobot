package modules

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/blacklist"
	"argo/internal/core"
	"argo/internal/text"
)

// Keep spammers out by their profile (modules/joinblacklists.py).

const maxShownJoin = 20

var joinLabels = map[string]string{"name": "Names", "username": "Usernames", "bio": "Bios", "channel": "Channels"}

func registerJoinBlacklists(r *core.Router) {
	r.Watch(-40, "joinblacklists", watchJoinBlacklist) // before the greetings (which would welcome a spammer), after the global bans
	r.Command(cmdJoinBlacklists, "joinblacklists", "joinblacklist")
	for _, kind := range core.JoinKinds {
		kind := kind
		r.Command(func(c *core.Ctx) error { return joinAdd(c, kind) }, "blacklist"+kind)
		r.Command(func(c *core.Ctx) error { return joinRemove(c, kind) }, "unblacklist"+kind)
	}
}

func joinGroupOnly(c *core.Ctx) bool {
	if isGroup(c.Msg.Chat.Type) {
		return true
	}
	c.Say(c.T("common.this_command_only_works_in_groups"))
	return false
}

func cmdJoinBlacklists(c *core.Ctx) error {
	if !joinGroupOnly(c) {
		return nil
	}
	found, err := c.App.JoinPatterns(c.Ctx, c.Msg.Chat.Id)
	if err != nil {
		return err
	}
	name := c.Msg.Chat.Title
	if name == "" {
		name = c.T("common.this_chat")
	}
	title := c.T("joinblacklists.join_blacklists_in", core.V{"v": text.Esc(name)})
	if len(found) == 0 {
		c.Say(c.T("joinblacklists.there_are_none_yet_add_one", core.V{"title": title}))
		return nil
	}
	lines := []string{title}
	for _, kind := range core.JoinKinds {
		if len(found[kind]) == 0 {
			continue
		}
		lines = append(lines, "", "<b>"+joinLabels[kind]+"</b>")
		for _, p := range found[kind] {
			lines = append(lines, "• <code>"+text.Esc(p)+"</code>")
		}
	}
	c.Say(strings.Join(lines, "\n"))
	return nil
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

func joinAdd(c *core.Ctx, kind string) error {
	if !joinGroupOnly(c) || c.BeginChatCommand(core.AuthorizeOpts{Right: rightChangeInfo, NoStaff: true}) == nil {
		return nil
	}
	texts := nonEmptyLines(c.Args)
	if len(texts) == 0 {
		c.Say(c.T("joinblacklists.give_me_triggers_one_per_line", core.V{"kind": kind}))
		return nil
	}
	var added, problems []string
	for _, t := range texts {
		pattern, err := core.JoinCheck(c.L, kind, t)
		if err != nil {
			var be *blacklist.Error
			if !errors.As(err, &be) {
				return err
			}
			problems = append(problems, "<code>"+text.Esc(text.Truncate(t, 40))+"</code>: "+text.Esc(be.Msg))
			continue
		}
		outcome, err := c.App.JoinAdd(c.Ctx, c.Msg.Chat.Id, kind, pattern)
		if err != nil {
			return err
		}
		switch outcome {
		case "added":
			added = append(added, "<code>"+text.Esc(pattern)+"</code>")
		case "exists":
			problems = append(problems, "<code>"+text.Esc(pattern)+"</code>: already in the list.")
		default:
			note := c.T("joinblacklists.the_list_is_full_triggers_remove", core.V{"kind": kind, "MAX_PER_KIND": core.MaxJoinPerKind})
			if !contains(problems, note) {
				problems = append(problems, note)
			}
		}
	}
	var lines []string
	if len(added) > 0 {
		lines = append(lines, fmt.Sprintf("<b>Added %d %s trigger%s:</b>", len(added), kind, plural(len(added))))
		for _, a := range added[:min(len(added), maxShownJoin)] {
			lines = append(lines, "• "+a)
		}
	}
	if len(problems) > 0 {
		lines = append(lines, "<b>Skipped:</b>")
		for _, p := range problems[:min(len(problems), maxShownJoin)] {
			lines = append(lines, "• "+p)
		}
	}
	c.Say(strings.Join(lines, "\n"))
	return nil
}

func joinRemove(c *core.Ctx, kind string) error {
	if !joinGroupOnly(c) || c.BeginChatCommand(core.AuthorizeOpts{Right: rightChangeInfo, NoStaff: true}) == nil {
		return nil
	}
	texts := nonEmptyLines(c.Args)
	if len(texts) == 0 {
		c.Say(c.T("joinblacklists.give_me_the_triggers_to_remove", core.V{"kind": kind}))
		return nil
	}
	var removed, missing []string
	for _, t := range texts {
		pattern, err := core.JoinCheck(c.L, kind, t)
		if err != nil {
			missing = append(missing, "<code>"+text.Esc(text.Truncate(t, 40))+"</code>")
			continue
		}
		shown := "<code>" + text.Esc(pattern) + "</code>"
		ok, err := c.App.JoinRemove(c.Ctx, c.Msg.Chat.Id, kind, pattern)
		if err != nil {
			return err
		}
		if ok {
			removed = append(removed, shown)
		} else {
			missing = append(missing, shown)
		}
	}
	var lines []string
	if len(removed) > 0 {
		lines = append(lines, fmt.Sprintf("<b>Removed %d %s trigger%s:</b>", len(removed), kind, plural(len(removed))))
		for _, r := range removed[:min(len(removed), maxShownJoin)] {
			lines = append(lines, "• "+r)
		}
	}
	if len(missing) > 0 {
		lines = append(lines, fmt.Sprintf("<b>Not in the %s list:</b>", kind))
		for _, m := range missing[:min(len(missing), maxShownJoin)] {
			lines = append(lines, "• "+m)
		}
	}
	c.Say(strings.Join(lines, "\n"))
	return nil
}

// --- Checking who joins --------------------------------------------------------------------

// profileMatch is (kind, trigger) of the first part of this person's profile that is blacklisted.
func profileMatch(c *core.Ctx, chatID int64, user *gotgbot.User) (string, string) {
	if hit := c.App.JoinMatch(c.Ctx, chatID, "name", core.FullName(user)); hit != "" {
		return "name", hit
	}
	if hit := c.App.JoinMatch(c.Ctx, chatID, "username", user.Username); hit != "" {
		return "username", hit
	}
	wantsBio, wantsChannel := c.App.JoinHas(c.Ctx, chatID, "bio"), c.App.JoinHas(c.Ctx, chatID, "channel")
	if !wantsBio && !wantsChannel {
		return "", ""
	}
	full, err := c.Bot.GetChatWithContext(c.Ctx, user.Id, nil)
	if err != nil {
		return "", "" // a profile that can't be read is not a reason to ban anyone
	}
	if wantsBio {
		if hit := c.App.JoinMatch(c.Ctx, chatID, "bio", full.Bio); hit != "" {
			return "bio", hit
		}
	}
	if wantsChannel && full.PersonalChat != nil {
		for _, t := range []string{full.PersonalChat.Title, full.PersonalChat.Username} {
			if hit := c.App.JoinMatch(c.Ctx, chatID, "channel", t); hit != "" {
				return "channel", hit
			}
		}
	}
	return "", ""
}

// joinExempt: bots, immune accounts, the bot's staff, approved users, chat admins and people an admin added.
func joinExempt(c *core.Ctx, m *gotgbot.Message, user *gotgbot.User) bool {
	chatID := m.Chat.Id
	if user.IsBot || core.IsImmune(user.Id) || c.App.GetRole(c.Ctx, user.Id) != core.NoRole {
		return true
	}
	if ok, _ := c.App.IsApproved(c.Ctx, chatID, user.Id); ok || c.IsChatAdmin(chatID, user.Id) {
		return true
	}
	return m.From != nil && m.From.Id != user.Id && c.IsChatAdmin(chatID, m.From.Id)
}

func watchJoinBlacklist(c *core.Ctx) (bool, error) {
	m := c.Msg
	if m == nil || c.Cb != nil || !isGroup(m.Chat.Type) || len(m.NewChatMembers) == 0 {
		return false, nil
	}
	chatID := m.Chat.Id
	if patterns, _ := c.App.JoinPatterns(c.Ctx, chatID); len(patterns) == 0 {
		return false, nil
	}
	type hit struct {
		user          *gotgbot.User
		kind, trigger string
	}
	var found []hit
	for i := range m.NewChatMembers {
		u := &m.NewChatMembers[i]
		if u.IsBot || u.Id == c.Bot.Id {
			continue
		}
		if kind, trigger := profileMatch(c, chatID, u); kind != "" && !joinExempt(c, m, u) {
			found = append(found, hit{u, kind, trigger})
		}
	}
	if len(found) == 0 {
		return false, nil
	}
	banned := false
	for _, h := range found {
		if _, err := c.Punish(chatID, h.user.Id, "ban", 0); err != nil {
			slog.Info("Join blacklist: could not ban", "user", h.user.Id, "chat", chatID, "err", err)
			continue
		}
		banned = true
		slog.Info("Join blacklist: banned", "user", h.user.Id, "chat", chatID, "kind", h.kind, "trigger", h.trigger)
	}
	clean := false
	if s, err := c.App.Greetings(c.Ctx, chatID); err == nil {
		clean = s.CleanService // the greetings handler never sees this message
	}
	if banned || clean {
		_, _ = c.Bot.DeleteMessageWithContext(c.Ctx, chatID, m.MessageId, nil)
	}
	return true, nil
}
