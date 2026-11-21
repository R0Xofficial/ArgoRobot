package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // zone names must work where the system has no zone database

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/jackc/pgx/v5"
)

// Nightmode: the chat is closed for the night and opens again in the morning (core/nightmode.py).
//
// When the night starts the bot saves the chat's permissions and takes away everybody's right to send messages;
// when it ends it puts the saved permissions back. Admins can always write. A sweep every half minute starts and
// ends nights; the saved permissions are kept in the database, so a restart in the middle of a night loses nothing.

const (
	NightSweepEvery   = 30 * time.Second
	nightFailureCD    = 300 * time.Second
	DefaultNightStart = 23 * 60 // minutes after midnight
	DefaultNightEnd   = 7 * 60
	DefaultNightZone  = "UTC"
)

// NightSettings are the nightmode settings of a chat.
type NightSettings struct {
	Enabled bool
	Start   int
	End     int
	Zone    string
	Active  bool            // the bot has closed the chat and not opened it yet
	Saved   map[string]bool // the chat's permissions from before the night; nil when none
}

func defaultNight() NightSettings {
	return NightSettings{Start: DefaultNightStart, End: DefaultNightEnd, Zone: DefaultNightZone}
}

var (
	offsetRE   = regexp.MustCompile(`(?i)^(?:utc|gmt)?\s*([+-])\s*(\d{1,2})(?::?(\d{2}))?$`)
	zoneNameRE = regexp.MustCompile(`^[A-Za-z_]+(?:/[A-Za-z0-9_+\-]+){1,2}$`)
	clockRE    = regexp.MustCompile(`^(\d{1,2})(?::(\d{2}))?$`)
)

// ParseZone turns 'Europe/Warsaw', 'UTC+2' or '+5:30' into the name to keep and the location; ok is false for
// anything else.
func ParseZone(s string) (name string, loc *time.Location, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil, false
	}
	switch strings.ToUpper(s) {
	case "UTC", "GMT", "Z":
		return "UTC", time.UTC, true
	}
	if m := offsetRE.FindStringSubmatch(s); m != nil {
		hours, _ := strconv.Atoi(m[2])
		minutes := 0
		if m[3] != "" {
			minutes, _ = strconv.Atoi(m[3])
		}
		if hours > 14 || minutes > 59 {
			return "", nil, false
		}
		secs := hours*3600 + minutes*60
		if m[1] == "-" {
			secs = -secs
		}
		name = fmt.Sprintf("UTC%s%02d:%02d", m[1], hours, minutes)
		return name, time.FixedZone(name, secs), true
	}
	if zoneNameRE.MatchString(s) {
		if loc, err := time.LoadLocation(s); err == nil {
			return s, loc, true
		}
	}
	return "", nil, false
}

// ZoneOf is the location of a stored zone name (UTC when it can't be read).
func ZoneOf(name string) *time.Location {
	if _, loc, ok := ParseZone(name); ok {
		return loc
	}
	return time.UTC
}

// ParseClock turns '23:00', '7' or '07:30' into minutes after midnight; ok is false for anything else.
func ParseClock(s string) (int, bool) {
	m := clockRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, false
	}
	hours, _ := strconv.Atoi(m[1])
	minutes := 0
	if m[2] != "" {
		minutes, _ = strconv.Atoi(m[2])
	}
	if hours > 23 || minutes > 59 {
		return 0, false
	}
	return hours*60 + minutes, true
}

// ClockText is minutes after midnight as 07:30.
func ClockText(minutes int) string { return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60) }

// IsNight tells whether now is inside the night. A night may pass midnight (23:00-07:00); equal hours mean no night.
func (s NightSettings) IsNight(now time.Time) bool {
	local := now.In(ZoneOf(s.Zone))
	minute := local.Hour()*60 + local.Minute()
	switch {
	case s.Start == s.End:
		return false
	case s.Start < s.End:
		return s.Start <= minute && minute < s.End
	}
	return minute >= s.Start || minute < s.End
}

func parseSaved(raw *string) map[string]bool {
	if raw == nil || *raw == "" {
		return nil
	}
	var saved map[string]bool
	if json.Unmarshal([]byte(*raw), &saved) != nil {
		return nil
	}
	return saved
}

