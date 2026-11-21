package modules

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/core"
	"argo/internal/i18n"
	"argo/internal/text"
)

// Warn commands for chat admins (modules/warns.py). Usable by chat admins (with the right to ban users) and by
// the bot's Owner, Developers and Sudo users.
//
// Not ported yet: the warn filters (/addwarn, /nowarn, /warnlist). They are blacklist triggers with the action
// "warn" and wait for the blacklist module.

const (
	defaultWarnLimit = 3
	maxWarnLimit     = 10
	cbWarnUndo       = "warn:undo:" // the button under a fresh warning
	cbWarnDelete     = "warn:del:"  // the buttons in a /warns list
)

func registerWarns(r *core.Router) {
	r.Command(cmdWarn, "warn", "dwarn")
	r.Command(cmdWarns, "warns")
	r.Button(cbWarnUndo, cbUndoWarn)
	r.Button(cbWarnDelete, cbDeleteWarn)
	r.Command(cmdRmwarn, "rmwarn")
	r.Command(cmdResetwarns, "resetwarns", "resetwarn")
	r.Command(cmdUnwarn, "unwarn")
	r.Command(cmdWarnlimit, "warnlimit")
	r.Command(cmdSetwarnlimit, "setwarnlimit")
	r.Command(cmdStrongwarn, "strongwarn")
	r.Command(cmdWarnmode, "warnmode")
}

// --- Storage ---

func warnLimit(c *core.Ctx, chatID int64) (int, error) {
	var limit int
	found, err := c.App.DB.Found(c.Ctx, "SELECT warn_limit FROM warn_settings WHERE chat_id = $1", []any{chatID}, &limit)
	if err != nil || !found {
		return defaultWarnLimit, err
	}
	return limit, nil
}

func setWarnLimit(c *core.Ctx, chatID int64, limit int) error {
	_, err := c.App.DB.Exec(c.Ctx, `INSERT INTO warn_settings (chat_id, warn_limit) VALUES ($1, $2)
ON CONFLICT (chat_id) DO UPDATE SET warn_limit = EXCLUDED.warn_limit`, chatID, limit)
	return err
}

// warnMode is what reaching the warn limit does: ban (default), kick, mute, tban or tmute (and its length).
func warnMode(c *core.Ctx, chatID int64) (string, int, error) {
	mode, seconds := "ban", 0
	found, err := c.App.DB.Found(c.Ctx, "SELECT limit_mode, limit_seconds FROM warn_settings WHERE chat_id = $1", []any{chatID}, &mode, &seconds)
	if err != nil || !found {
		return "ban", 0, err
	}
	return mode, seconds, nil
}

func setWarnMode(c *core.Ctx, chatID int64, mode string, seconds int) error {
	_, err := c.App.DB.Exec(c.Ctx, `INSERT INTO warn_settings (chat_id, warn_limit, strong, limit_mode, limit_seconds)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (chat_id) DO UPDATE SET strong = EXCLUDED.strong, limit_mode = EXCLUDED.limit_mode,
limit_seconds = EXCLUDED.limit_seconds`, chatID, defaultWarnLimit, mode != "kick", mode, seconds)
	return err
}

// modeText: 'ban', 'tmute for 2 hours'.
func modeText(l i18n.Lang, mode string, seconds int) string {
	if core.IsTimedMode(mode) {
		return l.T("common.for", core.V{"mode": l.Word(mode), "duration_text": text.DurationText(l, seconds)})
	}
	return l.Word(mode)
}

type warn struct {
	ID        int64
	UserID    int64
	UserName  string
	Reason    string
	ByName    string
	ByStaff   bool
	CreatedAt time.Time
}

