package modules

import (
	"strconv"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/core"
	"argo/internal/text"
)

// Captchas for new members (modules/captcha.py). The tests and the flow are in core/captcha.go.

const (
	maxJoined     = 10 // people tested for one "joined" message
	maxButtonText = 40
)

func registerCaptcha(r *core.Router) {
	core.RegisterInfo("captcha", false, func(c *core.Ctx, userID, chatID int64) []core.InfoFlag {
		if chatID == 0 {
			return nil
		}
		row, err := c.App.CaptchaFind(c.Ctx, chatID, userID)
		return []core.InfoFlag{{Label: c.T("captcha.captcha_pending"), Value: err == nil && row != nil && row.Kind == "join"}}
	})
	r.Command(cmdCaptcha, "captcha")
	r.Command(cmdCaptchamode, "captchamode")
	r.Command(cmdCaptcharules, "captcharules")
	r.Command(cmdCaptchamutetime, "captchamutetime")
	r.Command(cmdCaptchakick, "captchakick")
	r.Command(cmdCaptchakicktime, "captchakicktime")
	r.Command(cmdSetcaptchatext, "setcaptchatext")
	r.Command(cmdResetcaptchatext, "resetcaptchatext")
	r.Button(core.CaptchaPrefix+":", cbCaptchaTest)
	r.Button(core.CaptchaWelcomePrefix+":", cbCaptchaWelcome)
	r.Button(core.CaptchaRulesPrefix+":", cbCaptchaRules)
	// Joins are looked at before the other modules (welcome messages wait for the captcha). The watcher never takes
	// the message away from the modules after it.
	r.Watch(-10, "captcha", watchCaptchaJoin)
	r.WatchJoinRequests("captcha", func(c *core.Ctx, req *gotgbot.ChatJoinRequest) error {
		if isGroup(req.Chat.Type) {
			c.HoldRequest(req.Chat, &req.From, req.UserChatId)
		}
		return nil
	})
}

// watchCaptchaJoin mutes and tests newcomers, and cleans up the captcha of someone who left.
func watchCaptchaJoin(c *core.Ctx) (bool, error) {
	m := c.Msg
	if c.Cb != nil || c.Edited || m == nil || !isGroup(m.Chat.Type) {
		return false, nil
	}
	switch {
	case len(m.NewChatMembers) > 0:
		var people []gotgbot.User
		for _, u := range m.NewChatMembers {
			if !u.IsBot && u.Id != c.Bot.Id {
				people = append(people, u)
			}
		}
		for i := range people {
			if i >= maxJoined {
				break
			}
			c.HoldCaptcha(m.Chat, &people[i], m.MessageId, i)
		}
	case m.LeftChatMember != nil:
		c.CaptchaDropUser(m.Chat.Id, m.LeftChatMember.Id)
	}
	return false, nil
}

// --- Buttons ---

// captchaIDs reads cap:<id>:<n> (three parts) or capr:<id> (two); ok is false for anything else.
func captchaIDs(data string, parts int) (id int64, n int, ok bool) {
	p := strings.Split(data, ":")
	if len(p) != parts || !isDigits(p[1]) || (parts == 3 && !isDigits(p[2])) {
		return 0, 0, false
	}
	id, _ = strconv.ParseInt(p[1], 10, 64)
	if parts == 3 {
		n, _ = strconv.Atoi(p[2])
	}
	return id, n, true
}

func cbCaptchaTest(c *core.Ctx) error {
	id, index, ok := captchaIDs(c.Cb.Data, 3)
	if !ok {
		c.Answer(c.T("common.this_button_is_not_valid"), true)
		return nil
	}
	row, err := c.App.CaptchaRowByID(c.Ctx, id)
	if err != nil {
		return err
	}
	if row == nil {
		c.Answer(c.T("common.you_re_already_verified_or_this"), true)
		return nil
	}
	if c.User.Id != row.UserID {
		c.Answer(c.T("captcha.this_button_isn_t_for_you"), true)
		return nil
	}
	c.Answer(c.PressCaptcha(row, index, c.User), false)
	return nil
}

