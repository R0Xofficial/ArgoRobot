package core

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"argo/internal/i18n"
)

// Welcome and goodbye messages of a chat (core/greetings.py). One row per chat holds both messages, their on/off
// switches and the other greeting settings. A chat without a row, or a message that was never set, uses the defaults.

// GreetSettings are the greeting settings of a chat. The messages are nil until they are set.
type GreetSettings struct {
	WelcomeOn          bool
	WelcomeContent     *string
	WelcomeMediaType   *string
	WelcomeMediaFileID *string
	GoodbyeOn          bool
	GoodbyeContent     *string
	GoodbyeMediaType   *string
	GoodbyeMediaFileID *string
	WelcomeMute        bool
	CleanWelcome       bool
	CleanService       bool
	LastWelcomeID      *int64
}

// GreetColumns are the columns UpdateGreetings may change.
var GreetColumns = []string{
	"welcome_on", "welcome_content", "welcome_media_type", "welcome_media_file_id",
	"goodbye_on", "goodbye_content", "goodbye_media_type", "goodbye_media_file_id",
	"welcome_mute", "clean_welcome", "clean_service", "last_welcome_id",
}

// Greetings reads the greeting settings of a chat.
func (a *App) Greetings(ctx context.Context, chatID int64) (GreetSettings, error) {
	s := GreetSettings{WelcomeOn: true, GoodbyeOn: true}
	_, err := a.DB.Found(ctx, `SELECT welcome_on, welcome_content, welcome_media_type, welcome_media_file_id,
goodbye_on, goodbye_content, goodbye_media_type, goodbye_media_file_id,
welcome_mute, clean_welcome, clean_service, last_welcome_id FROM greetings WHERE chat_id = $1`, []any{chatID},
		&s.WelcomeOn, &s.WelcomeContent, &s.WelcomeMediaType, &s.WelcomeMediaFileID,
		&s.GoodbyeOn, &s.GoodbyeContent, &s.GoodbyeMediaType, &s.GoodbyeMediaFileID,
		&s.WelcomeMute, &s.CleanWelcome, &s.CleanService, &s.LastWelcomeID)
	return s, err
}

// UpdateGreetings changes some settings of a chat; the others keep their value. A nil value clears a column.
func (a *App) UpdateGreetings(ctx context.Context, chatID int64, columns map[string]any) error {
	if len(columns) == 0 {
		return fmt.Errorf("no greeting settings to change")
	}
	names := make([]string, 0, len(columns))
	for name := range columns {
		known := false
		for _, c := range GreetColumns {
			known = known || c == name
		}
		if !known {
			return fmt.Errorf("unknown greeting setting %q", name)
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
	_, err := a.DB.Exec(ctx, fmt.Sprintf("INSERT INTO greetings (chat_id, %s) VALUES ($1, %s) ON CONFLICT (chat_id) DO UPDATE SET %s",
		strings.Join(names, ", "), strings.Join(marks, ", "), strings.Join(sets, ", ")), args...)
	return err
}

// Custom tells whether the chat wrote its own welcome ("welcome") or goodbye ("goodbye").
func (s GreetSettings) Custom(kind string) bool {
	if kind == "welcome" {
		return s.WelcomeContent != nil || s.WelcomeMediaType != nil
	}
	return s.GoodbyeContent != nil || s.GoodbyeMediaType != nil
}

// On tells whether the welcome or the goodbye is switched on.
func (s GreetSettings) On(kind string) bool {
	if kind == "welcome" {
		return s.WelcomeOn
	}
	return s.GoodbyeOn
}

// MessageOf is the text, media type and media file id to send for "welcome" or "goodbye".
func (s GreetSettings) MessageOf(l i18n.Lang, kind string) (content, mediaType, fileID string) {
	if !s.Custom(kind) {
		if kind == "welcome" {
			return l.T("greetings.default_welcome"), "", ""
		}
		return l.T("greetings.default_goodbye"), "", ""
	}
	if kind == "welcome" {
		return deref(s.WelcomeContent), deref(s.WelcomeMediaType), deref(s.WelcomeMediaFileID)
	}
	return deref(s.GoodbyeContent), deref(s.GoodbyeMediaType), deref(s.GoodbyeMediaFileID)
}

// StaffGreeting is the welcome or goodbye of a role (core/staffgreet.py): somebody with a role gets the message of
// their role instead of the chat's own, in every chat, and is never asked for a captcha.
func StaffGreeting(role Role, kind string) string {
	if kind == "welcome" {
		return map[Role]string{
			Support:   "🛟 Support member {mention} has arrived in {chatname}. Help is on the way.",
			Sudo:      "🛡 Sudo user {mention} has joined {chatname}. Behave, everyone.",
			Developer: "🛠 Developer {mention} has entered {chatname}. The bot is on its best behaviour.",
			Owner:     "👑 My owner {mention} has joined {chatname}. Everybody stand up.",
		}[role]
	}
	return map[Role]string{
		Support:   "🛟 Support member {first} has left. Hope it was a good shift.",
		Sudo:      "🛡 Sudo user {first} has left the chat. The chat is a bit less safe.",
		Developer: "🛠 Developer {first} has left. Please don't break anything while away.",
		Owner:     "👑 My owner {first} has left. Until next time.",
	}[role]
}
