package core

import (
	"context"
	"strings"

	"argo/internal/blacklist"
	"argo/internal/i18n"
)

// Join blacklists: words that keep people out when their profile contains them (core/joinblacklist.py). There are
// four kinds, one for each part of a profile that is checked: the name, the @username, the bio and the personal
// channel. A chat has at most MaxJoinPerKind triggers of each kind. Triggers use the wildcards of the blacklist and
// match whole words without caring about case. Every join in a chat with triggers is checked, so a chat's triggers
// are compiled once and kept in memory.

var JoinKinds = []string{"name", "username", "bio", "channel"}

const (
	MaxJoinPerKind = 10
	maxCheckedText = 1000 // a bio is at most 140 characters; this only bounds odd input
)

type joinEntry struct {
	pattern string
	trigger *blacklist.Trigger
}

// JoinLists are the compiled join blacklists of one chat.
type JoinLists struct{ kinds map[string][]joinEntry }

// JoinCheck validates a trigger as typed and returns its stored form.
func JoinCheck(l i18n.Lang, kind, text string) (string, error) {
	pattern := blacklist.Normalize(text)
	if kind == "username" {
		pattern = strings.TrimLeft(pattern, "@") // people write @name; usernames are checked without the @
	}
	if pattern == "" {
		return "", &blacklist.Error{Msg: l.T("common.the_trigger_is_empty")}
	}
	if len([]rune(pattern)) > blacklist.MaxPattern {
		return "", &blacklist.Error{Msg: l.T("common.a_trigger_can_be_at_most", i18n.V{"MAX_PATTERN": blacklist.MaxPattern})}
	}
	if _, err := blacklist.Word(l, pattern); err != nil { // refuses too many wildcards
		return "", err
	}
	return pattern, nil
}

func (a *App) joinLoad(ctx context.Context, chatID int64) (*JoinLists, error) {
	if cached, ok := a.joinLists.get(chatID); ok {
		return cached, nil
	}
	rows, err := a.DB.Pool.Query(ctx, "SELECT kind, pattern FROM join_blacklist WHERE chat_id = $1 ORDER BY kind, pattern", chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	built := &JoinLists{kinds: map[string][]joinEntry{}}
	for rows.Next() {
		var kind, pattern string
		if err := rows.Scan(&kind, &pattern); err != nil {
			return nil, err
		}
		t, err := blacklist.Word(i18n.English, pattern)
		if err != nil {
			continue
		}
		built.kinds[kind] = append(built.kinds[kind], joinEntry{pattern, t})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	a.joinLists.put(chatID, built)
	return built, nil
}

// JoinPatterns are the stored triggers of a chat, by kind.
func (a *App) JoinPatterns(ctx context.Context, chatID int64) (map[string][]string, error) {
	loaded, err := a.joinLoad(ctx, chatID)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, kind := range JoinKinds {
		for _, e := range loaded.kinds[kind] {
			out[kind] = append(out[kind], e.pattern)
		}
	}
	return out, nil
}

// JoinHas tells whether the chat has triggers of this kind.
func (a *App) JoinHas(ctx context.Context, chatID int64, kind string) bool {
	loaded, err := a.joinLoad(ctx, chatID)
	return err == nil && len(loaded.kinds[kind]) > 0
}

// JoinAdd adds a trigger (as JoinCheck returns it): "added", "exists" or "full".
func (a *App) JoinAdd(ctx context.Context, chatID int64, kind, pattern string) (string, error) {
	loaded, err := a.joinLoad(ctx, chatID)
	if err != nil {
		return "", err
	}
	current := loaded.kinds[kind]
	for _, e := range current {
		if e.pattern == pattern {
			return "exists", nil
		}
	}
	if len(current) >= MaxJoinPerKind {
		return "full", nil
	}
	if _, err := a.DB.Exec(ctx, "INSERT INTO join_blacklist (chat_id, kind, pattern) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING", chatID, kind, pattern); err != nil {
		return "", err
	}
	a.joinLists.drop(chatID)
	return "added", nil
}

func (a *App) JoinRemove(ctx context.Context, chatID int64, kind, pattern string) (bool, error) {
	loaded, err := a.joinLoad(ctx, chatID)
	if err != nil {
		return false, err
	}
	known := false
	for _, e := range loaded.kinds[kind] {
		known = known || e.pattern == pattern
	}
	if !known {
		return false, nil
	}
	if _, err := a.DB.Exec(ctx, "DELETE FROM join_blacklist WHERE chat_id = $1 AND kind = $2 AND pattern = $3", chatID, kind, pattern); err != nil {
		return false, err
	}
	a.joinLists.drop(chatID)
	return true, nil
}

// JoinMatch is the first trigger of this kind that the text matches, or "".
func (a *App) JoinMatch(ctx context.Context, chatID int64, kind, text string) string {
	r := []rune(strings.TrimSpace(text))
	if len(r) > maxCheckedText {
		r = r[:maxCheckedText]
	}
	if len(r) == 0 {
		return ""
	}
	loaded, err := a.joinLoad(ctx, chatID)
	if err != nil {
		return ""
	}
	for _, e := range loaded.kinds[kind] {
		if e.trigger.Matches(r, nil) {
			return e.pattern
		}
	}
	return ""
}
