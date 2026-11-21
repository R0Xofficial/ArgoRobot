package modules

import (
	"log/slog"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/core"
	"argo/internal/text"
)

// Welcome and goodbye messages (modules/greetings.py).

const (
	maxGreeted = 5  // people greeted for one "joined" message (someone can add many at once)
	muteHours  = 24 // the welcome mute
)

func registerGreetings(r *core.Router) {
	r.Command(func(c *core.Ctx) error { return greetCommand(c, "welcome") }, "welcome")
	r.Command(func(c *core.Ctx) error { return greetCommand(c, "goodbye") }, "goodbye")
	r.Command(func(c *core.Ctx) error { return greetSet(c, "welcome") }, "setwelcome")
	r.Command(func(c *core.Ctx) error { return greetSet(c, "goodbye") }, "setgoodbye")
	r.Command(func(c *core.Ctx) error { return greetReset(c, "welcome") }, "resetwelcome")
	r.Command(func(c *core.Ctx) error { return greetReset(c, "goodbye") }, "resetgoodbye")
	r.Command(cmdWelcomemute, "welcomemute")
	r.Command(cmdCleanwelcome, "cleanwelcome")
	r.Command(cmdCleanservice, "cleanservice")

	// After the captcha (-10), which holds newcomers first. The watchers do not take the message away from the
	// modules after them.
	r.Watch(-5, "greetings", watchGreetings)
	r.WatchMembers(0, "greetings", watchGreetMember)
	cleanServiceHook = func(c *core.Ctx, chatID int64) bool {
		s, err := c.App.Greetings(c.Ctx, chatID)
		return err == nil && s.CleanService
	}
	core.CaptchaWelcomeLimit = maxGreeted // the captcha button rides on these welcomes
	core.CaptchaReleased = append(core.CaptchaReleased, greetAfterCaptcha)
}

func greetBegin(c *core.Ctx, change bool) bool { return captchaBegin(c, change) }

// --- Welcome and goodbye: the same commands for both ---

func capitalize(s string) string { return strings.ToUpper(s[:1]) + s[1:] }

func greetShow(c *core.Ctx, kind, argument string) error {
	if !greetBegin(c, false) {
		return nil
	}
	s, err := c.App.Greetings(c.Ctx, c.Msg.Chat.Id)
	if err != nil {
		return err
	}
	content, mediaType, fileID := s.MessageOf(c.L, kind)
	opt := c.T("greetings.default")
	if s.Custom(kind) {
		opt = c.T("greetings.custom")
	}
	head := []string{
		"<b>" + capitalize(kind) + "</b>",
		c.T("greetings.enabled", core.V{"yes_no": yesNo(c, s.On(kind))}),
		c.T("greetings.message", core.V{"opt": opt}),
	}
	if kind == "welcome" {
		head = append(head,
			c.T("greetings.welcome_mute", core.V{"yes_no": yesNo(c, s.WelcomeMute)}),
			c.T("greetings.clean_welcome", core.V{"yes_no": yesNo(c, s.CleanWelcome)}),
			c.T("greetings.clean_service", core.V{"yes_no": yesNo(c, s.CleanService)}))
	}
	c.Say(strings.Join(head, "\n"))
	c.SendSaved(c.User, content, mediaType, fileID, core.SendOpts{Raw: argument == "noformat", Label: kind})
	return nil
}

func greetCommand(c *core.Ctx, kind string) error {
	if !groupOnly(c) {
		return nil
	}
	arg := strings.ToLower(strings.TrimSpace(c.Args))
	if arg == "" || arg == "noformat" {
		return greetShow(c, kind, arg)
	}
	enabled, ok := text.ParseSwitch(arg)
	if !ok {
		c.Say(c.T("greetings.use_noformat_or_on_off", core.V{"kind": kind}))
		return nil
	}
	if !greetBegin(c, true) {
		return nil
	}
	plural := c.T("greetings.goodbyes")
	if kind == "welcome" {
		plural = c.T("greetings.welcomes")
	}
	s, err := c.App.Greetings(c.Ctx, c.Msg.Chat.Id)
	if err != nil {
		return err
	}
	if s.On(kind) == enabled {
		c.Say(c.T("greetings.are_already", core.V{"plural": plural, "opt": onOff(c, enabled)}))
		return nil
	}
	if err := c.App.UpdateGreetings(c.Ctx, c.Msg.Chat.Id, map[string]any{kind + "_on": enabled}); err != nil {
		return err
	}
	c.Say(c.T("greetings.are_now", core.V{"plural": plural, "opt": onOff(c, enabled)}))
	return nil
}

