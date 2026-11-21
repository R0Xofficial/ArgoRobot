package modules

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/core"
	"argo/internal/text"
)

// Antiraid: stop a flood of new accounts (modules/antiraid.py). The counting is in core/antiraid.go.

const raidFailureCD = 300 * time.Second

func registerAntiraid(r *core.Router) {
	r.Command(cmdAntiraid, "antiraid")
	r.Command(cmdRaidlimit, "raidlimit")
	r.Command(cmdRaidtime, "raidtime")
	r.Command(cmdRaidmode, "raidmode")
	// Before the modules that greet and test newcomers, so that they can tell who the raid took away. Neither of
	// them takes the event away from the others.
	r.Watch(-90, "antiraid", watchRaidJoin)
	r.WatchMembers(-90, "antiraid", watchRaidMember)
}

func raidModeText(c *core.Ctx, mode string) string {
	switch mode {
	case "mute":
		return c.T("antiraid.muted_until_the_raid_is_over")
	case "kick":
		return "kicked"
	}
	return "banned"
}

func raidSummary(c *core.Ctx, s core.RaidSettings) string {
	opt := c.T("common.off")
	if s.Enabled {
		opt = c.T("common.on")
	}
	out := c.T("antiraid.antiraid_enabled_raid_joins_within_lasts", core.V{
		"opt": opt, "joins": s.Joins, "duration_text": text.DurationText(c.L, s.Window),
		"duration_text2": text.DurationText(c.L, s.Duration), "mode_text": raidModeText(c, s.Mode),
	})
	if s.Raging(time.Now()) {
		left := int(s.Until - float64(time.Now().UnixNano())/1e9)
		out += c.T("antiraid.raid_running_ends_in", core.V{"duration_text": text.DurationText(c.L, left)})
	}
	return out
}

// --- The raid itself ---

// raidReact: newcomers came; count them, and say so when a raid starts. A failure never reaches the caller.
func raidReact(c *core.Ctx, chat gotgbot.Chat, users []gotgbot.User) {
	result, err := c.RaidJoined(chat.Id, users)
	if err != nil {
		var me *core.ModerationError
		if !errors.As(err, &me) {
			slog.Error("Antiraid failed", "chat", chat.Id, "err", err)
			return
		}
		if c.App.Cooldown(fmt.Sprintf("raid-failed:%d", chat.Id), raidFailureCD) {
			raidSay(c, chat.Id, c.T("antiraid.antiraid_is_on_but_i_can", core.V{"exc": text.Esc(me.Msg)}))
		}
		return
	}
	if result == nil || !result.Started {
		return
	}
	s := result.Settings
	raidSay(c, chat.Id, c.T("antiraid.raid_detected_accounts_joined_within_for", core.V{
		"people": len(result.People), "duration_text": text.DurationText(c.L, s.Window),
		"duration_text2": text.DurationText(c.L, s.Duration), "mode_text": raidModeText(c, s.Mode),
	}))
}

func raidSay(c *core.Ctx, chatID int64, msg string) {
	if err := c.SayTo(chatID, msg); err != nil {
		slog.Warn("Antiraid could not write to chat", "chat", chatID)
	}
}

// watchRaidJoin looks at every "joined" message before the handlers do, without taking it away from them.
func watchRaidJoin(c *core.Ctx) (bool, error) {
	m := c.Msg
	if c.Cb != nil || c.Edited || m == nil || !isGroup(m.Chat.Type) || len(m.NewChatMembers) == 0 {
		return false, nil
	}
	raidReact(c, m.Chat, m.NewChatMembers)
	return false, nil
}

func memberPresent(m gotgbot.MergedChatMember) bool {
	switch m.Status {
	case "member", "administrator", "creator":
		return true
	case "restricted":
		return m.IsMember
	}
	return false
}

// watchRaidMember is the same for the member updates: in a chat with hidden members there is no "joined" message.
func watchRaidMember(c *core.Ctx, e *gotgbot.ChatMemberUpdated) (bool, error) {
	if !isGroup(e.Chat.Type) {
		return false, nil
	}
	old, now := e.OldChatMember.MergeChatMember(), e.NewChatMember.MergeChatMember()
	if !memberPresent(old) && memberPresent(now) && now.Status != "creator" {
		raidReact(c, e.Chat, []gotgbot.User{now.User})
	}
	return false, nil
}

