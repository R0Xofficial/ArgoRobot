package core

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/markup"
	"argo/internal/text"
)

// Captchas for new members (core/captchas.py; the commands are in modules/captcha.go).
//
// A chat that turned captchas on (/captcha on) mutes everybody who joins until they pass a test. The test is one of
// four kinds (/captchamode):
//
//	button  press one button
//	math    pick the answer to a small sum from six buttons
//	text    the code is written in the message; its characters sit scrambled on buttons and must be pressed in the
//	        order of the code
//	text2   the same, but the code is a picture
//
// When the chat uses join requests, the test is sent to the person privately and the request is approved once they
// pass it. Optionally (/captcharules) they must then accept the rules.
//
// Who still has to solve a captcha is kept in the database (table captcha_pending), so a restart loses nobody. A
// background sweep kicks people whose time ran out (/captchakick, /captchakicktime), unmutes the ones whose mute time
// ended (/captchamutetime) and lets go of everybody when the chat or the whole bot turns captchas off.
//
// Welcome messages wait for the captcha: the greetings module asks CaptchaWelcomeFor and registers a hook in
// CaptchaReleased to greet people once they are through.

const (
	captchaRequestLifetime = 24 * time.Hour // a join request that is not kicked is forgotten after this
	captchaRecent          = 300 * time.Second
	CaptchaSweepEvery      = 20 * time.Second
	captchaMaxRulesHTML    = 2500
	captchaCacheLimit      = 5000

	CaptchaPrefix        = "cap"  // cap:<id>:<button>, a button of a captcha message
	CaptchaRulesPrefix   = "capr" // capr:<id>
	CaptchaWelcomePrefix = "capw" // capw:<chat id>, the button under a welcome message: the same for everybody
)

// CaptchaWelcomeLimit is how many people are greeted for one "joined" message (the greetings module sets the real
// value): the captcha button rides on these welcomes.
var CaptchaWelcomeLimit = 5

// CaptchaReleased hooks are called for everybody who is let into a chat: the chat, the person and the id of their
// "joined" message (0 when there is none).
var CaptchaReleased []func(c *Ctx, chatID int64, user *gotgbot.User, joinMessageID int64)

// CaptchaSettings are the captcha settings of a chat.
type CaptchaSettings struct {
	Enabled     bool
	Mode        string
	Rules       bool
	MuteSeconds *int // nil: muted until the captcha is solved
	Kick        bool
	KickSeconds int
	ButtonText  *string
}

// CaptchaRow is somebody who still has to solve a captcha (a row of captcha_pending).
type CaptchaRow struct {
	ID            int64
	ChatID        int64
	UserID        int64
	Kind          string // join or request
	Mode          string
	Phase         string // captcha or rules
	Question      string
	Answer        string
	Layout        string
	Taken         string
	Name          string
	Title         string
	MessageChatID int64
	MessageID     int64 // 0: none
	IsPhoto       bool
	JoinMessageID int64 // 0: none
	CreatedAt     time.Time
	UnmuteAt      *time.Time
	KickAt        *time.Time
	ExpiresAt     *time.Time
	Attached      bool // the welcome message carries the button
	Welcomed      bool
}

const captchaColumns = `id, chat_id, user_id, kind, mode, phase, COALESCE(question, ''), COALESCE(answer, ''), COALESCE(layout, ''),
taken, COALESCE(name, ''), COALESCE(title, ''), COALESCE(message_chat_id, 0), COALESCE(message_id, 0), is_photo,
COALESCE(join_message_id, 0), created_at, unmute_at, kick_at, expires_at, attached, welcomed`

func (r *CaptchaRow) dest() []any {
	return []any{&r.ID, &r.ChatID, &r.UserID, &r.Kind, &r.Mode, &r.Phase, &r.Question, &r.Answer, &r.Layout,
		&r.Taken, &r.Name, &r.Title, &r.MessageChatID, &r.MessageID, &r.IsPhoto,
		&r.JoinMessageID, &r.CreatedAt, &r.UnmuteAt, &r.KickAt, &r.ExpiresAt, &r.Attached, &r.Welcomed}
}

type captchaState struct {
	mu     sync.Mutex
	cache  map[int64]CaptchaSettings
	recent map[[2]int64]time.Time
	locks  map[int64]*sync.Mutex
}

func defaultCaptcha() CaptchaSettings { return CaptchaSettings{Mode: "button", KickSeconds: 300} }

// --- Settings ---

// CaptchaSettings reads the captcha settings of a chat.
func (a *App) CaptchaSettings(ctx context.Context, chatID int64) (CaptchaSettings, error) {
	a.captcha.mu.Lock()
	s, ok := a.captcha.cache[chatID]
	a.captcha.mu.Unlock()
	if ok {
		return s, nil
	}
	s = defaultCaptcha()
	if _, err := a.DB.Found(ctx, "SELECT enabled, mode, rules, mute_seconds, kick, kick_seconds, button_text FROM captcha_settings WHERE chat_id = $1",
		[]any{chatID}, &s.Enabled, &s.Mode, &s.Rules, &s.MuteSeconds, &s.Kick, &s.KickSeconds, &s.ButtonText); err != nil {
		return s, err
	}
	a.captcha.mu.Lock()
	if a.captcha.cache == nil || len(a.captcha.cache) >= captchaCacheLimit {
		a.captcha.cache = map[int64]CaptchaSettings{}
	}
	a.captcha.cache[chatID] = s
	a.captcha.mu.Unlock()
	return s, nil
}