func cbCaptchaWelcome(c *core.Ctx) error {
	value := strings.TrimPrefix(c.Cb.Data, core.CaptchaWelcomePrefix+":")
	digits := strings.TrimLeft(value, "-")
	if !isDigits(digits) {
		c.Answer(c.T("common.this_button_is_not_valid"), true)
		return nil
	}
	chatID, _ := strconv.ParseInt(value, 10, 64)
	if c.Msg != nil && c.Msg.Chat.Id != chatID {
		c.Answer(c.T("common.this_button_is_not_valid"), true)
		return nil
	}
	note, alert, url := c.WelcomePressed(chatID, c.User)
	opts := &gotgbot.AnswerCallbackQueryOpts{Url: url}
	if note != "" {
		opts.Text, opts.ShowAlert = note, alert
	}
	_, err := c.Bot.AnswerCallbackQueryWithContext(c.Ctx, c.Cb.Id, opts)
	return err
}

func cbCaptchaRules(c *core.Ctx) error {
	id, _, ok := captchaIDs(c.Cb.Data, 2)
	if !ok {
		c.Answer(c.T("common.this_button_is_not_valid"), true)
		return nil
	}
	row, err := c.App.CaptchaRowByID(c.Ctx, id)
	if err != nil {
		return err
	}
	if row == nil {
		c.Answer(c.T("captcha.this_captcha_has_expired"), true)
		return nil
	}
	if c.User.Id != row.UserID {
		c.Answer(c.T("captcha.this_isn_t_for_you"), true)
		return nil
	}
	c.Answer(c.AcceptRules(row, c.User), false)
	return nil
}

// --- Settings ---

// captchaBegin: the chat's own business: admins only, never the bot's staff.
func captchaBegin(c *core.Ctx, change bool) bool {
	if !isGroup(c.Msg.Chat.Type) {
		c.Say(c.T("common.this_command_only_works_in_groups"))
		return false
	}
	right := rightLookChat
	if change {
		right = rightChangeInfo
	}
	return c.BeginChatCommand(core.AuthorizeOpts{Right: right}) != nil
}

func secondsText(seconds int) string {
	return core.FormatDuration(time.Duration(seconds) * time.Second)
}

func captchaSummary(c *core.Ctx, s core.CaptchaSettings) string {
	mute := c.T("captcha.stay_muted_until_solved")
	if s.MuteSeconds != nil && *s.MuteSeconds != 0 {
		mute = c.T("captcha.unmuted_after", core.V{"time": secondsText(*s.MuteSeconds)})
	}
	button := c.T("captcha.default", core.V{"tr": text.Esc(c.T("captchas.default_button"))})
	if s.ButtonText != nil && *s.ButtonText != "" {
		button = "<code>" + text.Esc(*s.ButtonText) + "</code>"
	}
	return c.T("captcha.captcha_enabled_mode_rules_unsolved_kick", core.V{
		"on_off": onOff(c, s.Enabled), "mode": s.Mode, "on_off2": onOff(c, s.Rules), "mute": mute,
		"on_off3": onOff(c, s.Kick), "time": secondsText(s.KickSeconds), "button": button,
	})
}

// captchaRightsWarning: sentences about what keeps captchas from working here, else "".
func captchaRightsWarning(c *core.Ctx) string {
	note := ""
	if !c.WelcomeAvailable(c.Msg.Chat.Id) {
		note = c.T("captcha.welcome_messages_are_currently_off_captchas")
	}
	member := c.GetMember(c.Msg.Chat.Id, c.Bot.Id)
	if member == nil {
		return note
	}
	if member.Status == "creator" || (member.Status == "administrator" && member.CanRestrictMembers) {
		return note
	}
	return note + c.T("captcha.i_can_t_mute_anybody_yet")
}

func cmdCaptcha(c *core.Ctx) error {
	words := strings.Fields(c.Args)
	arg := ""
	if len(words) > 0 {
		arg = words[0]
	}
	chatID := c.Msg.Chat.Id
	if arg == "" {
		if captchaBegin(c, false) {
			s, err := c.App.CaptchaSettings(c.Ctx, chatID)
			if err != nil {
				return err
			}
			warning := ""
			if !c.WelcomeAvailable(chatID) {
				warning = c.T("captcha.welcome_messages_are_off_captchas_only")
			}
			c.Say(captchaSummary(c, s) + warning + c.T("captcha.use_captcha_on_or_captcha_off"))
		}
		return nil
	}
	enabled, ok := text.ParseSwitch(arg)
	if !ok {
		c.Say(c.T("captcha.use_captcha_on_or_captcha_off_2"))
		return nil
	}
	if !captchaBegin(c, true) {
		return nil
	}
	s, err := c.App.CaptchaSettings(c.Ctx, chatID)
	if err != nil {
		return err
	}
	if s.Enabled == enabled {
		c.Say(c.T("captcha.captchas_are_already", core.V{"on_off": onOff(c, enabled)}))
		return nil
	}
	if err := c.App.UpdateCaptcha(c.Ctx, chatID, map[string]any{"enabled": enabled}); err != nil {
		return err
	}
	if enabled {
		c.Say(c.T("captcha.captchas_are_on_new_members_are") + captchaRightsWarning(c))
		return nil
	}
	freed, err := c.ReleaseChat(chatID)
	if err != nil {
		return err
	}
	msg := c.T("captcha.captchas_are_off")
	if freed > 0 {
		msg += c.T("captcha.waiting_member_let_in", core.V{"freed": freed, "opt": plural(freed)})
	}
	c.Say(msg)
	return nil
}