// --- Commands ---

func raidAllowed(c *core.Ctx) bool {
	if !isGroup(c.Msg.Chat.Type) {
		c.Say(c.T("common.this_command_only_works_in_groups"))
		return false
	}
	return c.BeginChatCommand(core.AuthorizeOpts{}) != nil
}

func cmdAntiraid(c *core.Ctx) error {
	if !raidAllowed(c) {
		return nil
	}
	chatID := c.Msg.Chat.Id
	if arg := strings.TrimSpace(c.Args); arg != "" {
		wanted, ok := text.ParseSwitch(arg)
		if !ok {
			c.Say(c.T("antiraid.use_antiraid_on_or_antiraid_off"))
			return nil
		}
		// A running raid ends first: once the antiraid is off there is no raid to end (the Python version asks
		// after switching it off, and so leaves the raid waiting for the next time it is switched on).
		if !wanted {
			if _, err := c.App.EndRaid(c.Ctx, chatID); err != nil {
				return err
			}
		}
		if err := c.App.UpdateRaid(c.Ctx, chatID, map[string]any{"enabled": wanted}); err != nil {
			return err
		}
	}
	s, err := c.App.RaidSettings(c.Ctx, chatID)
	if err != nil {
		return err
	}
	c.Say(raidSummary(c, s))
	return nil
}

// raidSeconds: 30s, 2m, 1h, 1d -> seconds.
func raidSeconds(s string, allowSeconds bool) (int, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if allowSeconds && strings.HasSuffix(s, "s") {
		if n, err := strconv.Atoi(s[:len(s)-1]); err == nil && isDigits(s[:len(s)-1]) {
			return n, true
		}
	}
	d, ok := core.ParseDuration(s)
	return int(d.Seconds()), ok
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func cmdRaidlimit(c *core.Ctx) error {
	if !raidAllowed(c) {
		return nil
	}
	words := strings.Fields(c.Args)
	usage := c.T("antiraid.use_raidlimit_lt_joins_gt_lt", core.V{"MIN_JOINS": core.RaidMinJoins, "MAX_JOINS": core.RaidMaxJoins})
	if len(words) != 2 || !isDigits(words[0]) {
		c.Say(usage)
		return nil
	}
	joins, _ := strconv.Atoi(words[0])
	window, ok := raidSeconds(words[1], true)
	if joins < core.RaidMinJoins || joins > core.RaidMaxJoins || !ok || window < core.RaidMinWindow || window > core.RaidMaxWindow {
		c.Say(usage)
		return nil
	}
	if err := c.App.UpdateRaid(c.Ctx, c.Msg.Chat.Id, map[string]any{"joins": joins, "window": window}); err != nil {
		return err
	}
	c.Say(c.T("antiraid.a_raid_now_starts_when_accounts", core.V{"joins": joins, "duration_text": text.DurationText(c.L, window)}))
	return nil
}

func cmdRaidtime(c *core.Ctx) error {
	if !raidAllowed(c) {
		return nil
	}
	d, ok := raidSeconds(c.Args, false)
	if !ok || d < core.RaidMinDuration || d > core.RaidMaxDuration {
		c.Say(c.T("antiraid.use_raidtime_lt_time_gt_from"))
		return nil
	}
	if err := c.App.UpdateRaid(c.Ctx, c.Msg.Chat.Id, map[string]any{"duration": d}); err != nil {
		return err
	}
	c.Say(c.T("antiraid.a_raid_now_lasts", core.V{"duration_text": text.DurationText(c.L, d)}))
	return nil
}

func cmdRaidmode(c *core.Ctx) error {
	if !raidAllowed(c) {
		return nil
	}
	mode := strings.ToLower(strings.TrimSpace(c.Args))
	if !contains(core.RaidModes, mode) {
		c.Say(c.T("antiraid.use_raidmode_lt_mute_kick_ban"))
		return nil
	}
	if err := c.App.UpdateRaid(c.Ctx, c.Msg.Chat.Id, map[string]any{"mode": mode}); err != nil {
		return err
	}
	c.Say(c.T("antiraid.during_a_raid_newcomers_are_now", core.V{"mode_text": raidModeText(c, mode)}))
	return nil
}