func greetSet(c *core.Ctx, kind string) error {
	if !groupOnly(c) || !greetBegin(c, true) {
		return nil
	}
	// Typed text and a replied-to message are both turned into the HTML of the markup engine.
	body := core.ArgsOf(c.Msg, c.Args)
	replied := core.RepliedMessage(c.Msg)
	media := core.MediaOf(c.Msg)
	if media == nil {
		media = core.MediaOf(replied) // media sent with the command, else the replied-to message's
	}
	if body == "" && replied != nil {
		body = core.TextOf(replied)
	}
	if body == "" && media == nil {
		c.Say(c.T("greetings.tell_me_what_to_say_write", core.V{"kind": kind}))
		return nil
	}
	if err := c.Validate(body, media != nil); err != nil {
		return sayMarkupError(c, err)
	}
	var mt, mid any
	if media != nil {
		mt, mid = media.Kind, media.FileID
	}
	if err := c.App.UpdateGreetings(c.Ctx, c.Msg.Chat.Id, map[string]any{
		kind + "_content": body, kind + "_media_type": mt, kind + "_media_file_id": mid,
	}); err != nil {
		return err
	}
	c.Say(c.T("greetings.the_message_is_saved_use_to", core.V{"kind": kind}))
	return nil
}

func greetReset(c *core.Ctx, kind string) error {
	if !groupOnly(c) || !greetBegin(c, true) {
		return nil
	}
	if err := c.App.UpdateGreetings(c.Ctx, c.Msg.Chat.Id, map[string]any{
		kind + "_content": nil, kind + "_media_type": nil, kind + "_media_file_id": nil,
	}); err != nil {
		return err
	}
	c.Say(c.T("greetings.the_message_is_back_to_the", core.V{"kind": kind}))
	return nil
}

// --- Switches ---

func greetSwitch(c *core.Ctx, column, usage string, current func(core.GreetSettings) bool, say func(bool) string) error {
	if !groupOnly(c) {
		return nil
	}
	arg := strings.ToLower(strings.TrimSpace(c.Args))
	chatID := c.Msg.Chat.Id
	if arg == "" {
		if !greetBegin(c, false) {
			return nil
		}
		s, err := c.App.Greetings(c.Ctx, chatID)
		if err != nil {
			return err
		}
		c.Say(say(current(s)) + "\n" + usage)
		return nil
	}
	enabled, ok := text.ParseSwitch(arg)
	if !ok {
		c.Say(usage)
		return nil
	}
	if !greetBegin(c, true) {
		return nil
	}
	s, err := c.App.Greetings(c.Ctx, chatID)
	if err != nil {
		return err
	}
	if current(s) == enabled {
		c.Say(c.T("greetings.this_is_already", core.V{"opt": onOff(c, enabled)}))
		return nil
	}
	if err := c.App.UpdateGreetings(c.Ctx, chatID, map[string]any{column: enabled}); err != nil {
		return err
	}
	c.Say(say(enabled))
	return nil
}

func cmdWelcomemute(c *core.Ctx) error {
	return greetSwitch(c, "welcome_mute", c.T("greetings.use_welcomemute_on_or_welcomemute_off"),
		func(s core.GreetSettings) bool { return s.WelcomeMute },
		func(on bool) string {
			if on {
				return c.T("greetings.new_members_can_t_send_media", core.V{"MUTE_HOURS": muteHours})
			}
			return c.T("greetings.new_members_can_send_anything_right")
		})
}

