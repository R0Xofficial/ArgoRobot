package modules

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"argo/internal/core"
	"argo/internal/text"
)

// Flood control (modules/antiflood.py). It is off until a chat admin sets a limit with /setflood. Bots are
// ignored, and so are chat admins, approved users and the bot's staff.

const (
	floodRight      = "can_change_info" // the admin right needed to change these settings
	floodCooldown   = 300 * time.Second // between "I can't act" complaints in the same chat
	floodPriority   = -60               // after global bans (-100), before the other modules
	spanMaxSeconds  = core.FloodMaxSeconds
	spanMinSeconds  = core.FloodMinSeconds
	floodCountLabel = "flood-failed:%d"
)

var spanRE = regexp.MustCompile(`(?i)^(\d{1,6})([smh]?)$`)

func registerAntiflood(r *core.Router) {
	r.Command(cmdFlood, "flood")
	r.Command(cmdSetflood, "setflood")
	r.Command(cmdSetfloodtimer, "setfloodtimer")
	r.Command(cmdSetfloodmode, "setfloodmode")
	r.Command(cmdClearflood, "clearflood")
	r.Command(cmdAntichannel, "antichannel")
	r.Watch(floodPriority, "flood", watchFlood)
	r.Watch(floodPriority+1, "antichannel", watchChannel)
}

func floodModeText(c *core.Ctx, s core.FloodSettings) string {
	if core.IsTimedMode(s.Mode) {
		return c.T("antiflood.for", core.V{"mode": c.L.Word(s.Mode), "span_text": text.DurationText(c.L, s.ModeSeconds)})
	}
	return c.L.Word(s.Mode)
}

func onOff(c *core.Ctx, on bool) string {
	if on {
		return c.T("common.on")
	}
	return c.T("common.off")
}

// parseSpan: 10s, 2m, 1h (a bare number is seconds) -> seconds.
func parseSpan(s string) (int, bool) {
	m := spanRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, false
	}
	n, _ := strconv.Atoi(m[1])
	switch strings.ToLower(m[2]) {
	case "m":
		n *= 60
	case "h":
		n *= 3600
	}
	return n, n >= spanMinSeconds && n <= spanMaxSeconds
}

func parseCount(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil && n >= core.FloodMinCount && n <= core.FloodMaxCount
}

func floodText(c *core.Ctx, title string, s core.FloodSettings) string {
	lines := []string{c.T("antiflood.flood_control_in", core.V{"chat_title": text.Esc(title)}), ""}
	if !s.Active() {
		lines = append(lines, "Flood control is <b>off</b>.")
	} else {
		if s.Limit != 0 {
			lines = append(lines, c.T("antiflood.limit_more_than_messages_in_a", core.V{"limit": s.Limit}))
		} else {
			lines = append(lines, c.T("antiflood.limit_off"))
		}
		if s.TimerCount != 0 {
			lines = append(lines, c.T("antiflood.timer_messages_within", core.V{"timer": s.TimerCount, "span_text": text.DurationText(c.L, s.TimerSeconds)}))
		} else {
			lines = append(lines, c.T("antiflood.timer_off"))
		}
		lines = append(lines, "Action: <b>"+text.Esc(floodModeText(c, s))+"</b>")
		lines = append(lines, "Delete all flood messages: <b>"+onOff(c, s.Clear)+"</b>")
	}
	lines = append(lines, "Anti-channel: <b>"+onOff(c, s.Antichannel)+"</b>")
	return strings.Join(lines, "\n")
}

// floodAdmin: settings are for chat admins and, unless the chat's owner turned it off, the bot's staff.
func floodAdmin(c *core.Ctx) bool {
	return c.BeginChatCommand(core.AuthorizeOpts{Right: floodRight}) != nil
}

func cmdFlood(c *core.Ctx) error {
	if !core.IsGroup(c.Msg.Chat) {
		c.Say(c.T("common.this_command_only_works_in_groups"))
		return nil
	}
	s, err := c.App.FloodSettings(c.Ctx, c.Msg.Chat.Id)
	if err != nil {
		return err
	}
	title := c.Msg.Chat.Title
	if title == "" {
		title = c.T("common.this_chat")
	}
	c.Say(floodText(c, title, s))
	return nil
}