var captchaColumnNames = map[string]bool{"enabled": true, "mode": true, "rules": true, "mute_seconds": true, "kick": true, "kick_seconds": true, "button_text": true}

// UpdateCaptcha changes some settings of a chat; the others keep their value. A nil value clears a column.
func (a *App) UpdateCaptcha(ctx context.Context, chatID int64, columns map[string]any) error {
	if len(columns) == 0 {
		return fmt.Errorf("no captcha settings to change")
	}
	names := make([]string, 0, len(columns))
	for name := range columns {
		if !captchaColumnNames[name] {
			return fmt.Errorf("unknown captcha setting %q", name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	args := []any{chatID}
	marks := make([]string, len(names))
	sets := make([]string, len(names))
	for i, name := range names {
		args = append(args, columns[name])
		marks[i] = fmt.Sprintf("$%d", i+2)
		sets[i] = name + " = EXCLUDED." + name
	}
	_, err := a.DB.Exec(ctx, fmt.Sprintf("INSERT INTO captcha_settings (chat_id, %s) VALUES ($1, %s) ON CONFLICT (chat_id) DO UPDATE SET %s",
		strings.Join(names, ", "), strings.Join(marks, ", "), strings.Join(sets, ", ")), args...)
	a.forgetCaptcha(chatID)
	return err
}

func (a *App) forgetCaptcha(chatID int64) {
	a.captcha.mu.Lock()
	delete(a.captcha.cache, chatID)
	a.captcha.mu.Unlock()
}

// CaptchaActive returns the chat's settings when captchas are in force there, else nil. It never fails.
func (a *App) CaptchaActive(ctx context.Context, chatID int64) *CaptchaSettings {
	if !a.ModuleActive(ctx, chatID, "captcha") {
		return nil
	}
	s, err := a.CaptchaSettings(ctx, chatID)
	if err != nil {
		slog.Error("Looking up the captcha settings failed", "err", err)
		return nil
	}
	if !s.Enabled {
		return nil
	}
	return &s
}

// CaptchaPictureWorks tells whether the picture captcha can be drawn. It always can here.
func CaptchaPictureWorks() bool { return len(CaptchaPicture("AB2", nil)) > 0 }

// --- Messages ---

func (r *CaptchaRow) taken() []int {
	var out []int
	for _, p := range strings.Split(r.Taken, ",") {
		if n, err := strconv.Atoi(p); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func prettySeconds(c *Ctx, seconds float64) string {
	n := int(seconds + 0.5)
	if n < 1 {
		n = 1
	}
	return text.DurationText(c.L, n)
}

// RulesContent is the rules of a chat as the admin wrote them ("" when there are none).
func (a *App) RulesContent(ctx context.Context, chatID int64) string {
	var content *string
	if _, err := a.DB.Found(ctx, "SELECT content FROM rules WHERE chat_id = $1", []any{chatID}, &content); err != nil || content == nil {
		return ""
	}
	return *content
}

// rulesHTML are the rules ready to be shown inside the captcha message, or "" when there are none worth showing.
func (c *Ctx) rulesHTML(content, chatTitle, name string) string {
	if content == "" {
		return ""
	}
	parsed, err := markup.Parse(c.L, markup.Pick(content), map[string]string{
		"first": name, "last": "", "fullname": name, "username": "", "mention": name,
		"id": "", "chatname": chatTitle, "count": "",
	})
	if err != nil || utf8.RuneCountInString(parsed.HTML) > captchaMaxRulesHTML {
		return ""
	}
	return parsed.HTML
}

// captchaBody is the text of the captcha message for the person's current state.
func (c *Ctx) captchaBody(r *CaptchaRow) string {
	title := text.Esc(r.Title)
	if r.Title == "" {
		title = text.Esc(c.T("common.this_chat"))
	}
	name := r.Name
	if name == "" {
		name = "there"
	}
	if r.Phase == "rules" {
		shown := c.rulesHTML(c.App.RulesContent(c.Ctx, r.ChatID), r.Title, name)
		if shown == "" {
			shown = c.T("captchas.read_the_rules_of_the_chat")
		}
		var lead string
		switch {
		case r.Kind == "request":
			lead = c.T("captchas.hi_you_sent_a_request_to", V{"name": text.Esc(name), "title": title})
		case r.MessageChatID == r.ChatID:
			lead = c.T("captchas.one_last_thing_read_the_rules", V{"user_id": text.UserLink(r.UserID, name), "title": title})
		case r.Mode == "button":
			lead = c.T("captchas.hi_to_speak_in_read_the", V{"name": text.Esc(name), "title": title})
		default:
			lead = c.T("captchas.last_step_read_the_rules_of", V{"title": title})
		}
		return lead + "\n\n" + shown
	}

	var lead string
	switch {
	case r.Kind == "request":
		lead = c.T("captchas.hi_you_sent_a_request_to_2", V{"name": text.Esc(name), "title": title})
	case r.MessageChatID != r.ChatID:
		lead = c.T("captchas.hi_to_speak_in_prove_you", V{"name": text.Esc(name), "title": title})
	default:
		lead = c.T("captchas.welcome_to_prove_you_re_human", V{"user_id": text.UserLink(r.UserID, name), "title": title})
	}
	var task string
	switch r.Mode {
	case "button":
		task = c.T("captchas.press_the_button_below")
	case "math":
		task = c.T("captchas.solve_this_and_press_the_answer", V{"question": text.Esc(r.Question)})
	default:
		layout := []rune(r.Layout)
		var typed []rune
		for _, i := range r.taken() {
			if i >= 0 && i < len(layout) {
				typed = append(typed, layout[i])
			}
		}
		progress := string(typed) + strings.Repeat("•", max(len([]rune(r.Answer))-len(typed), 0))
		if r.Mode == "text" {
			task = c.T("captchas.press_the_characters_in_this_order", V{"answer": text.Esc(r.Answer), "progress": progress})
		} else {
			task = c.T("captchas.press_the_characters_of_the_code", V{"progress": progress})
		}
	}
	note := ""
	if r.KickAt != nil {
		note = c.T("captchas.you_have_to_do_this_or", V{"pretty": prettySeconds(c, r.KickAt.Sub(r.CreatedAt).Seconds())})
	}
	return lead + "\n\n" + task + note
}

func (c *Ctx) captchaButton(buttonText *string) string {
	if buttonText != nil && *buttonText != "" {
		return *buttonText
	}
	return c.T("captchas.default_button")
}

// captchaKeyboard is the buttons of the captcha message.
func (c *Ctx) captchaKeyboard(r *CaptchaRow, buttonText *string) gotgbot.InlineKeyboardMarkup {
	cb := func(label string, i int) gotgbot.InlineKeyboardButton {
		return gotgbot.InlineKeyboardButton{Text: label, CallbackData: fmt.Sprintf("%s:%d:%d", CaptchaPrefix, r.ID, i)}
	}
	if r.Phase == "rules" {
		return gotgbot.InlineKeyboardMarkup{InlineKeyboard: [][]gotgbot.InlineKeyboardButton{{
			{Text: c.T("captchas.accept_button"), CallbackData: fmt.Sprintf("%s:%d", CaptchaRulesPrefix, r.ID)}}}}
	}
	if r.Mode == "button" {
		return gotgbot.InlineKeyboardMarkup{InlineKeyboard: [][]gotgbot.InlineKeyboardButton{{cb(c.captchaButton(buttonText), 0)}}}
	}
	var buttons []gotgbot.InlineKeyboardButton
	perRow := 5
	if r.Mode == "math" {
		for i, option := range strings.Split(r.Layout, ",") {
			buttons = append(buttons, cb(option, i))
		}
		perRow = 3
	} else {
		taken := map[int]bool{}
		for _, i := range r.taken() {
			taken[i] = true
		}
		for i, ch := range []rune(r.Layout) {
			if !taken[i] {
				buttons = append(buttons, cb(string(ch), i))
			}
		}
	}
	var rows [][]gotgbot.InlineKeyboardButton
	for i := 0; i < len(buttons); i += perRow {
		rows = append(rows, buttons[i:min(i+perRow, len(buttons))])
	}
	return gotgbot.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func (c *Ctx) editCaptchaMessage(r *CaptchaRow, msg string, kb *gotgbot.InlineKeyboardMarkup) error {
	var err error
	if r.IsPhoto {
		o := &gotgbot.EditMessageCaptionOpts{ChatId: r.MessageChatID, MessageId: r.MessageID, Caption: msg, ParseMode: parseHTML}
		if kb != nil {
			o.ReplyMarkup = *kb
		}
		_, _, err = c.Bot.EditMessageCaptionWithContext(c.Ctx, o)
	} else {
		o := &gotgbot.EditMessageTextOpts{ChatId: r.MessageChatID, MessageId: r.MessageID, Text: msg, ParseMode: parseHTML}
		if kb != nil {
			o.ReplyMarkup = *kb
		}
		_, _, err = c.Bot.EditMessageTextWithContext(c.Ctx, o)
	}
	return err
}

// showCaptcha brings the message up to date with the row.
func (c *Ctx) showCaptcha(r *CaptchaRow, buttonText *string) {
	kb := c.captchaKeyboard(r, buttonText)
	if err := c.editCaptchaMessage(r, c.captchaBody(r), &kb); err != nil && !strings.Contains(strings.ToLower(err.Error()), "not modified") {
		slog.Info("Could not update a captcha message", "message", r.MessageID, "err", err)
	}
}

func (c *Ctx) removeCaptchaMessage(r *CaptchaRow) {
	if r.MessageID != 0 {
		_, _ = c.Bot.DeleteMessageWithContext(c.Ctx, r.MessageChatID, r.MessageID, nil)
	}
}

// sayCaptcha replaces the captcha message's text and takes its buttons away.
func (c *Ctx) sayCaptcha(r *CaptchaRow, msg string) { _ = c.editCaptchaMessage(r, msg, nil) }

// --- Rows ---

func (a *App) captchaRow(ctx context.Context, where string, args ...any) (*CaptchaRow, error) {
	r := &CaptchaRow{}
	found, err := a.DB.Found(ctx, "SELECT "+captchaColumns+" FROM captcha_pending WHERE "+where, args, r.dest()...)
	if err != nil || !found {
		return nil, err
	}
	return r, nil
}

// CaptchaRowByID reads a pending captcha; nil when there is none.
func (a *App) CaptchaRowByID(ctx context.Context, id int64) (*CaptchaRow, error) {
	return a.captchaRow(ctx, "id = $1", id)
}

// CaptchaFind reads the pending captcha of a person in a chat; nil when there is none.
func (a *App) CaptchaFind(ctx context.Context, chatID, userID int64) (*CaptchaRow, error) {
	return a.captchaRow(ctx, "chat_id = $1 AND user_id = $2", chatID, userID)
}

// CaptchaHeld tells whether the person still has to solve a captcha in that chat.
func (a *App) CaptchaHeld(ctx context.Context, chatID, userID int64) bool {
	r, err := a.CaptchaFind(ctx, chatID, userID)
	return err == nil && r != nil
}

func (a *App) deleteCaptcha(ctx context.Context, id int64) error {
	_, err := a.DB.Exec(ctx, "DELETE FROM captcha_pending WHERE id = $1", id)
	return err
}

func (a *App) markPassed(chatID, userID int64) {
	a.captcha.mu.Lock()
	defer a.captcha.mu.Unlock()
	if a.captcha.recent == nil {
		a.captcha.recent = map[[2]int64]time.Time{}
	}
	if len(a.captcha.recent) > 2000 {
		for k, t := range a.captcha.recent {
			if time.Since(t) > captchaRecent {
				delete(a.captcha.recent, k)
			}
		}
	}
	a.captcha.recent[[2]int64{chatID, userID}] = time.Now()
}

func (a *App) justPassed(chatID, userID int64) bool {
	a.captcha.mu.Lock()
	defer a.captcha.mu.Unlock()
	t, ok := a.captcha.recent[[2]int64{chatID, userID}]
	return ok && time.Since(t) < captchaRecent
}

func (c *Ctx) captchaExempt(chatID int64, u *gotgbot.User) bool {
	if u.IsBot || u.Id == c.Bot.Id || IsImmune(u.Id) || c.App.justPassed(chatID, u.Id) {
		return true
	}
	return c.App.GetRole(c.Ctx, u.Id) != NoRole || c.isApproved(chatID, u.Id)
}

func (c *Ctx) insertCaptcha(chat gotgbot.Chat, user *gotgbot.User, kind string, cfg *CaptchaSettings, joinMessageID, messageChatID int64, attached bool) (*CaptchaRow, error) {
	mode := cfg.Mode
	if !inList(CaptchaModes, mode) {
		mode = "button"
	}
	if mode == "text2" && !CaptchaPictureWorks() {
		mode = "text"
	}
	test := MakeCaptcha(mode, nil)
	now := time.Now().UTC()
	var kickAt, unmuteAt, expiresAt *time.Time
	if cfg.Kick {
		t := now.Add(time.Duration(cfg.KickSeconds) * time.Second)
		kickAt = &t
	}
	if kind == "join" && cfg.MuteSeconds != nil && *cfg.MuteSeconds > 0 {
		t := now.Add(time.Duration(*cfg.MuteSeconds) * time.Second)
		unmuteAt = &t
	}
	if kind == "request" && kickAt == nil {
		t := now.Add(captchaRequestLifetime)
		expiresAt = &t
	}
	var joinID, msgChat *int64
	if joinMessageID != 0 {
		joinID = &joinMessageID
	}
	if messageChatID != 0 {
		msgChat = &messageChatID
	}
	r := &CaptchaRow{}
	err := c.App.DB.Row(c.Ctx, `INSERT INTO captcha_pending
(chat_id, user_id, kind, mode, question, answer, layout, name, title, message_chat_id, join_message_id, created_at, unmute_at, kick_at, expires_at, attached)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16) RETURNING `+captchaColumns,
		[]any{chat.Id, user.Id, kind, mode, test.Question, test.Answer, test.Layout, FullName(user), chat.Title, msgChat, joinID, now, unmuteAt, kickAt, expiresAt, attached},
		r.dest()...)
	return r, err
}

// rulesOnly: in the private chat, button mode with rules to accept needs no button of its own: the rules come at
// once, and accepting them is the test. (Under a welcome message the button is the test.)
func (c *Ctx) rulesOnly(r *CaptchaRow, cfg *CaptchaSettings) {
	if r.Mode == "button" && r.Phase == "captcha" && cfg.Rules && c.App.RulesContent(c.Ctx, r.ChatID) != "" {
		_, _ = c.App.DB.Exec(c.Ctx, "UPDATE captcha_pending SET phase = 'rules' WHERE id = $1", r.ID)
		r.Phase = "rules"
	}
}

// presentCaptcha sends the captcha message. It reports false when it could not be sent.
func (c *Ctx) presentCaptcha(r *CaptchaRow, cfg *CaptchaSettings) bool {
	msg, kb := c.captchaBody(r), c.captchaKeyboard(r, cfg.ButtonText)
	var sent *gotgbot.Message
	var err error
	if r.Mode == "text2" {
		sent, err = c.Bot.SendPhotoWithContext(c.Ctx, r.MessageChatID,
			gotgbot.InputFileByReader("captcha.png", bytes.NewReader(CaptchaPicture(r.Answer, nil))),
			&gotgbot.SendPhotoOpts{Caption: msg, ParseMode: parseHTML, ReplyMarkup: kb})
	} else {
		sent, err = c.Bot.SendMessageWithContext(c.Ctx, r.MessageChatID, msg, &gotgbot.SendMessageOpts{ParseMode: parseHTML, ReplyMarkup: kb})
	}
	if err != nil {
		slog.Info("Could not send a captcha", "to", r.MessageChatID, "err", err)
		return false
	}
	isPhoto := r.Mode == "text2"
	_, _ = c.App.DB.Exec(c.Ctx, "UPDATE captcha_pending SET message_id = $2, is_photo = $3 WHERE id = $1", r.ID, sent.MessageId, isPhoto)
	r.MessageID, r.IsPhoto = sent.MessageId, isPhoto
	return true
}

// --- Letting people in or out ---

func (c *Ctx) defaultPermissions(chatID int64) gotgbot.ChatPermissions {
	chat, err := c.Bot.GetChatWithContext(c.Ctx, chatID, nil)
	if err != nil || chat.Permissions == nil {
		return gotgbot.ChatPermissions{CanSendMessages: true}
	}
	return *chat.Permissions
}

func (c *Ctx) captchaStatus(chatID, userID int64) (string, *gotgbot.MergedChatMember) {
	member, err := c.Bot.GetChatMemberWithContext(c.Ctx, chatID, userID, nil)
	if err != nil {
		return "", nil
	}
	m := member.MergeChatMember()
	return m.Status, &m
}

// unmute gives the right to write back. It reports false when the person is gone or was changed by someone else.
func (c *Ctx) unmute(chatID, userID int64) bool {
	status, member := c.captchaStatus(chatID, userID)
	if status != "restricted" || member.CanSendMessages {
		return status == "member" || status == "administrator" || status == "creator"
	}
	if _, err := c.Bot.RestrictChatMemberWithContext(c.Ctx, chatID, userID, c.defaultPermissions(chatID), nil); err != nil {
		slog.Info("Could not unmute", "user", userID, "chat", chatID, "err", err)
		return false
	}
	return true
}

func (c *Ctx) captchaWelcome(r *CaptchaRow, user *gotgbot.User) {
	if user == nil {
		user = &gotgbot.User{Id: r.UserID, FirstName: r.Name}
	}
	for _, hook := range CaptchaReleased {
		hook(c, r.ChatID, user, r.JoinMessageID)
	}
}

func chatTitleOr(c *Ctx, title string) string {
	if title == "" {
		return c.T("captchas.the_chat")
	}
	return title
}

// ReleaseCaptcha: the person is through (or let go): unmute, tidy up, welcome.
func (c *Ctx) ReleaseCaptcha(r *CaptchaRow, user *gotgbot.User, greet bool) {
	c.App.markPassed(r.ChatID, r.UserID)
	_ = c.App.deleteCaptcha(c.Ctx, r.ID)
	if r.MessageID != 0 && r.MessageChatID != r.ChatID && r.Kind == "join" {
		c.sayCaptcha(r, c.T("captchas.done_you_can_speak_in_now", V{"v": text.Esc(chatTitleOr(c, r.Title))})) // solved in a private chat
	} else {
		c.removeCaptchaMessage(r)
	}
	if r.Kind == "request" {
		return
	}
	// A welcome message that carried the button was sent when the person joined: no second one.
	if c.unmute(r.ChatID, r.UserID) && greet && !r.Attached {
		c.captchaWelcome(r, user)
	}
}

func (c *Ctx) approveRequest(r *CaptchaRow) {
	c.App.markPassed(r.ChatID, r.UserID)
	_ = c.App.deleteCaptcha(c.Ctx, r.ID)
	if _, err := c.Bot.ApproveChatJoinRequestWithContext(c.Ctx, r.ChatID, r.UserID, nil); err != nil {
		slog.Info("Could not approve a join request", "user", r.UserID, "chat", r.ChatID, "err", err)
		c.sayCaptcha(r, c.T("captchas.you_passed_but_i_couldn_t"))
		return
	}
	c.sayCaptcha(r, c.T("captchas.done_welcome_to", V{"v": text.Esc(chatTitleOr(c, r.Title))}))
}

// kickCaptcha: time ran out; remove the person (they may try again) or dismiss their request.
func (c *Ctx) kickCaptcha(r *CaptchaRow) {
	_ = c.App.deleteCaptcha(c.Ctx, r.ID)
	if r.Kind == "request" {
		_, _ = c.Bot.DeclineChatJoinRequestWithContext(c.Ctx, r.ChatID, r.UserID, nil)
		c.sayCaptcha(r, c.T("captchas.time_s_up_your_request_to", V{"v": text.Esc(chatTitleOr(c, r.Title))}))
		return
	}
	c.removeCaptchaMessage(r)
	if status, _ := c.captchaStatus(r.ChatID, r.UserID); status == "left" || status == "kicked" {
		return
	}
	if _, err := c.Bot.BanChatMemberWithContext(c.Ctx, r.ChatID, r.UserID, nil); err != nil {
		slog.Info("Could not remove a person", "user", r.UserID, "chat", r.ChatID, "err", err)
		return
	}
	_, _ = c.Bot.UnbanChatMemberWithContext(c.Ctx, r.ChatID, r.UserID, &gotgbot.UnbanChatMemberOpts{OnlyIfBanned: true})
}

// dropCaptcha forgets the captcha without touching the person (a request stays for the admins).
func (c *Ctx) dropCaptcha(r *CaptchaRow) {
	_ = c.App.deleteCaptcha(c.Ctx, r.ID)
	c.removeCaptchaMessage(r)
}

// CaptchaDropUser: the person left (or was removed): their captcha message is pointless now. The row stays: a muted
// person who comes back is still muted, so they must be recognised and tested again.
func (c *Ctx) CaptchaDropUser(chatID, userID int64) {
	r, err := c.App.CaptchaFind(c.Ctx, chatID, userID)
	if err != nil || r == nil || r.Kind != "join" || r.MessageID == 0 {
		return
	}
	c.removeCaptchaMessage(r)
	_, _ = c.App.DB.Exec(c.Ctx, "UPDATE captcha_pending SET message_id = NULL WHERE id = $1", r.ID)
}

// ReleaseChat: captchas were turned off in the chat; nobody stays muted. It returns how many people were let go.
func (c *Ctx) ReleaseChat(chatID int64) (int, error) {
	rows, err := c.captchaRows("chat_id = $1 ORDER BY id", chatID)
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		c.letGo(r)
	}
	return len(rows), nil
}

func (c *Ctx) captchaRows(where string, args ...any) ([]*CaptchaRow, error) {
	rows, err := c.App.DB.Pool.Query(c.Ctx, "SELECT "+captchaColumns+" FROM captcha_pending WHERE "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CaptchaRow
	for rows.Next() {
		r := &CaptchaRow{}
		if err := rows.Scan(r.dest()...); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (c *Ctx) letGo(r *CaptchaRow) {
	if r.Kind == "request" {
		c.dropCaptcha(r)
	} else {
		c.ReleaseCaptcha(r, nil, true)
	}
}

// --- Joining ---

// WelcomeAvailable tells whether the chat welcomes newcomers. Captchas ride on the welcome message, so they need it.
func (c *Ctx) WelcomeAvailable(chatID int64) bool {
	if !c.App.ModuleActive(c.Ctx, chatID, "greetings") {
		return false
	}
	s, err := c.App.Greetings(c.Ctx, chatID)
	if err != nil {
		slog.Error("Looking up the welcome settings failed", "err", err)
		return false
	}
	return s.WelcomeOn
}

// HoldCaptcha mutes a newcomer and gives them a captcha when the chat wants one. It reports whether they are held.
//
// Captchas need the welcome message: it carries the button (CaptchaWelcomeFor), which is the same for every newcomer
// and works for whoever still has a captcha. Without welcomes nobody is muted. A newcomer whose welcome fails to
// carry the button gets a captcha message of their own after a while (the sweep).
func (c *Ctx) HoldCaptcha(chat gotgbot.Chat, user *gotgbot.User, joinMessageID int64, index int) bool {
	cfg := c.App.CaptchaActive(c.Ctx, chat.Id)
	if cfg == nil || !c.WelcomeAvailable(chat.Id) || c.captchaExempt(chat.Id, user) {
		return false
	}
	if c.App.RaidWasRemoved(chat.Id, user.Id) {
		return false // a raid took them away: nobody to test
	}
	if c.BanApplies(chat.Id, user.Id) {
		return false // being banned: muting them now would undo the ban
	}
	old, _ := c.App.CaptchaFind(c.Ctx, chat.Id, user.Id)
	status, _ := c.captchaStatus(chat.Id, user.Id)
	if status != "member" && !(status == "restricted" && old != nil) {
		return false // an admin, restricted by someone else or gone: nothing to test
	}
	if _, err := c.Bot.RestrictChatMemberWithContext(c.Ctx, chat.Id, user.Id, gotgbot.ChatPermissions{}, nil); err != nil {
		slog.Info("Could not mute for a captcha", "user", user.Id, "chat", chat.Id, "err", err)
		return false
	}
	if old != nil {
		c.dropCaptcha(old)
	}
	if _, err := c.insertCaptcha(chat, user, "join", cfg, joinMessageID, chat.Id, true); err != nil {
		slog.Error("Could not keep a captcha", "err", err)
		return false
	}
	if index >= CaptchaWelcomeLimit {
		// No welcome of their own, but the button under the others works for everybody.
		c.MarkWelcomed(chat.Id, user.Id)
	}
	return true
}

// HoldRequest: a join request; send the person a captcha privately. It reports whether it was sent.
func (c *Ctx) HoldRequest(chat gotgbot.Chat, user *gotgbot.User, userChatID int64) bool {
	cfg := c.App.CaptchaActive(c.Ctx, chat.Id)
	if cfg == nil || c.captchaExempt(chat.Id, user) {
		return false
	}
	if old, _ := c.App.CaptchaFind(c.Ctx, chat.Id, user.Id); old != nil {
		c.dropCaptcha(old)
	}
	r, err := c.insertCaptcha(chat, user, "request", cfg, 0, userChatID, false)
	if err != nil {
		slog.Error("Could not keep a captcha", "err", err)
		return false
	}
	c.rulesOnly(r, cfg)
	if !c.presentCaptcha(r, cfg) {
		_ = c.App.deleteCaptcha(c.Ctx, r.ID)
		return false
	}
	return true
}

// --- The welcome message's button ---

// CaptchaWelcomeButton is the button under welcome messages. It is the same for everybody and always a callback
// button: whoever presses it is looked up, so a person who is already verified is only told so, in a pop-up.
func (c *Ctx) CaptchaWelcomeButton(chatID int64) gotgbot.InlineKeyboardButton {
	cfg, _ := c.App.CaptchaSettings(c.Ctx, chatID)
	return gotgbot.InlineKeyboardButton{Text: c.captchaButton(cfg.ButtonText), CallbackData: fmt.Sprintf("%s:%d", CaptchaWelcomePrefix, chatID)}
}

// CaptchaWelcomeFor is for the greetings module: (skip, button) for a newcomer. skip: they have a captcha message of
// their own and are welcomed once through. button: the verify button for their welcome (nil for none).
func (c *Ctx) CaptchaWelcomeFor(chatID, userID int64) (skip bool, button *gotgbot.InlineKeyboardButton) {
	r, err := c.App.CaptchaFind(c.Ctx, chatID, userID)
	if err != nil || r == nil || r.Kind != "join" {
		return false, nil
	}
	if !r.Attached {
		return true, nil
	}
	b := c.CaptchaWelcomeButton(chatID)
	return false, &b
}

// WelcomePressed: somebody pressed the button under a welcome message. It returns what to tell them (note, as an
// alert or not) or a link to open (the private chat) instead.
func (c *Ctx) WelcomePressed(chatID int64, user *gotgbot.User) (note string, alert bool, url string) {
	r, err := c.App.CaptchaFind(c.Ctx, chatID, user.Id)
	if err != nil || r == nil || r.Kind != "join" {
		return c.T("captchas.you_re_already_verified"), true, ""
	}
	cfg, _ := c.App.CaptchaSettings(c.Ctx, chatID)
	simple := r.Mode == "button" && !(cfg.Rules && c.App.RulesContent(c.Ctx, chatID) != "")
	if simple {
		return c.PressCaptcha(r, 0, user), false, ""
	}
	return "", false, "https://t.me/" + c.Bot.Username + "?start=cap_" + strconv.FormatInt(chatID, 10)
}

// MarkWelcomed records that the person's welcome (or the one they share) carries the button.
func (c *Ctx) MarkWelcomed(chatID, userID int64) {
	_, _ = c.App.DB.Exec(c.Ctx, "UPDATE captcha_pending SET welcomed = TRUE WHERE chat_id = $1 AND user_id = $2", chatID, userID)
}

// OpenCaptchaInPrivate: the person came to the private chat from the button under a welcome message. It returns what
// to tell them, or "" when the captcha was sent.
func (c *Ctx) OpenCaptchaInPrivate(user *gotgbot.User, chatID, privateChatID int64) string {
	r, err := c.App.CaptchaFind(c.Ctx, chatID, user.Id)
	if err != nil || r == nil || r.Kind != "join" {
		return c.T("common.you_re_already_verified_or_this")
	}
	cfg := c.App.CaptchaActive(c.Ctx, r.ChatID)
	if cfg == nil {
		return c.T("captchas.captchas_are_switched_off_in_that")
	}
	if r.MessageID != 0 {
		c.removeCaptchaMessage(r)
	}
	_, _ = c.App.DB.Exec(c.Ctx, "UPDATE captcha_pending SET message_chat_id = $2, message_id = NULL, is_photo = FALSE WHERE id = $1", r.ID, privateChatID)
	r.MessageChatID, r.MessageID, r.IsPhoto = privateChatID, 0, false
	c.rulesOnly(r, cfg)
	if !c.presentCaptcha(r, cfg) {
		return c.T("captchas.i_couldn_t_send_you_the")
	}
	return ""
}

// --- Pressing buttons ---

func (a *App) captchaLock(id int64) *sync.Mutex {
	a.captcha.mu.Lock()
	defer a.captcha.mu.Unlock()
	if a.captcha.locks == nil || len(a.captcha.locks) > 500 {
		a.captcha.locks = map[int64]*sync.Mutex{}
	}
	l, ok := a.captcha.locks[id]
	if !ok {
		l = &sync.Mutex{}
		a.captcha.locks[id] = l
	}
	return l
}

// afterTest: the test is passed; the rules come next when the chat wants them accepted, else the person is in.
func (c *Ctx) afterTest(r *CaptchaRow, cfg *CaptchaSettings, user *gotgbot.User) {
	inWelcome := r.Attached && r.MessageChatID == r.ChatID // no message of ours to show the rules in
	if cfg.Rules && !inWelcome && c.App.RulesContent(c.Ctx, r.ChatID) != "" {
		_, _ = c.App.DB.Exec(c.Ctx, "UPDATE captcha_pending SET phase = 'rules' WHERE id = $1", r.ID)
		r.Phase = "rules"
		c.showCaptcha(r, cfg.ButtonText)
		return
	}
	c.finishCaptcha(r, user)
}

// RulesMissing tells whether the chat has no rules to accept, so /captcharules would have nothing to show.
func (c *Ctx) RulesMissing(chatID int64) bool { return c.App.RulesContent(c.Ctx, chatID) == "" }

func (c *Ctx) finishCaptcha(r *CaptchaRow, user *gotgbot.User) {
	if r.Kind == "request" {
		c.approveRequest(r)
	} else {
		c.ReleaseCaptcha(r, user, true)
	}
}

// PressCaptcha: a button of the test was pressed. It returns a short note to show the person ("" for none). The
// caller has checked that the button is theirs.
func (c *Ctx) PressCaptcha(r *CaptchaRow, index int, user *gotgbot.User) string {
	lock := c.App.captchaLock(r.ID)
	lock.Lock()
	defer lock.Unlock()
	r, err := c.App.CaptchaRowByID(c.Ctx, r.ID)
	if err != nil || r == nil || r.Phase != "captcha" {
		return c.T("captchas.this_captcha_is_no_longer_valid")
	}
	cfg, _ := c.App.CaptchaSettings(c.Ctx, r.ChatID)

	switch r.Mode {
	case "button":
		fromWelcome := r.Attached && r.MessageChatID == r.ChatID
		c.afterTest(r, &cfg, user)
		if fromWelcome {
			return c.T("captchas.you_re_verified_welcome")
		}
		return ""
	case "math":
		options := strings.Split(r.Layout, ",")
		if index >= 0 && index < len(options) && options[index] == r.Answer {
			c.afterTest(r, &cfg, user)
			return ""
		}
		fresh := MakeCaptcha("math", nil)
		_, _ = c.App.DB.Exec(c.Ctx, "UPDATE captcha_pending SET question = $2, answer = $3, layout = $4 WHERE id = $1", r.ID, fresh.Question, fresh.Answer, fresh.Layout)
		r.Question, r.Answer, r.Layout = fresh.Question, fresh.Answer, fresh.Layout
		c.showCaptcha(r, cfg.ButtonText)
		return c.T("captchas.wrong_here_s_a_new_one")
	}

	// text and text2
	taken := r.taken()
	layout, answer := []rune(r.Layout), []rune(r.Answer)
	if index < 0 || index >= len(layout) {
		return ""
	}
	for _, t := range taken {
		if t == index {
			return ""
		}
	}
	if layout[index] != answer[len(taken)] {
		_, _ = c.App.DB.Exec(c.Ctx, "UPDATE captcha_pending SET taken = '' WHERE id = $1", r.ID)
		r.Taken = ""
		c.showCaptcha(r, cfg.ButtonText)
		return c.T("captchas.wrong_order_start_again")
	}
	taken = append(taken, index)
	parts := make([]string, len(taken))
	for i, t := range taken {
		parts[i] = strconv.Itoa(t)
	}
	r.Taken = strings.Join(parts, ",")
	_, _ = c.App.DB.Exec(c.Ctx, "UPDATE captcha_pending SET taken = $2 WHERE id = $1", r.ID, r.Taken)
	if len(taken) == len(answer) {
		c.afterTest(r, &cfg, user)
	} else {
		c.showCaptcha(r, cfg.ButtonText)
	}
	return ""
}

// AcceptRules: the person accepted the rules shown in their captcha message.
func (c *Ctx) AcceptRules(r *CaptchaRow, user *gotgbot.User) string {
	lock := c.App.captchaLock(r.ID)
	lock.Lock()
	defer lock.Unlock()
	r, err := c.App.CaptchaRowByID(c.Ctx, r.ID)
	if err != nil || r == nil || r.Phase != "rules" {
		return c.T("captchas.this_is_no_longer_valid")
	}
	c.finishCaptcha(r, user)
	return ""
}

// --- The sweep ---

func (c *Ctx) handleCaptcha(r *CaptchaRow, now time.Time) {
	switch {
	case c.App.CaptchaActive(c.Ctx, r.ChatID) == nil:
		c.letGo(r) // captchas were turned off while the person waited
	case r.KickAt != nil && !r.KickAt.After(now):
		c.kickCaptcha(r)
	case r.UnmuteAt != nil && !r.UnmuteAt.After(now):
		c.ReleaseCaptcha(r, nil, true)
	case r.ExpiresAt != nil && !r.ExpiresAt.After(now):
		c.dropCaptcha(r)
	}
}

// SweepCaptchas is one round: it kicks, unmutes and forgets whoever is due.
func (a *App) SweepCaptchas(ctx context.Context, bot *gotgbot.Bot) error {
	base := &Ctx{App: a, Bot: bot, Ctx: ctx}
	rows, err := base.captchaRows("TRUE ORDER BY id LIMIT 500")
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, r := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c := &Ctx{App: a, Bot: bot, Ctx: ctx, L: a.UseChat(ctx, r.ChatID)}
		func() {
			defer func() {
				if p := recover(); p != nil {
					slog.Error("Handling a captcha panicked", "user", r.UserID, "chat", r.ChatID, "panic", p)
				}
			}()
			c.handleCaptcha(r, now)
		}()
	}
	return nil
}

// RunCaptchaSweeper runs SweepCaptchas until ctx ends.
func (a *App) RunCaptchaSweeper(ctx context.Context, bot *gotgbot.Bot) {
	for {
		if err := a.SweepCaptchas(ctx, bot); err != nil && ctx.Err() == nil {
			slog.Error("Captcha sweep failed", "err", err)
		}
		if !pause(ctx, CaptchaSweepEvery) {
			return
		}
	}
}

func inList(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