// NightSettings reads the nightmode settings of a chat.
func (a *App) NightSettings(ctx context.Context, chatID int64) (NightSettings, error) {
	s := defaultNight()
	var saved *string
	_, err := a.DB.Found(ctx, "SELECT enabled, start_minute, end_minute, zone, active, saved FROM nightmode WHERE chat_id = $1",
		[]any{chatID}, &s.Enabled, &s.Start, &s.End, &s.Zone, &s.Active, &saved)
	s.Saved = parseSaved(saved)
	return s, err
}

// UpdateNight changes enabled, start, end or zone (the keys of values); the rest stays.
func (a *App) UpdateNight(ctx context.Context, chatID int64, values map[string]any) error {
	cur, err := a.NightSettings(ctx, chatID)
	if err != nil {
		return err
	}
	if v, ok := values["enabled"]; ok {
		cur.Enabled = v.(bool)
	}
	if v, ok := values["start"]; ok {
		cur.Start = v.(int)
	}
	if v, ok := values["end"]; ok {
		cur.End = v.(int)
	}
	if v, ok := values["zone"]; ok {
		cur.Zone = v.(string)
	}
	_, err = a.DB.Exec(ctx, `INSERT INTO nightmode (chat_id, enabled, start_minute, end_minute, zone) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (chat_id) DO UPDATE SET enabled = EXCLUDED.enabled, start_minute = EXCLUDED.start_minute,
end_minute = EXCLUDED.end_minute, zone = EXCLUDED.zone`, chatID, cur.Enabled, cur.Start, cur.End, cur.Zone)
	return err
}

func (a *App) setNightState(ctx context.Context, chatID int64, active bool, saved map[string]bool) error {
	var raw *string
	if saved != nil {
		b, err := json.Marshal(saved)
		if err != nil {
			return err
		}
		s := string(b)
		raw = &s
	}
	_, err := a.DB.Exec(ctx, "UPDATE nightmode SET active = $2, saved = $3 WHERE chat_id = $1", chatID, active, raw)
	return err
}

// sendRights are the permissions the night takes away.
var sendRights = []string{
	"can_send_messages", "can_send_audios", "can_send_documents", "can_send_photos", "can_send_videos",
	"can_send_video_notes", "can_send_voice_notes", "can_send_polls", "can_send_other_messages", "can_add_web_page_previews",
}

func isSendRight(name string) bool {
	for _, r := range sendRights {
		if r == name {
			return true
		}
	}
	return false
}

// permissionsOf turns a saved map into ChatPermissions (unknown names are ignored).
func permissionsOf(m map[string]bool) gotgbot.ChatPermissions {
	var p gotgbot.ChatPermissions
	b, _ := json.Marshal(m)
	_ = json.Unmarshal(b, &p)
	return p
}

func closedPermissions(saved map[string]bool) gotgbot.ChatPermissions {
	rest := map[string]bool{}
	for k, v := range saved {
		if !isSendRight(k) {
			rest[k] = v
		}
	}
	return permissionsOf(rest) // every right that is not there is off
}

func reopenedPermissions(saved map[string]bool) gotgbot.ChatPermissions {
	if len(saved) > 0 {
		return permissionsOf(saved)
	}
	all := map[string]bool{}
	for _, r := range sendRights {
		all[r] = true
	}
	return permissionsOf(all)
}

// savedOf is the permissions of a chat as a map of the rights that are on and off.
func savedOf(p *gotgbot.ChatPermissions) map[string]bool {
	out := map[string]bool{}
	if p == nil {
		return out
	}
	b, _ := json.Marshal(p)
	var raw map[string]bool
	_ = json.Unmarshal(b, &raw)
	for k, v := range raw {
		out[k] = v
	}
	for _, r := range sendRights { // the ones that were off are written out, as Telegram reported them
		if _, ok := out[r]; !ok {
			out[r] = false
		}
	}
	return out
}

func (a *App) closeChat(ctx context.Context, bot *gotgbot.Bot, chatID int64) error {
	chat, err := bot.GetChatWithContext(ctx, chatID, nil)
	if err != nil {
		return err
	}
	saved := savedOf(chat.Permissions)
	// Saved first: if closing fails nothing is left behind, if the bot dies in between the chat is still restored.
	if err := a.setNightState(ctx, chatID, true, saved); err != nil {
		return err
	}
	if _, err := bot.SetChatPermissionsWithContext(ctx, chatID, closedPermissions(saved), &gotgbot.SetChatPermissionsOpts{UseIndependentChatPermissions: true}); err != nil {
		_ = a.setNightState(ctx, chatID, false, nil)
		return err
	}
	return nil
}