func cmdCaptchamode(c *core.Ctx) error {
	arg := strings.ToLower(strings.TrimSpace(c.Args))
	usage := c.T("captcha.use_captchamode_button_math_text_or")
	chatID := c.Msg.Chat.Id
	if arg == "" {
		if captchaBegin(c, false) {
			s, err := c.App.CaptchaSettings(c.Ctx, chatID)
			if err != nil {
				return err
			}
			c.Say(c.T("captcha.the_captcha_mode_is", core.V{"mode": s.Mode, "usage": usage}))
		}
		return nil
	}
	if !contains(core.CaptchaModes, arg) {
		c.Say(usage)
		return nil
	}
	if !captchaBegin(c, true) {
		return nil
	}
	if arg == "text2" && !core.CaptchaPictureWorks() {
		c.Say(c.T("captcha.i_can_t_draw_pictures_on"))
		return nil
	}
	if err := c.App.UpdateCaptcha(c.Ctx, chatID, map[string]any{"mode": arg}); err != nil {
		return err
	}
	c.Say(c.T("captcha.the_captcha_mode_is_now", core.V{"argument": arg}))
	return nil
}

func cmdCaptcharules(c *core.Ctx) error {
	arg := strings.TrimSpace(c.Args)
	chatID := c.Msg.Chat.Id
	if arg == "" {
		if captchaBegin(c, false) {
			s, err := c.App.CaptchaSettings(c.Ctx, chatID)
			if err != nil {
				return err
			}
			note := ""
			if s.Rules && c.RulesMissing(chatID) {
				note = c.T("captcha.this_chat_has_no_rules_yet")
			}
			c.Say(c.T("captcha.accepting_the_rules_is_use_captcharules", core.V{"on_off": onOff(c, s.Rules), "note": note}))
		}
		return nil
	}
	enabled, ok := text.ParseSwitch(arg)
	if !ok {
		c.Say(c.T("captcha.use_captcharules_on_or_captcharules_off"))
		return nil
	}
	if !captchaBegin(c, true) {
		return nil
	}
	if err := c.App.UpdateCaptcha(c.Ctx, chatID, map[string]any{"rules": enabled}); err != nil {
		return err
	}
	msg := c.T("captcha.newcomers_no_longer_have_to_accept")
	if enabled {
		msg = c.T("captcha.newcomers_must_accept_the_rules_after")
		if c.RulesMissing(chatID) {
			msg += c.T("captcha.this_chat_has_no_rules_yet_2")
		}
	}
	c.Say(msg)
	return nil
}

// captchaDuration is a time within what Telegram allows, in seconds.
func captchaDuration(s string) (int, bool) {
	d, ok := core.ParseDuration(s)
	if !ok || d < core.MinDuration || d > core.MaxDuration {
		return 0, false
	}
	return int(d.Seconds()), true
}

func cmdCaptchamutetime(c *core.Ctx) error {
	arg := strings.ToLower(strings.TrimSpace(c.Args))
	usage := c.T("captcha.use_captchamutetime_30m_or_2h_1d")
	chatID := c.Msg.Chat.Id
	if arg == "" {
		if captchaBegin(c, false) {
			s, err := c.App.CaptchaSettings(c.Ctx, chatID)
			if err != nil {
				return err
			}
			now := c.T("captcha.unsolved_members_stay_muted_until_they")
			if s.MuteSeconds != nil && *s.MuteSeconds != 0 {
				now = c.T("captcha.unsolved_members_are_unmuted_after", core.V{"time": secondsText(*s.MuteSeconds)})
			}
			c.Say(now + "\n" + usage)
		}
		return nil
	}
	var seconds *int
	if arg != "off" && arg != "no" {
		n, ok := captchaDuration(strings.Fields(arg)[0])
		if !ok {
			c.Say(usage)
			return nil
		}
		seconds = &n
	}
	if !captchaBegin(c, true) {
		return nil
	}
	var value any
	if seconds != nil {
		value = *seconds
	}
	if err := c.App.UpdateCaptcha(c.Ctx, chatID, map[string]any{"mute_seconds": value}); err != nil {
		return err
	}
	if seconds == nil {
		c.Say(c.T("captcha.unsolved_members_stay_muted_until_they_2"))
	} else {
		c.Say(c.T("captcha.members_who_haven_t_solved_the", core.V{"time": secondsText(*seconds)}))
	}
	return nil
}

