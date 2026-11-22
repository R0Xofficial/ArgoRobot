package core

import (
	"context"
	"fmt"
	"sync"

	"argo/internal/i18n"
)

// Per-chat settings that belong to the chat's owner. Each is one yes/no switch with a
// default, read on many updates and therefore cached.
//
//	staff_help    whether the bot's staff (Owner, Developers, Sudo) may use the chat's admin commands. On by default.
//	admin_errors  whether people who are not admins get an answer when they try an admin command. On by default.
//	anon_admin    whether anonymous admins may use every command without being checked. Off by default.
//	anti_channel_pin  whether the posts of the linked channel that Telegram pins by itself are unpinned again. Off by default.
//	clean_linked  whether the posts of the linked channel that Telegram forwards into the chat are deleted. Off by default.
var settingDefaults = map[string]bool{
	"staff_help":       true,
	"gban_enforce":     true,
	"admin_errors":     true,
	"anon_admin":       false,
	"clean_commands":   false,
	"reports":          true,
	"anti_channel_pin": false,
	"clean_linked":     false,
}

type settingsCache struct {
	mu sync.Mutex
	m  map[string]map[int64]bool
}

func (s *settingsCache) forget(chatID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.m {
		delete(m, chatID)
	}
}

// Setting reads one of the switches (an unknown name is a programming error and panics).
func (a *App) Setting(ctx context.Context, name string, chatID int64) bool {
	def, ok := settingDefaults[name]
	if !ok {
		panic("unknown chat setting " + name)
	}
	a.settings.mu.Lock()
	if v, ok := a.settings.m[name][chatID]; ok {
		a.settings.mu.Unlock()
		return v
	}
	a.settings.mu.Unlock()

	value := def
	if err := a.DB.Row(ctx, fmt.Sprintf("SELECT %s FROM chat_settings WHERE chat_id = $1", name), []any{chatID}, &value); err != nil && err != errNoRows {
		logErr("Could not read a chat setting", err)
		return def
	} else if err == errNoRows {
		value = def
	}
	a.settings.put(name, chatID, value)
	return value
}

func (s *settingsCache) put(name string, chatID int64, v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil || len(s.m[name]) >= cacheLimit {
		s.m = map[string]map[int64]bool{}
	}
	if s.m[name] == nil {
		s.m[name] = map[int64]bool{}
	}
	s.m[name][chatID] = v
}

// SetSetting writes one of the switches.
func (a *App) SetSetting(ctx context.Context, name string, chatID int64, on bool) error {
	if _, ok := settingDefaults[name]; !ok {
		panic("unknown chat setting " + name)
	}
	_, err := a.DB.Exec(ctx, fmt.Sprintf(`INSERT INTO chat_settings (chat_id, %[1]s) VALUES ($1, $2)
ON CONFLICT (chat_id) DO UPDATE SET %[1]s = EXCLUDED.%[1]s`, name), chatID, on)
	if err == nil {
		a.settings.put(name, chatID, on)
	}
	return err
}

// --- The language of a chat ---

type langCache struct{ cache[int64, i18n.Lang] }

// ChatLanguage is the language a chat chose with /setlang (a private chat is a chat too, its id is the user's id).
func (a *App) ChatLanguage(ctx context.Context, chatID int64) (i18n.Lang, error) {
	if l, ok := a.langs.get(chatID); ok {
		return l, nil
	}
	code := i18n.Default
	err := a.DB.Row(ctx, "SELECT language FROM chat_settings WHERE chat_id = $1", []any{chatID}, &code)
	if err != nil && err != errNoRows {
		return i18n.English, err
	}
	if !i18n.Known(code) {
		code = i18n.Default
	}
	l := i18n.Lang(code)
	a.langs.put(chatID, l)
	return l, nil
}

func (a *App) SetChatLanguage(ctx context.Context, chatID int64, code string) error {
	_, err := a.DB.Exec(ctx, `INSERT INTO chat_settings (chat_id, language) VALUES ($1, $2)
ON CONFLICT (chat_id) DO UPDATE SET language = EXCLUDED.language`, chatID, code)
	if err == nil {
		a.langs.put(chatID, i18n.Lang(code))
	}
	return err
}

// UseChat is the language to speak for a chat; English when it can't be read.
func (a *App) UseChat(ctx context.Context, chatID int64) i18n.Lang {
	l, err := a.ChatLanguage(ctx, chatID)
	if err != nil {
		logErr("Could not read the language of a chat", err)
		return i18n.English
	}
	return l
}
