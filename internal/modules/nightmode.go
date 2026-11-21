package modules

import (
	"errors"
	"strings"
	"time"

	"argo/internal/core"
	"argo/internal/text"
)

// Nightmode: close the chat for the night (modules/nightmode.py). The work is in core/nightmode.go.

func registerNightmode(r *core.Router) {
	r.Command(cmdNightmode, "nightmode")
	r.Command(cmdNighthours, "nighthours")
	r.Command(cmdNighttz, "nighttz")
}

func nightSummary(c *core.Ctx, s core.NightSettings) string {
	night := s.Enabled && s.IsNight(time.Now())
	now := time.Now().In(core.ZoneOf(s.Zone))
	opt, opt2 := c.T("common.off"), c.T("nightmode.open")
	if s.Enabled {
		opt = c.T("common.on")
	}
	if s.Active {
		opt2 = c.T("nightmode.closed")
	}
	out := c.T("nightmode.nightmode_enabled_night_to_time_zone", core.V{
		"opt": opt, "clock_text": core.ClockText(s.Start), "clock_text2": core.ClockText(s.End),
		"zone": text.Esc(s.Zone), "now": now, "opt2": opt2,
	})
	if night && !s.Active {
		out += c.T("nightmode.the_night_has_begun_i_close")
	}
	return out
}

func nightAllowed(c *core.Ctx) bool {
	if !isGroup(c.Msg.Chat.Type) {
		c.Say(c.T("common.this_command_only_works_in_groups"))
		return false
	}
	return c.BeginChatCommand(core.AuthorizeOpts{}) != nil
}

// nightApply closes or opens the chat now when the clock says so; the sweep would do it within half a minute anyway.
// The returned sentence is the problem, if there was one.
func nightApply(c *core.Ctx) string {
	chatID := c.Msg.Chat.Id
	what, err := c.App.ReconcileNight(c.Ctx, c.Bot, chatID, nil)
	if err != nil {
		var me *core.ModerationError
		if errors.As(err, &me) {
			return me.Msg
		}
		reason := err.Error()
		if tg := core.TgError(err); tg != nil {
			reason = tg.Description
		}
		return c.T("nightmode.i_can_t_change_the_permissions", core.V{"getattr": reason})
	}
	if what != "" {
		if s, err := c.App.NightSettings(c.Ctx, chatID); err == nil {
			c.App.AnnounceNight(c.Ctx, c.Bot, chatID, what, s)
		}
	}
	return ""
}

func cmdNightmode(c *core.Ctx) error {
	if !nightAllowed(c) {
		return nil
	}
	chatID := c.Msg.Chat.Id
	arg := strings.TrimSpace(c.Args)
	problem := ""
	if arg != "" {
		wanted, ok := text.ParseSwitch(arg)
		if !ok {
			c.Say(c.T("nightmode.use_nightmode_on_or_nightmode_off"))
			return nil
		}
		if err := c.App.UpdateNight(c.Ctx, chatID, map[string]any{"enabled": wanted}); err != nil {
			return err
		}
		problem = nightApply(c)
	}
	s, err := c.App.NightSettings(c.Ctx, chatID)
	if err != nil {
		return err
	}
	out := nightSummary(c, s)
	if problem != "" {
		out += "\n" + text.Esc(problem)
	}
	c.Say(out)
	return nil
}

func cmdNighthours(c *core.Ctx) error {
	if !nightAllowed(c) {
		return nil
	}
	words := strings.Fields(strings.ReplaceAll(strings.ReplaceAll(c.Args, "-", " "), "to", " "))
	start, end, ok := 0, 0, false
	if len(words) == 2 {
		var ok1, ok2 bool
		start, ok1 = core.ParseClock(words[0])
		end, ok2 = core.ParseClock(words[1])
		ok = ok1 && ok2
	}
	if !ok || start == end {
		c.Say(c.T("nightmode.use_nighthours_lt_from_gt_lt"))
		return nil
	}
	chatID := c.Msg.Chat.Id
	if err := c.App.UpdateNight(c.Ctx, chatID, map[string]any{"start": start, "end": end}); err != nil {
		return err
	}
	problem := nightApply(c)
	s, err := c.App.NightSettings(c.Ctx, chatID)
	if err != nil {
		return err
	}
	out := c.T("nightmode.the_night_now_lasts_from_to", core.V{
		"clock_text": core.ClockText(start), "clock_text2": core.ClockText(end), "zone": text.Esc(s.Zone),
	})
	if problem != "" {
		out += "\n" + text.Esc(problem)
	}
	c.Say(out)
	return nil
}

func cmdNighttz(c *core.Ctx) error {
	if !nightAllowed(c) {
		return nil
	}
	name, _, ok := core.ParseZone(c.Args)
	if !ok {
		c.Say(c.T("nightmode.use_nighttz_lt_zone_gt_a"))
		return nil
	}
	if err := c.App.UpdateNight(c.Ctx, c.Msg.Chat.Id, map[string]any{"zone": name}); err != nil {
		return err
	}
	out := c.T("nightmode.the_hours_of_the_night_are", core.V{"name": text.Esc(name)})
	if problem := nightApply(c); problem != "" {
		out += "\n" + text.Esc(problem)
	}
	c.Say(out)
	return nil
}