func cmdCleanwelcome(c *core.Ctx) error {
	return greetSwitch(c, "clean_welcome", c.T("greetings.use_cleanwelcome_on_or_cleanwelcome_off"),
		func(s core.GreetSettings) bool { return s.CleanWelcome },
		func(on bool) string {
			if on {
				return c.T("greetings.my_previous_welcome_message_will_be")
			}
			return c.T("greetings.old_welcome_messages_will_be_kept")
		})
}

func cmdCleanservice(c *core.Ctx) error {
	return greetSwitch(c, "clean_service", c.T("greetings.use_cleanservice_on_or_cleanservice_off"),
		func(s core.GreetSettings) bool { return s.CleanService },
		func(on bool) string {
			if on {
				return c.T("greetings.the_joined_and_left_service_messages")
			}
			return c.T("greetings.service_messages_will_be_kept")
		})
}

// --- Joins and leaves ---

// restrictNewcomer is the welcome mute: no media for a day. Whoever is restricted already is left alone.
func restrictNewcomer(c *core.Ctx, user *gotgbot.User) {
	chatID := c.Msg.Chat.Id
	if approved, _ := c.App.IsApproved(c.Ctx, chatID, user.Id); approved || core.IsImmune(user.Id) {
		return
	}
	member := c.GetMember(chatID, user.Id)
	if member == nil || member.Status != "member" {
		// Muted, an admin or gone: restricting again could undo what someone decided.
		return
	}
	until := time.Now().Add(muteHours * time.Hour).Unix()
	_, err := c.Bot.RestrictChatMemberWithContext(c.Ctx, chatID, user.Id, gotgbot.ChatPermissions{CanSendMessages: true},
		&gotgbot.RestrictChatMemberOpts{UseIndependentChatPermissions: true, UntilDate: until})
	if err != nil {
		slog.Info("Welcome mute failed", "user", user.Id, "chat", chatID, "err", err)
	}
}

func deleteMessage(c *core.Ctx, chatID, messageID int64) {
	_, _ = c.Bot.DeleteMessageWithContext(c.Ctx, chatID, messageID, nil)
}

// greet: the welcome mute and the welcome message for people who are in.
func greet(c *core.Ctx, people []gotgbot.User, s core.GreetSettings) {
	var members []*gotgbot.User
	for i := range people {
		user := &people[i]
		if role := c.App.GetRole(c.Ctx, user.Id); role != core.NoRole {
			// Somebody with a role is welcomed with the message of their role, whatever the chat's settings are.
			c.SendSaved(user, core.StaffGreeting(role, "welcome"), "", "", core.SendOpts{Label: c.T("greetings.staff_welcome_message")})
		} else {
			members = append(members, user) // the chat's own welcome, mute and captcha are for everyone else
		}
	}
	chatID := c.Msg.Chat.Id
	if s.WelcomeMute {
		for _, user := range members {
			restrictNewcomer(c, user)
		}
	}
	if !s.WelcomeOn || len(members) == 0 {
		return
	}
	content, mediaType, fileID := s.MessageOf(c.L, "welcome")
	if s.CleanWelcome && s.LastWelcomeID != nil && *s.LastWelcomeID != 0 {
		deleteMessage(c, chatID, *s.LastWelcomeID)
	}
	var last int64
	for _, user := range members {
		// Somebody with a captcha message of their own is welcomed once through; someone whose welcome carries
		// the captcha button gets it.
		skip, button := c.CaptchaWelcomeFor(chatID, user.Id)
		if skip {
			continue
		}
		sent := c.SendSaved(user, content, mediaType, fileID, core.SendOpts{Label: c.T("greetings.welcome_message"), ExtraButton: button})
		if sent != nil {
			last = sent.MessageId
			if button != nil {
				c.MarkWelcomed(chatID, user.Id)
			}
		}
	}
	if last != 0 && s.CleanWelcome {
		if err := c.App.UpdateGreetings(c.Ctx, chatID, map[string]any{"last_welcome_id": last}); err != nil {
			slog.Error("Could not remember the last welcome", "chat", chatID, "err", err)
		}
	}
}