func cmdSetflood(c *core.Ctx) error {
	if !floodAdmin(c) {
		return nil
	}
	arg := strings.ToLower(strings.TrimSpace(c.Args))
	usage := c.T("antiflood.use_setflood_lt_count_gt_to", core.V{"MIN_COUNT": core.FloodMinCount, "MAX_COUNT": core.FloodMaxCount})
	if on, ok := text.ParseSwitch(arg); (ok && !on) || arg == "0" {
		if _, err := c.App.UpdateFlood(c.Ctx, c.Msg.Chat.Id, map[string]any{"flood_limit": 0}); err != nil {
			return err
		}
		c.Say(c.T("antiflood.the_limit_on_messages_in_a"))
		return nil
	}
	n, ok := parseCount(arg)
	if !ok {
		c.Say(usage)
		return nil
	}
	if _, err := c.App.UpdateFlood(c.Ctx, c.Msg.Chat.Id, map[string]any{"flood_limit": n}); err != nil {
		return err
	}
	c.Say(c.T("antiflood.flood_control_will_act_on_anyone", core.V{"count": n}))
	return nil
}

func cmdSetfloodtimer(c *core.Ctx) error {
	if !floodAdmin(c) {
		return nil
	}
	parts := strings.Fields(strings.ToLower(c.Args))
	chatID := c.Msg.Chat.Id
	if len(parts) == 1 {
		if on, ok := text.ParseSwitch(parts[0]); ok && !on {
			if _, err := c.App.UpdateFlood(c.Ctx, chatID, map[string]any{"timer_count": 0, "timer_seconds": 0}); err != nil {
				return err
			}
			c.Say(c.T("antiflood.the_timer_is_now_off"))
			return nil
		}
	}
	var count, seconds int
	var ok1, ok2 bool
	if len(parts) == 2 {
		count, ok1 = parseCount(parts[0])
		seconds, ok2 = parseSpan(parts[1])
	}
	if !ok1 || !ok2 {
		c.Say(c.T("antiflood.use_setfloodtimer_lt_count_gt_lt", core.V{"MIN_COUNT": core.FloodMinCount, "MAX_COUNT": core.FloodMaxCount}))
		return nil
	}
	if _, err := c.App.UpdateFlood(c.Ctx, chatID, map[string]any{"timer_count": count, "timer_seconds": seconds}); err != nil {
		return err
	}
	c.Say(c.T("antiflood.flood_control_will_act_on_anyone_2", core.V{"count": count, "span_text": text.DurationText(c.L, seconds)}))
	return nil
}

func cmdSetfloodmode(c *core.Ctx) error {
	if !floodAdmin(c) {
		return nil
	}
	parts := strings.Fields(strings.ToLower(c.Args))
	if len(parts) == 0 || !core.IsEnforceMode(parts[0]) || len(parts) > 2 {
		c.Say(c.T("antiflood.use_setfloodmode_lt_ban_kick_mute"))
		return nil
	}
	mode := parts[0]
	seconds := 0
	if core.IsTimedMode(mode) {
		var d time.Duration
		ok := false
		if len(parts) == 2 {
			d, ok = core.ParseDuration(parts[1])
		}
		if !ok || d < core.MinDuration || d > core.MaxDuration {
			c.Say(c.T("antiflood.needs_a_duration_like_30m_2h", core.V{"mode": mode}))
			return nil
		}
		seconds = int(d.Seconds())
	} else if len(parts) == 2 {
		c.Say(c.T("antiflood.doesn_t_take_a_duration_use", core.V{"mode": mode}))
		return nil
	}
	s, err := c.App.UpdateFlood(c.Ctx, c.Msg.Chat.Id, map[string]any{"mode": mode, "mode_seconds": seconds})
	if err != nil {
		return err
	}
	c.Say(c.T("antiflood.flood_control_will_now_anyone_who", core.V{"mode_text": text.Esc(floodModeText(c, s))}))
	return nil
}

func floodToggle(c *core.Ctx, column, usage string, say func(bool) string, current func(core.FloodSettings) bool) error {
	if !floodAdmin(c) {
		return nil
	}
	arg := strings.ToLower(strings.TrimSpace(c.Args))
	if arg == "" {
		s, err := c.App.FloodSettings(c.Ctx, c.Msg.Chat.Id)
		if err != nil {
			return err
		}
		c.Say(say(current(s)) + "\n" + usage)
		return nil
	}
	on, ok := text.ParseSwitch(arg)
	if !ok {
		c.Say(usage)
		return nil
	}
	if _, err := c.App.UpdateFlood(c.Ctx, c.Msg.Chat.Id, map[string]any{column: on}); err != nil {
		return err
	}
	c.Say(say(on))
	return nil
}

func cmdClearflood(c *core.Ctx) error {
	return floodToggle(c, "clear_flood", c.T("antiflood.use_clearflood_on_or_clearflood_off"),
		func(on bool) string {
			if on {
				return c.T("antiflood.the_messages_that_trigger_flood_control")
			}
			return c.T("antiflood.the_messages_that_trigger_flood_control_2")
		}, func(s core.FloodSettings) bool { return s.Clear })
}