func addWarn(c *core.Ctx, chatID int64, t *core.Target, reason string, actor *core.Actor) (int64, error) {
	var by any
	if actor.ID != 0 {
		by = actor.ID
	}
	var id int64
	err := c.App.DB.Row(c.Ctx, `INSERT INTO warns (chat_id, user_id, user_name, reason, warned_by, warned_by_name, warned_by_staff)
VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		[]any{chatID, t.ID, nilIfEmpty(t.Name), nilIfEmpty(reason), by, actor.Name, actor.Staff}, &id)
	return id, err
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func listWarns(c *core.Ctx, chatID, userID int64) ([]warn, error) {
	rows, err := c.App.DB.Pool.Query(c.Ctx, `SELECT id, user_id, user_name, reason, warned_by_name, warned_by_staff, created_at
FROM warns WHERE chat_id = $1 AND user_id = $2 ORDER BY id`, chatID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []warn
	for rows.Next() {
		var w warn
		var name, reason, byName *string
		if err := rows.Scan(&w.ID, &w.UserID, &name, &reason, &byName, &w.ByStaff, &w.CreatedAt); err != nil {
			return nil, err
		}
		w.UserName, w.Reason, w.ByName = deref(name), deref(reason), deref(byName)
		out = append(out, w)
	}
	return out, rows.Err()
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// deleteWarn removes one warn of this chat; nil when there is no such warn here.
func deleteWarn(ctx context.Context, c *core.Ctx, chatID, warnID int64) (*warn, error) {
	var w warn
	var name *string
	found, err := c.App.DB.Found(ctx, "DELETE FROM warns WHERE id = $1 AND chat_id = $2 RETURNING id, user_id, user_name",
		[]any{warnID, chatID}, &w.ID, &w.UserID, &name)
	if err != nil || !found {
		return nil, err
	}
	w.UserName = deref(name)
	return &w, nil
}

func clearWarns(c *core.Ctx, chatID, userID int64) (int64, error) {
	return c.App.DB.Exec(c.Ctx, "DELETE FROM warns WHERE chat_id = $1 AND user_id = $2", chatID, userID)
}

// --- Wording ---

func warnUndoKeyboard(c *core.Ctx, id int64) *gotgbot.InlineKeyboardMarkup {
	return core.Keyboard([]gotgbot.InlineKeyboardButton{core.Button(c.T("warns.remove_warn"), cbWarnUndo+itoa(id))})
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func warnsView(c *core.Ctx, who string, warns []warn, limit int) (string, *gotgbot.InlineKeyboardMarkup) {
	if len(warns) == 0 {
		return c.T("warns.has_no_warns", core.V{"who": who}), nil
	}
	lines := []string{c.T("warns.has_warns", core.V{"who": who, "warns": len(warns), "limit": limit}), ""}
	for _, w := range warns {
		reason := c.T("warns.no_reason_given")
		if w.Reason != "" {
			reason = text.Esc(text.Truncate(w.Reason, 100))
		}
		lines = append(lines, fmt.Sprintf("<code>#%d</code> %s", w.ID, reason))
		by := w.ByName
		if by == "" {
			by = c.T("common.unknown")
		}
		by = text.Esc(by)
		if w.ByStaff {
			by += c.T("warns.bot_staff")
		}
		lines = append(lines, fmt.Sprintf("By %s on %s", by, w.CreatedAt.Format("2006-01-02")))
	}
	var rows [][]gotgbot.InlineKeyboardButton
	var row []gotgbot.InlineKeyboardButton
	for _, w := range warns {
		row = append(row, core.Button(c.T("warns.remove", core.V{"id": w.ID}), cbWarnDelete+itoa(w.ID)))
		if len(row) == 2 {
			rows, row = append(rows, row), nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	return strings.Join(lines, "\n"), core.Keyboard(rows...)
}

// --- /warn ---

func cmdWarn(c *core.Ctx) error {
	deleting := c.Cmd == "dwarn"
	target, actor := c.Begin(core.BeginOpts{})
	if target == nil {
		return nil
	}
	var replied *gotgbot.Message
	if deleting {
		if replied = c.RequireReply(); replied == nil {
			return nil
		}
	}
	chatID := c.Msg.Chat.Id
	reason := strings.TrimSpace(target.Extra)
	warnID, err := addWarn(c, chatID, target, reason, actor)
	if err != nil {
		return err
	}
	if deleting {
		// Best effort: the message may already be gone.
		_, _ = c.Bot.DeleteMessageWithContext(c.Ctx, chatID, replied.MessageId, nil)
	}
	warns, err := listWarns(c, chatID, target.ID)
	if err != nil {
		return err
	}
	count := len(warns)
	limit, err := warnLimit(c, chatID)
	if err != nil {
		return err
	}
	who := c.Who(target)
	c.LogAction("WARNED", actor, target, reason, valueDetail("warns.warns", fmt.Sprintf("<code>%d/%d</code>", count, limit)))
	msg := c.T("warns.was_warned", core.V{
		"who": who, "count": count, "limit": limit, "by": c.By(actor), "reason_line": c.ReasonLine(reason), "staff_note": c.StaffNote(actor),
	})
	if count < limit {
		_, err = c.Reply(msg, warnUndoKeyboard(c, warnID))
		return err
	}

	// The limit is reached: ban (or kick, or ...), and start them from zero if they ever come back.
	mode, seconds, err := warnMode(c, chatID)
	if err != nil {
		return err
	}
	done, err := c.Punish(chatID, target.ID, mode, seconds)
	if err != nil {
		if me, ok := err.(*core.ModerationError); ok {
			// The warns stay, so the situation is not lost; the button still works.
			_, err = c.Reply(c.T("warns.the_limit_is_reached_but_i", core.V{"text": msg, "exc": text.Esc(me.Msg)}), warnUndoKeyboard(c, warnID))
		}
		return err
	}
	if _, err := clearWarns(c, chatID, target.ID); err != nil {
		return err
	}
	c.LogAction("WARNLIMIT", actor, target, reason, func(l i18n.Lang) (string, string) {
		return l.T("warns.result"), core.Outcome(l, mode, seconds)
	})
	_, err = c.ReplyText(c.T("warns.reached_the_warn_limit_and_was", core.V{
		"who": who, "count": count, "limit": limit, "done": done, "by": c.By(actor),
		"reason_line": c.ReasonLine(reason), "staff_note": c.StaffNote(actor),
	}))
	return err
}

// --- /warns and the buttons ---

func cmdWarns(c *core.Ctx) error {
	target, _ := c.Begin(core.BeginOpts{Unpunitive: true})
	if target == nil {
		return nil
	}
	warns, err := listWarns(c, c.Msg.Chat.Id, target.ID)
	if err != nil {
		return err
	}
	limit, err := warnLimit(c, c.Msg.Chat.Id)
	if err != nil {
		return err
	}
	msg, kb := warnsView(c, c.Who(target), warns, limit)
	_, err = c.Reply(msg, kb)
	return err
}

func warnID(data, prefix string) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimPrefix(data, prefix), 10, 64)
	return id, err == nil && id >= 0 && !strings.HasPrefix(strings.TrimPrefix(data, prefix), "-")
}

func cbUndoWarn(c *core.Ctx) error {
	id, valid := warnID(c.Cb.Data, cbWarnUndo)
	actor, ok := c.AuthorizeCallback("")
	if !ok {
		return nil
	}
	if !valid {
		c.Answer(c.T("common.this_button_is_not_valid"), true)
		return nil
	}
	removed, err := deleteWarn(c.Ctx, c, c.Msg.Chat.Id, id)
	if err != nil {
		return err
	}
	if removed == nil {
		c.Answer(c.T("warns.this_warn_no_longer_exists"), true)
		c.RemoveButtons()
		return nil
	}
	c.Answer("", false)
	return c.Edit(c.T("warns.the_warn_of_was_removed", core.V{
		"id": removed.ID, "who": text.UserLink(removed.UserID, removed.UserName), "by": c.By(actor), "staff_note": c.StaffNote(actor),
	}), nil)
}

func cbDeleteWarn(c *core.Ctx) error {
	id, valid := warnID(c.Cb.Data, cbWarnDelete)
	if _, ok := c.AuthorizeCallback(""); !ok {
		return nil
	}
	if !valid {
		c.Answer(c.T("common.this_button_is_not_valid"), true)
		return nil
	}
	chatID := c.Msg.Chat.Id
	removed, err := deleteWarn(c.Ctx, c, chatID, id)
	if err != nil {
		return err
	}
	if removed == nil {
		c.Answer(c.T("warns.this_warn_no_longer_exists"), true)
		return nil
	}
	c.Answer(c.T("warns.warn_removed", core.V{"id": removed.ID}), false)
	warns, err := listWarns(c, chatID, removed.UserID)
	if err != nil {
		return err
	}
	limit, err := warnLimit(c, chatID)
	if err != nil {
		return err
	}
	msg, kb := warnsView(c, text.UserLink(removed.UserID, removed.UserName), warns, limit)
	return c.Edit(msg, kb)
}

// --- /rmwarn, /resetwarns ---

func cmdRmwarn(c *core.Ctx) error {
	actor := c.BeginChatCommand(core.AuthorizeOpts{})
	if actor == nil {
		return nil
	}
	arg := strings.TrimPrefix(strings.TrimSpace(c.Args), "#")
	id, err := strconv.ParseInt(arg, 10, 64)
	if err != nil || id < 0 || strings.HasPrefix(arg, "+") || strings.HasPrefix(arg, "-") {
		c.Say(c.T("warns.give_the_id_of_the_warn"))
		return nil
	}
	removed, err := deleteWarn(c.Ctx, c, c.Msg.Chat.Id, id)
	if err != nil {
		return err
	}
	if removed == nil {
		c.Say(c.T("warns.there_is_no_warn_with_that"))
		return nil
	}
	_, err = c.ReplyText(c.T("warns.the_warn_of_was_removed", core.V{
		"id": removed.ID, "who": text.UserLink(removed.UserID, removed.UserName), "by": c.By(actor), "staff_note": c.StaffNote(actor),
	}))
	return err
}

func cmdResetwarns(c *core.Ctx) error {
	target, actor := c.Begin(core.BeginOpts{Unpunitive: true})
	if target == nil {
		return nil
	}
	removed, err := clearWarns(c, c.Msg.Chat.Id, target.ID)
	if err != nil {
		return err
	}
	who := c.Who(target)
	if removed == 0 {
		c.Say(c.T("warns.has_no_warns", core.V{"who": who}))
		return nil
	}
	c.LogAction("WARNSRESET", actor, target, "", valueDetail("warns.removed", fmt.Sprintf("<code>%d</code>", removed)))
	_, err = c.ReplyText(c.T("warns.all_warns_of_were_removed", core.V{
		"who": who, "removed": removed, "by": c.By(actor), "staff_note": c.StaffNote(actor),
	}))
	return err
}

// --- /unwarn ---

func cmdUnwarn(c *core.Ctx) error {
	target, actor := c.Begin(core.BeginOpts{Unpunitive: true})
	if target == nil {
		return nil
	}
	chatID := c.Msg.Chat.Id
	warns, err := listWarns(c, chatID, target.ID)
	if err != nil {
		return err
	}
	who := c.Who(target)
	if len(warns) == 0 {
		c.Say(c.T("warns.has_no_warns", core.V{"who": who}))
		return nil
	}
	if _, err := deleteWarn(c.Ctx, c, chatID, warns[len(warns)-1].ID); err != nil {
		return err
	}
	limit, err := warnLimit(c, chatID)
	if err != nil {
		return err
	}
	c.LogAction("UNWARNED", actor, target, "", valueDetail("warns.warns", fmt.Sprintf("<code>%d/%d</code>", len(warns)-1, limit)))
	_, err = c.ReplyText(c.T("warns.the_latest_warn_of_was_removed", core.V{
		"who": who, "by": c.By(actor), "v": len(warns) - 1, "limit": limit, "staff_note": c.StaffNote(actor),
	}))
	return err
}

// --- Limit ---

func cmdWarnlimit(c *core.Ctx) error {
	if c.BeginChatCommand(core.AuthorizeOpts{}) == nil {
		return nil
	}
	limit, err := warnLimit(c, c.Msg.Chat.Id)
	if err != nil {
		return err
	}
	_, err = c.ReplyText(c.T("warns.the_warn_limit_in_this_chat", core.V{"limit": limit}))
	return err
}

func cmdSetwarnlimit(c *core.Ctx) error {
	if c.BeginChatCommand(core.AuthorizeOpts{}) == nil {
		return nil
	}
	arg := strings.TrimSpace(c.Args)
	n, err := strconv.Atoi(arg)
	if err != nil || n < 1 || n > maxWarnLimit || strings.HasPrefix(arg, "+") {
		c.Say(c.T("warns.give_a_number_from_1_to", core.V{"MAX_LIMIT": maxWarnLimit}))
		return nil
	}
	if err := setWarnLimit(c, c.Msg.Chat.Id, n); err != nil {
		return err
	}
	_, err = c.ReplyText(c.T("warns.the_warn_limit_in_this_chat_2", core.V{"argument": n}))
	return err
}

// cmdStrongwarn is the old switch: on = ban at the limit, off = kick. /warnmode has the rest of the choices.
func cmdStrongwarn(c *core.Ctx) error {
	if c.BeginChatCommand(core.AuthorizeOpts{}) == nil {
		return nil
	}
	chatID := c.Msg.Chat.Id
	if arg := strings.TrimSpace(c.Args); arg != "" {
		on, ok := text.ParseSwitch(arg)
		if !ok {
			c.Say(c.T("warns.use_strongwarn_on_or_strongwarn_off"))
			return nil
		}
		mode := "kick"
		if on {
			mode = "ban"
		}
		if err := setWarnMode(c, chatID, mode, 0); err != nil {
			return err
		}
	}
	mode, seconds, err := warnMode(c, chatID)
	if err != nil {
		return err
	}
	_, err = c.ReplyText(c.T("warns.a_user_who_reaches_the_warn", core.V{"mode_text": text.Esc(modeText(c.L, mode, seconds))}))
	return err
}

// ParseAction reads an action such as 'ban' or 'tmute 30m' (core/blacklist.py parse_action, without "off").
// It returns the mode and its length in seconds; a *ActionError says what is wrong with the text.
func ParseAction(l i18n.Lang, spec string) (string, int, error) {
	words := strings.Fields(strings.ToLower(spec))
	if len(words) == 0 {
		return "", 0, &ActionError{l.T("blacklist.the_action_is_empty")}
	}
	name := words[0]
	if name == "delete" {
		name = "del"
	}
	valid := name == "del" || name == "warn" || core.IsEnforceMode(name)
	if !valid {
		return "", 0, &ActionError{l.T("blacklist.unknown_action_use_one_of", core.V{
			"v": words[0], "options": "delete, ban, mute, kick, warn, tban <time>, tmute <time>",
		})}
	}
	if core.IsTimedMode(name) {
		var d time.Duration
		ok := false
		if len(words) == 2 {
			d, ok = core.ParseDuration(words[1])
		}
		if !ok || d < core.MinDuration || d > core.MaxDuration {
			return "", 0, &ActionError{l.T("blacklist.needs_a_duration_like_30m_2h", core.V{"name": name})}
		}
		return name, int(d.Seconds()), nil
	}
	if len(words) > 1 {
		return "", 0, &ActionError{l.T("blacklist.doesn_t_take_a_duration_use", core.V{"name": name})}
	}
	return name, 0, nil
}

// ActionError is a message about an unacceptable action text.
type ActionError struct{ Msg string }

func (e *ActionError) Error() string { return e.Msg }

func cmdWarnmode(c *core.Ctx) error {
	if c.BeginChatCommand(core.AuthorizeOpts{}) == nil {
		return nil
	}
	spec := strings.TrimSpace(c.Args)
	chatID := c.Msg.Chat.Id
	usage := c.T("warns.warnmode_usage")
	if spec != "" {
		mode, seconds, err := ParseAction(c.L, spec)
		if err != nil {
			c.Say(text.Esc(err.Error()) + "\n" + usage)
			return nil
		}
		if !core.IsEnforceMode(mode) { // delete and warn make no sense at the limit
			c.Say(usage)
			return nil
		}
		if err := setWarnMode(c, chatID, mode, seconds); err != nil {
			return err
		}
		_, err = c.ReplyText(c.T("warns.a_user_who_reaches_the_warn_2", core.V{"mode_text": text.Esc(modeText(c.L, mode, seconds))}))
		return err
	}
	mode, seconds, err := warnMode(c, chatID)
	if err != nil {
		return err
	}
	_, err = c.ReplyText(c.T("warns.a_user_who_reaches_the_warn_3", core.V{
		"mode_text": text.Esc(modeText(c.L, mode, seconds)), "WARNMODE_USAGE": usage,
	}))
	return err
}