func (a *App) reopenChat(ctx context.Context, bot *gotgbot.Bot, chatID int64, saved map[string]bool) error {
	if _, err := bot.SetChatPermissionsWithContext(ctx, chatID, reopenedPermissions(saved), &gotgbot.SetChatPermissionsOpts{UseIndependentChatPermissions: true}); err != nil {
		return err
	}
	return a.setNightState(ctx, chatID, false, nil)
}

// ReconcileNight brings a chat in line with the clock. It returns "closed" or "opened" when it changed it, else "".
// Telegram's refusal (no right to change the permissions) is the error.
func (a *App) ReconcileNight(ctx context.Context, bot *gotgbot.Bot, chatID int64, s *NightSettings) (string, error) {
	if s == nil {
		cur, err := a.NightSettings(ctx, chatID)
		if err != nil {
			return "", err
		}
		s = &cur
	}
	night := s.Enabled && s.IsNight(time.Now())
	switch {
	case night && !s.Active:
		return "closed", a.closeChat(ctx, bot, chatID)
	case !night && s.Active:
		return "opened", a.reopenChat(ctx, bot, chatID, s.Saved)
	}
	return "", nil
}

// AnnounceNight says in the chat that the night began or ended (or that it could not be started).
func (a *App) AnnounceNight(ctx context.Context, bot *gotgbot.Bot, chatID int64, what string, s NightSettings) {
	l := a.UseChat(ctx, chatID)
	var msg string
	switch what {
	case "closed":
		msg = l.T("nightmode.night_mode_is_on_the_chat", V{"clock_text": ClockText(s.End), "zone": s.Zone})
	case "opened":
		msg = l.T("nightmode.night_mode_is_over_the_chat")
	default:
		msg = l.T("nightmode.night_mode_is_on_but_i")
	}
	if _, err := bot.SendMessageWithContext(ctx, chatID, msg, nil); err != nil {
		slog.Warn("Nightmode could not write to chat", "chat", chatID)
	}
}

// SweepNight is one round over every chat that has nightmode on or is closed by it.
func (a *App) SweepNight(ctx context.Context, bot *gotgbot.Bot) error {
	rows, err := a.DB.Pool.Query(ctx, "SELECT chat_id, enabled, start_minute, end_minute, zone, active, saved FROM nightmode WHERE enabled OR active")
	if err != nil {
		return err
	}
	type item struct {
		chat int64
		s    NightSettings
	}
	var items []item
	for rows.Next() {
		it := item{s: defaultNight()}
		var saved *string
		if err := rows.Scan(&it.chat, &it.s.Enabled, &it.s.Start, &it.s.End, &it.s.Zone, &it.s.Active, &saved); err != nil {
			rows.Close()
			return err
		}
		it.s.Saved = parseSaved(saved)
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil && err != pgx.ErrNoRows {
		return err
	}
	for _, it := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if until, ok := a.nightFail.Load(it.chat); ok && time.Now().Before(until.(time.Time)) {
			continue
		}
		s := it.s
		what, err := a.ReconcileNight(ctx, bot, it.chat, &s)
		if err != nil {
			a.nightFail.Store(it.chat, time.Now().Add(nightFailureCD))
			slog.Warn("Nightmode could not change the permissions of a chat", "chat", it.chat, "err", err)
			a.AnnounceNight(ctx, bot, it.chat, "failed", s)
			continue
		}
		if what != "" {
			a.AnnounceNight(ctx, bot, it.chat, what, s)
		}
	}
	return nil
}

// RunNightSweeper runs SweepNight until ctx ends.
func (a *App) RunNightSweeper(ctx context.Context, bot *gotgbot.Bot) {
	for {
		if err := a.SweepNight(ctx, bot); err != nil && ctx.Err() == nil {
			slog.Error("Nightmode sweep failed", "err", err)
		}
		if !pause(ctx, NightSweepEvery) {
			return
		}
	}
}