// logMember writes who joined (#JOINED) or left on their own (#LEFT) in the chat's log (/setlog).
func logMember(c *core.Ctx, chat gotgbot.Chat, tag string, user *gotgbot.User) {
	c.RecordLog(chat, tag, "", text.UserRef(user.Id, core.FullName(user)), "", "", nil, "")
}

func hasLog(c *core.Ctx, chatID int64) bool {
	target, err := c.App.LogChat(c.Ctx, chatID)
	return err == nil && target != 0
}

func greetingsHere(c *core.Ctx, chatID int64) bool {
	return c.App.ModuleActive(c.Ctx, chatID, "greetings")
}

// madeUpMessage is a message to answer to when there is none: the welcome goes to the chat's main topic.
func madeUpMessage(c *core.Ctx, chat gotgbot.Chat, id int64) *core.Ctx {
	c2 := *c
	c2.Msg = &gotgbot.Message{MessageId: id, Chat: chat}
	c2.L = c.App.UseChat(c.Ctx, chat.Id)
	return &c2
}

func watchGreetings(c *core.Ctx) (bool, error) {
	m := c.Msg
	if c.Cb != nil || c.Edited || m == nil || !isGroup(m.Chat.Type) {
		return false, nil
	}
	switch {
	case len(m.NewChatMembers) > 0:
		return false, onJoin(c)
	case m.LeftChatMember != nil:
		return false, onLeave(c)
	}
	return false, nil
}

func onJoin(c *core.Ctx) error {
	m := c.Msg
	s, err := c.App.Greetings(c.Ctx, m.Chat.Id)
	if err != nil {
		return err
	}
	for i := range m.NewChatMembers {
		user := &m.NewChatMembers[i]
		c.App.MarkMember("join", m.Chat.Id, user.Id)
		if user.Id != c.Bot.Id {
			logMember(c, m.Chat, "JOINED", user)
		}
	}
	var people []gotgbot.User
	for _, user := range m.NewChatMembers {
		// Bots are not greeted, and a globally banned user (in a chat that removes them) or one a raid took away is
		// about to be removed.
		if user.IsBot || user.Id == c.Bot.Id || c.BanApplies(m.Chat.Id, user.Id) || c.App.RaidWasRemoved(m.Chat.Id, user.Id) {
			continue
		}
		people = append(people, user)
	}
	if len(people) > maxGreeted {
		people = people[:maxGreeted]
	}
	defer func() {
		// Whatever went wrong while greeting, the "joined" message still goes when the chat asked for it.
		if s.CleanService {
			deleteMessage(c, m.Chat.Id, m.MessageId)
		}
	}()
	greet(c, people, s)
	return nil
}

// greetAfterCaptcha: somebody got through a captcha; this is the welcome they would have had on joining.
func greetAfterCaptcha(c *core.Ctx, chatID int64, user *gotgbot.User, joinMessageID int64) {
	if !greetingsHere(c, chatID) {
		return
	}
	chat, err := c.Bot.GetChatWithContext(c.Ctx, chatID, nil)
	if err != nil {
		return
	}
	s, err := c.App.Greetings(c.Ctx, chatID)
	if err != nil {
		return
	}
	greet(madeUpMessage(c, chat.ToChat(), joinMessageID), []gotgbot.User{*user}, s)
}

func onLeave(c *core.Ctx) error {
	m := c.Msg
	s, err := c.App.Greetings(c.Ctx, m.Chat.Id)
	if err != nil {
		return err
	}
	left := m.LeftChatMember
	c.App.MarkMember("leave", m.Chat.Id, left.Id)
	// The sender of the service message is the person who left, or whoever removed them.
	leftOnOwn := m.From != nil && m.From.Id == left.Id
	if leftOnOwn && left.Id != c.Bot.Id { // a removal is in the log already, with the admin who did it
		logMember(c, m.Chat, "LEFT", left)
	}
	defer func() {
		if s.CleanService {
			deleteMessage(c, m.Chat.Id, m.MessageId)
		}
	}()
	sayGoodbye(c, left, s, leftOnOwn)
	return nil
}