func cmdAntichannel(c *core.Ctx) error {
	return floodToggle(c, "antichannel", c.T("antiflood.use_antichannel_on_or_antichannel_off"),
		func(on bool) string {
			if on {
				return c.T("antiflood.anti_channel_is_on_anyone_speaking")
			}
			return c.T("antiflood.anti_channel_is_off")
		}, func(s core.FloodSettings) bool { return s.Antichannel })
}

// --- Enforcement ---------------------------------------------------------------------------

func serviceMessage(c *core.Ctx) bool {
	m := c.Msg
	return len(m.NewChatMembers) > 0 || m.LeftChatMember != nil || m.PinnedMessage != nil || m.IsAutomaticForward
}

func watchFlood(c *core.Ctx) (bool, error) {
	m, user := c.Msg, c.User
	if c.Edited || c.Cb != nil || user == nil || user.IsBot || m.SenderChat != nil || !core.IsGroup(m.Chat) || serviceMessage(c) {
		return false, nil
	}
	s, err := c.App.FloodSettings(c.Ctx, m.Chat.Id)
	if err != nil || !s.Active() {
		return false, err
	}
	ids := c.App.RecordFlood(s, m.Chat.Id, user.Id, m.MessageId, time.Now(), m.MediaGroupId)
	if ids == nil {
		return false, nil
	}
	// Only now, when someone is over the limit, the (slow) checks of who is exempt.
	if core.IsImmune(user.Id) || c.Role != core.NoRole {
		return false, nil
	}
	if approved, err := c.App.IsApproved(c.Ctx, m.Chat.Id, user.Id); err != nil || approved {
		return false, err
	}
	if c.IsChatAdmin(m.Chat.Id, user.Id) {
		return false, nil
	}

	var doomed []int64
	if s.Clear {
		doomed = append(doomed, ids...) // the whole flood
	} else {
		// Always the messages over the limit: the one that tripped it, and any beyond it.
		if s.Limit != 0 && len(ids) > s.Limit {
			doomed = append(doomed, ids[s.Limit:]...)
		}
		found := false
		for _, id := range doomed {
			found = found || id == m.MessageId
		}
		if !found {
			doomed = append(doomed, m.MessageId)
		}
	}
	done, perr := c.Punish(m.Chat.Id, user.Id, s.Mode, s.ModeSeconds)
	if len(doomed) > 0 {
		_, _ = c.Bot.DeleteMessagesWithContext(c.Ctx, m.Chat.Id, doomed, nil)
	}
	who := text.UserLink(user.Id, core.FullName(user))
	if perr != nil {
		var me *core.ModerationError
		if !errors.As(perr, &me) {
			return true, perr
		}
		if c.App.Cooldown(fmt.Sprintf(floodCountLabel, m.Chat.Id), floodCooldown) {
			c.Say(c.T("antiflood.is_flooding_the_chat_but_i", core.V{"id": who, "failure": text.Esc(me.Msg)}))
		}
		return true, nil
	}
	_, err = c.SendPlain(c.T("antiflood.flood_control_has_been", core.V{"id": who, "done": done}))
	return true, err
}

// watchChannel handles a message written as a channel in a chat that doesn't allow that.
func watchChannel(c *core.Ctx) (bool, error) {
	m, ch := c.Msg, c.Msg.SenderChat
	if c.Edited || c.Cb != nil || ch == nil || !core.IsGroup(m.Chat) || ch.Id == m.Chat.Id || m.IsAutomaticForward {
		return false, nil
	}
	s, err := c.App.FloodSettings(c.Ctx, m.Chat.Id)
	if err != nil || !s.Antichannel {
		return false, err
	}
	_, _ = c.Bot.DeleteMessageWithContext(c.Ctx, m.Chat.Id, m.MessageId, nil)
	_, err = c.Bot.BanChatSenderChatWithContext(c.Ctx, m.Chat.Id, ch.Id, nil)
	if err = c.Perform(err); err != nil {
		var me *core.ModerationError
		if !errors.As(err, &me) {
			return true, err
		}
		if c.App.Cooldown(fmt.Sprintf(floodCountLabel, m.Chat.Id), floodCooldown) {
			c.Say(c.T("antiflood.anti_channel_is_on_but_i", core.V{"exc": text.Esc(me.Msg)}))
		}
		return true, nil
	}
	title := ch.Title
	if title == "" {
		title = c.T("antiflood.a_channel")
	}
	title = text.Esc(title)
	name := "<b>" + title + "</b>"
	if ch.Username != "" {
		name = fmt.Sprintf(`<a href="https://t.me/%s">%s</a>`, text.Esc(ch.Username), title)
	}
	_, err = c.SendPlain(c.T("antiflood.has_been_banned_speaking_as_a", core.V{"name": name}))
	return true, err
}
