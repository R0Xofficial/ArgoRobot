package core

import (
	"context"
	"regexp"
	"strings"
	"unicode"

	"argo/internal/i18n"
)

// Filters: saved replies to keywords. A trigger is a word (as a whole word), a "phrase" in
// double quotes, prefix:word (messages that start with it), exact:word (messages that are exactly it), or
// (a, b, "c d") for several triggers that share a reply. Matching ignores case. Every message of every chat is
// checked, so a chat's triggers are compiled once and kept in memory.

const (
	MaxFilters    = 300
	MaxFilterPat  = 100
	MaxFilterGrp  = 20
	MaxFilterBody = 4000
)

var filterModifiers = []string{"prefix", "exact"}

// FilterError: the command can't be understood; Msg says why.
type FilterError struct{ Msg string }

func (e *FilterError) Error() string { return e.Msg }

func normalizeFilter(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

func closingQuote(r rune) (rune, bool) {
	switch r {
	case '"':
		return '"', true
	case '“':
		return '”', true
	}
	return 0, false
}

// readOne is the trigger at the start of s: its stored pattern and the rest of the text.
func readOne(l i18n.Lang, s string) (string, string, error) {
	s = strings.TrimLeftFunc(s, unicode.IsSpace)
	modifier := ""
	lowered := strings.ToLower(s)
	for _, name := range filterModifiers {
		if strings.HasPrefix(lowered, name+":") {
			modifier = name + ":"
			s = s[len(modifier):]
			break
		}
	}
	var body, rest string
	if r := []rune(s); len(r) > 0 {
		if closing, ok := closingQuote(r[0]); ok {
			inner := string(r[1:])
			end := strings.IndexRune(inner, closing)
			if end == -1 {
				return "", "", &FilterError{l.T("filterstore.a_quote_is_not_closed_close")}
			}
			body, rest = inner[:end], inner[end+len(string(closing)):]
		} else {
			f := strings.Fields(s)
			if len(f) > 0 {
				body = f[0]
				rest = s[len(body):]
			}
		}
	}
	body = normalizeFilter(body)
	if body == "" {
		return "", "", &FilterError{l.T("filterstore.the_keyword_is_empty")}
	}
	pattern := modifier + body
	if len([]rune(pattern)) > MaxFilterPat {
		return "", "", &FilterError{l.T("filterstore.a_keyword_can_be_at_most", i18n.V{"MAX_PATTERN": MaxFilterPat})}
	}
	return pattern, rest, nil
}

func splitGroup(l i18n.Lang, inner string) ([]string, error) {
	var items []string
	var cur []rune
	var quote rune
	for _, ch := range inner {
		switch {
		case quote != 0:
			cur = append(cur, ch)
			if ch == quote {
				quote = 0
			}
		default:
			if closing, ok := closingQuote(ch); ok {
				quote = closing
				cur = append(cur, ch)
			} else if ch == ',' {
				items = append(items, string(cur))
				cur = nil
			} else {
				cur = append(cur, ch)
			}
		}
	}
	if quote != 0 {
		return nil, &FilterError{l.T("filterstore.a_quote_is_not_closed_close")}
	}
	return append(items, string(cur)), nil
}

// ParseFilterTriggers reads the triggers at the start of a command's arguments, and the rest (the reply text).
func ParseFilterTriggers(l i18n.Lang, args string) ([]string, string, error) {
	s := strings.TrimLeftFunc(args, unicode.IsSpace)
	if s == "" {
		return nil, "", &FilterError{l.T("filterstore.give_me_a_keyword")}
	}
	if s[0] != '(' {
		p, rest, err := readOne(l, s)
		if err != nil {
			return nil, "", err
		}
		return []string{p}, strings.TrimSpace(rest), nil
	}
	var quote rune
	end := -1
	for i, ch := range s {
		if i == 0 {
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			continue
		}
		if closing, ok := closingQuote(ch); ok {
			quote = closing
		} else if ch == ')' {
			end = i
			break
		}
	}
	if end == -1 {
		return nil, "", &FilterError{l.T("filterstore.a_parenthesis_is_not_closed_finish")}
	}
	items, err := splitGroup(l, s[1:end])
	if err != nil {
		return nil, "", err
	}
	var patterns []string
	for _, item := range items {
		if strings.TrimSpace(item) == "" {
			continue
		}
		p, rest, err := readOne(l, item)
		if err != nil {
			return nil, "", err
		}
		if strings.TrimSpace(rest) != "" {
			return nil, "", &FilterError{l.T("filterstore.put_keywords_with_spaces_in_double", i18n.V{"strip": strings.TrimSpace(item)})}
		}
		dup := false
		for _, x := range patterns {
			dup = dup || x == p
		}
		if !dup {
			patterns = append(patterns, p)
		}
	}
	if len(patterns) == 0 {
		return nil, "", &FilterError{l.T("filterstore.the_list_of_keywords_is_empty")}
	}
	if len(patterns) > MaxFilterGrp {
		return nil, "", &FilterError{l.T("filterstore.at_most_keywords_in_one_filter", i18n.V{"MAX_GROUP": MaxFilterGrp})}
	}
	return patterns, strings.TrimSpace(s[end+1:]), nil
}

// --- Matching ---------------------------------------------------------------------------------

type filterTrigger struct {
	pattern string
	kind    string // plain / prefix / exact
	re      *regexp.Regexp
}

const spaceRun = `[\s\p{Z}\x{85}]+`

func compileFilter(pattern string) filterTrigger {
	kind, body := "plain", pattern
	if k, b, ok := strings.Cut(pattern, ":"); ok {
		for _, m := range filterModifiers {
			if k == m {
				kind, body = k, b
			}
		}
	}
	words := strings.Split(body, " ")
	for i, w := range words {
		words[i] = regexp.QuoteMeta(w)
	}
	literal := strings.Join(words, spaceRun)
	src := "(?i)" + literal
	switch kind {
	case "prefix":
		src = `(?i)\A` + literal
	case "exact":
		src = `(?i)\A` + literal + `\z`
	}
	return filterTrigger{pattern, kind, regexp.MustCompile(src)}
}

func isWordRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

func (t filterTrigger) matches(s string) bool {
	if t.kind != "plain" {
		return t.re.MatchString(s)
	}
	for _, loc := range t.re.FindAllStringIndex(s, -1) {
		start, end := loc[0], loc[1]
		if start > 0 {
			if r := []rune(s[:start]); isWordRune(r[len(r)-1]) {
				continue
			}
		}
		if end < len(s) {
			if r := []rune(s[end:]); isWordRune(r[0]) {
				continue
			}
		}
		return true
	}
	return false
}

// The break between two answers of the same filter, per chat (/filtercooldown).
const (
	DefaultFilterCooldown = 5 // seconds
	MaxFilterCooldown     = 3600
)

// FilterCooldown is the seconds the same filter waits before it answers again in this chat (0: it answers every time).
func (a *App) FilterCooldown(ctx context.Context, chatID int64) int {
	if v, ok := a.filterWaits.get(chatID); ok {
		return v
	}
	v := DefaultFilterCooldown
	if err := a.DB.Row(ctx, "SELECT cooldown FROM filter_settings WHERE chat_id = $1", []any{chatID}, &v); err != nil && err != errNoRows {
		logErr("Could not read the filter cooldown", err)
		return DefaultFilterCooldown
	} else if err == errNoRows {
		v = DefaultFilterCooldown
	}
	a.filterWaits.put(chatID, v)
	return v
}

// SetFilterCooldown sets the break; a negative number puts back the default. It returns what is in force now.
func (a *App) SetFilterCooldown(ctx context.Context, chatID int64, seconds int) (int, error) {
	var err error
	if seconds < 0 || seconds == DefaultFilterCooldown {
		seconds = DefaultFilterCooldown
		_, err = a.DB.Exec(ctx, "DELETE FROM filter_settings WHERE chat_id = $1", chatID)
	} else {
		_, err = a.DB.Exec(ctx, `INSERT INTO filter_settings (chat_id, cooldown) VALUES ($1, $2)
ON CONFLICT (chat_id) DO UPDATE SET cooldown = EXCLUDED.cooldown`, chatID, seconds)
	}
	if err == nil {
		a.filterWaits.put(chatID, seconds)
	}
	return seconds, err
}

type filterList struct{ triggers []filterTrigger }

func (a *App) filterTriggers(ctx context.Context, chatID int64) (*filterList, error) {
	if c, ok := a.filterCache.get(chatID); ok {
		return c, nil
	}
	rows, err := a.DB.Pool.Query(ctx, "SELECT pattern FROM filters WHERE chat_id = $1 ORDER BY id", chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	built := &filterList{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		built.triggers = append(built.triggers, compileFilter(p))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	a.filterCache.put(chatID, built)
	return built, nil
}

// FilterMatch is the first trigger (by age) that the text matches, or "".
func (a *App) FilterMatch(ctx context.Context, chatID int64, text string) string {
	l, err := a.filterTriggers(ctx, chatID)
	if err != nil {
		return ""
	}
	text = strings.TrimSpace(text)
	for _, t := range l.triggers {
		if t.matches(text) {
			return t.pattern
		}
	}
	return ""
}

// --- Storage ----------------------------------------------------------------------------------

type FilterRow struct{ Pattern, Content, MediaType, MediaID string }

func (a *App) GetFilter(ctx context.Context, chatID int64, pattern string) (*FilterRow, error) {
	var r FilterRow
	var mt, mid *string
	found, err := a.DB.Found(ctx, "SELECT pattern, content, media_type, media_file_id FROM filters WHERE chat_id = $1 AND pattern = $2",
		[]any{chatID, pattern}, &r.Pattern, &r.Content, &mt, &mid)
	if err != nil || !found {
		return nil, err
	}
	if mt != nil {
		r.MediaType = *mt
	}
	if mid != nil {
		r.MediaID = *mid
	}
	return &r, nil
}

func (a *App) FilterPatterns(ctx context.Context, chatID int64) ([]string, error) {
	l, err := a.filterTriggers(ctx, chatID)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(l.triggers))
	for i, t := range l.triggers {
		out[i] = t.pattern
	}
	return out, nil
}

// SaveFilter saves a reply under every trigger: which were added, which updated, which refused (the chat is full).
func (a *App) SaveFilter(ctx context.Context, chatID int64, wanted []string, content, mediaType, mediaID string, createdBy int64) (added, updated, full []string, err error) {
	current, err := a.FilterPatterns(ctx, chatID)
	if err != nil {
		return nil, nil, nil, err
	}
	have := map[string]bool{}
	for _, p := range current {
		have[p] = true
	}
	var mt, mid any
	if mediaType != "" {
		mt, mid = mediaType, mediaID
	}
	for _, p := range wanted {
		switch {
		case have[p]:
			updated = append(updated, p)
		case len(current)+len(added) >= MaxFilters:
			full = append(full, p)
			continue
		default:
			added = append(added, p)
		}
		if _, err = a.DB.Exec(ctx, `INSERT INTO filters (chat_id, pattern, content, media_type, media_file_id, created_by)
VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (chat_id, pattern) DO UPDATE SET content = EXCLUDED.content,
media_type = EXCLUDED.media_type, media_file_id = EXCLUDED.media_file_id`, chatID, p, content, mt, mid, createdBy); err != nil {
			a.filterCache.drop(chatID)
			return nil, nil, nil, err
		}
	}
	a.filterCache.drop(chatID)
	return added, updated, full, nil
}

func (a *App) RemoveFilter(ctx context.Context, chatID int64, pattern string) (bool, error) {
	n, err := a.DB.Exec(ctx, "DELETE FROM filters WHERE chat_id = $1 AND pattern = $2", chatID, pattern)
	a.filterCache.drop(chatID)
	return n > 0, err
}

func (a *App) ClearFilters(ctx context.Context, chatID int64) (int64, error) {
	n, err := a.DB.Exec(ctx, "DELETE FROM filters WHERE chat_id = $1", chatID)
	a.filterCache.drop(chatID)
	return n, err
}