// sayGoodbye: the goodbye of someone who left on their own: their role's, or the chat's when it has them on.
func sayGoodbye(c *core.Ctx, left *gotgbot.User, s core.GreetSettings, leftOnOwn bool) {
	if !leftOnOwn || left.IsBot || left.Id == c.Bot.Id {
		return
	}
	if role := c.App.GetRole(c.Ctx, left.Id); role != core.NoRole { // staff are always seen off, with the goodbye of their role
		c.SendSaved(left, core.StaffGreeting(role, "goodbye"), "", "", core.SendOpts{Label: c.T("greetings.staff_goodbye_message")})
	} else if s.GoodbyeOn {
		content, mediaType, fileID := s.MessageOf(c.L, "goodbye")
		c.SendSaved(left, content, mediaType, fileID, core.SendOpts{Label: c.T("greetings.goodbye_message")})
	}
}

// --- Joins and leaves that come without a service message ---
// In a group with hidden members (and in big ones) Telegram sends no "joined" or "left" message, only the
// chat_member update to admins. The update does the greeting when no service message showed up for it.

func watchGreetMember(c *core.Ctx, e *gotgbot.ChatMemberUpdated) (bool, error) {
	if !isGroup(e.Chat.Type) {
		return false, nil
	}
	old, now := e.OldChatMember.MergeChatMember(), e.NewChatMember.MergeChatMember()
	switch {
	case !memberPresent(old) && memberPresent(now) && now.Status != "creator":
		return false, memberJoined(c, e, &now.User)
	case memberPresent(old) && now.Status == "left": // not "kicked": a ban is no goodbye
		return false, memberLeft(c, e, &old.User)
	}
	return false, nil
}

func memberJoined(c *core.Ctx, e *gotgbot.ChatMemberUpdated, user *gotgbot.User) error {
	if user.Id == c.Bot.Id {
		return nil
	}
	logging := hasLog(c, e.Chat.Id)
	greeting := !user.IsBot && greetingsHere(c, e.Chat.Id)
	if !logging && !greeting {
		return nil
	}
	if !c.App.NoServiceMessage("join", e.Chat.Id, user.Id) {
		return nil // the service message came and has done it
	}
	if logging {
		logMember(c, e.Chat, "JOINED", user)
	}
	if !greeting || c.BanApplies(e.Chat.Id, user.Id) || c.App.RaidWasRemoved(e.Chat.Id, user.Id) {
		return nil
	}
	c.HoldCaptcha(e.Chat, user, 0, 0)
	s, err := c.App.Greetings(c.Ctx, e.Chat.Id)
	if err != nil {
		return err
	}
	greet(madeUpMessage(c, e.Chat, 0), []gotgbot.User{*user}, s)
	return nil
}

func memberLeft(c *core.Ctx, e *gotgbot.ChatMemberUpdated, user *gotgbot.User) error {
	if user.Id == c.Bot.Id {
		return nil
	}
	logging := hasLog(c, e.Chat.Id)
	greeting := !user.IsBot && greetingsHere(c, e.Chat.Id)
	if !logging && !greeting {
		return nil
	}
	if !c.App.NoServiceMessage("leave", e.Chat.Id, user.Id) {
		return nil
	}
	leftOnOwn := e.From.Id == user.Id
	if logging && leftOnOwn {
		logMember(c, e.Chat, "LEFT", user)
	}
	if !greeting {
		return nil
	}
	c.CaptchaDropUser(e.Chat.Id, user.Id)
	s, err := c.App.Greetings(c.Ctx, e.Chat.Id)
	if err != nil {
		return err
	}
	sayGoodbye(madeUpMessage(c, e.Chat, 0), user, s, leftOnOwn)
	return nil
}