func cmdCaptchakick(c *core.Ctx) error {
	arg := strings.TrimSpace(c.Args)
	chatID := c.Msg.Chat.Id
	if arg == "" {
		if captchaBegin(c, false) {
			s, err := c.App.CaptchaSettings(c.Ctx, chatID)
			if err != nil {
				return err
			}
			c.Say(c.T("captcha.kicking_is_after_use_captchakick_on", core.V{"on_off": onOff(c, s.Kick), "time": secondsText(s.KickSeconds)}))
		}
		return nil
	}
	enabled, ok := text.ParseSwitch(arg)
	if !ok {
		c.Say(c.T("captcha.use_captchakick_on_or_captchakick_off"))
		return nil
	}
	if !captchaBegin(c, true) {
		return nil
	}
	s, err := c.App.CaptchaSettings(c.Ctx, chatID)
	if err != nil {
		return err
	}
	if err := c.App.UpdateCaptcha(c.Ctx, chatID, map[string]any{"kick": enabled}); err != nil {
		return err
	}
	if enabled {
		c.Say(c.T("captcha.people_who_don_t_solve_the", core.V{"time": secondsText(s.KickSeconds)}))
	} else {
		c.Say(c.T("captcha.people_are_no_longer_removed_for"))
	}
	return nil
}

func cmdCaptchakicktime(c *core.Ctx) error {
	arg := strings.ToLower(strings.TrimSpace(c.Args))
	usage := c.T("captcha.use_captchakicktime_5m_or_30m_2h")
	chatID := c.Msg.Chat.Id
	if arg == "" {
		if captchaBegin(c, false) {
			s, err := c.App.CaptchaSettings(c.Ctx, chatID)
			if err != nil {
				return err
			}
			c.Say(c.T("captcha.the_kick_time_is", core.V{"time": secondsText(s.KickSeconds), "usage": usage}))
		}
		return nil
	}
	seconds, ok := captchaDuration(strings.Fields(arg)[0])
	if !ok {
		c.Say(usage)
		return nil
	}
	if !captchaBegin(c, true) {
		return nil
	}
	s, err := c.App.CaptchaSettings(c.Ctx, chatID)
	if err != nil {
		return err
	}
	if err := c.App.UpdateCaptcha(c.Ctx, chatID, map[string]any{"kick_seconds": seconds}); err != nil {
		return err
	}
	hint := ""
	if !s.Kick {
		hint = c.T("captcha.kicking_is_off_turn_it_on")
	}
	c.Say(c.T("captcha.people_get_to_solve_the_captcha", core.V{"time": secondsText(seconds), "hint": hint}))
	return nil
}

func cmdSetcaptchatext(c *core.Ctx) error {
	t := strings.Join(strings.Fields(c.Args), " ")
	if t == "" {
		c.Say(c.T("captcha.use_setcaptchatext_lt_text_gt_for"))
		return nil
	}
	if len([]rune(t)) > maxButtonText {
		c.Say(c.T("captcha.that_s_too_long_for_a", core.V{"MAX_BUTTON_TEXT": maxButtonText}))
		return nil
	}
	if !captchaBegin(c, true) {
		return nil
	}
	if err := c.App.UpdateCaptcha(c.Ctx, c.Msg.Chat.Id, map[string]any{"button_text": t}); err != nil {
		return err
	}
	c.Say(c.T("captcha.the_captcha_button_now_says", core.V{"text": text.Esc(t)}))
	return nil
}

func cmdResetcaptchatext(c *core.Ctx) error {
	if !captchaBegin(c, true) {
		return nil
	}
	if err := c.App.UpdateCaptcha(c.Ctx, c.Msg.Chat.Id, map[string]any{"button_text": nil}); err != nil {
		return err
	}
	c.Say(c.T("captcha.the_captcha_button_is_back_to", core.V{"DEFAULT_BUTTON": text.Esc(c.T("captchas.default_button"))}))
	return nil
}
