// Package blacklist is the matching of blacklist triggers: words and phrases a chat does not
// allow.
//
// A trigger is text with an optional modifier and wildcards:
//
//	word              the word or phrase anywhere in a message, as whole words
//	exact:word        the whole message is word
//	prefix:word       the message starts with word
//	sticker:😀         a sticker with this emoji
//	stickerpack:name  any sticker of this pack
//
//	?    any one character except whitespace
//	*    any run of characters except whitespace (great for URLs)
//	**   any run of characters, whitespace included
//	\*  \?  a literal * or ?
//
// Matching ignores case. Go's regexp has no look-around, so the matcher is written by hand: it backtracks like a regular expression with look-around would, with memoised failures so that wildcards can't make it slow.
package blacklist

import (
	"regexp"
	"strings"
	"unicode"

	"argo/internal/i18n"
)

const (
	MaxTriggers      = 500
	MaxPattern       = 100
	MaxWildcards     = 4 // wildcard runs in one trigger
	MaxLongWildcards = 2 // of which ** (they can match anything, so they are the slow ones)
	MaxMatchedText   = 4096
)

var Modifiers = []string{"exact", "prefix", "sticker", "stickerpack"}

// Error says why a trigger text (or an action) is not acceptable; the message is for people.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

type tokenKind int

const (
	tLit   tokenKind = iota // one literal character, any case
	tOne                    // ?  one character except whitespace
	tRun                    // *  any run except whitespace
	tAny                    // ** any run
	tSpace                  // whitespace in the trigger: one or more whitespace characters
)

type token struct {
	kind tokenKind
	r    rune
}

// Trigger is one stored trigger, ready to match.
type Trigger struct {
	Pattern string // as stored
	Kind    string // plain / exact / prefix / sticker / stickerpack
	Body    string
	Action  string // "" = the chat's default action
	Seconds int
	Reason  string
	tokens  []token
}

// isSpace is white space, the way \s sees it.
func isSpace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }

// isWord is \w for text: letters, digits and the underscore.
func isWord(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) }

func foldEq(a, b rune) bool {
	if a == b {
		return true
	}
	for f := unicode.SimpleFold(a); f != a; f = unicode.SimpleFold(f) {
		if f == b {
			return true
		}
	}
	return false
}

// CollapseSpace is the text with every run of whitespace turned into one space, and trimmed.
func CollapseSpace(text string) string { return strings.Join(strings.FieldsFunc(text, isSpace), " ") }

// SplitLines splits a text into lines.
func SplitLines(text string) []string {
	var lines []string
	start := 0
	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		switch r := runes[i]; r {
		case '\n', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029, '\r':
			lines = append(lines, string(runes[start:i]))
			if r == '\r' && i+1 < len(runes) && runes[i+1] == '\n' {
				i++
			}
			start = i + 1
		}
	}
	if start < len(runes) {
		lines = append(lines, string(runes[start:]))
	}
	return lines
}

// IsSpace tells white space for one character.
func IsSpace(r rune) bool { return isSpace(r) }

// lower is like strings.ToLower, but İ becomes "i" and a combining dot, and a capital sigma at the
// end of a word becomes the final sigma.
func lower(s string) string {
	runes := []rune(s)
	var b strings.Builder
	for i, r := range runes {
		switch {
		case r == 0x130:
			b.WriteString("i\u0307")
		case r == 0x3a3:
			if i > 0 && unicode.IsLetter(runes[i-1]) && (i+1 == len(runes) || !unicode.IsLetter(runes[i+1])) {
				b.WriteRune(0x3c2)
			} else {
				b.WriteRune(0x3c3)
			}
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// Normalize is trim, lowercase, single spaces: the stored form, and what removal compares.
func Normalize(text string) string {
	return lower(strings.Join(strings.FieldsFunc(text, isSpace), " "))
}

// SplitModifier splits "exact:word" into ("exact", "word"); anything else is ("plain", pattern).
func SplitModifier(pattern string) (string, string) {
	head, rest, found := strings.Cut(pattern, ":")
	if found {
		for _, m := range Modifiers {
			if head == m {
				return head, strings.TrimSpace(rest)
			}
		}
	}
	return "plain", pattern
}

var packNameRE = regexp.MustCompile(`^[\p{L}\p{N}_]{1,64}$`)

// Check validates a trigger as typed and returns its stored form.
func Check(l i18n.Lang, text string) (string, error) {
	pattern := Normalize(text)
	if pattern == "" {
		return "", &Error{l.T("common.the_trigger_is_empty")}
	}
	if len([]rune(pattern)) > MaxPattern {
		return "", &Error{l.T("common.a_trigger_can_be_at_most", i18n.V{"MAX_PATTERN": MaxPattern})}
	}
	kind, body := SplitModifier(pattern)
	if body == "" {
		return "", &Error{l.T("blacklist.needs_something_after_it", i18n.V{"kind": kind})}
	}
	switch kind {
	case "stickerpack":
		body = strings.TrimRight(body, "/")
		if i := strings.LastIndex(body, "/"); i >= 0 { // accept t.me/addstickers/name
			body = body[i+1:]
		}
		if !packNameRE.MatchString(body) {
			return "", &Error{l.T("blacklist.a_sticker_pack_name_has_only")}
		}
		return "stickerpack:" + body, nil
	case "sticker":
		if strings.IndexFunc(body, isSpace) >= 0 {
			return "", &Error{l.T("blacklist.sticker_takes_one_emoji")}
		}
		return "sticker:" + body, nil
	}
	if _, err := glob(l, body); err != nil { // refuses too many wildcards
		return "", err
	}
	if kind != "plain" {
		return kind + ":" + body, nil
	}
	return body, nil
}

// glob turns wildcard text into tokens.
func glob(l i18n.Lang, body string) ([]token, error) {
	runes := []rune(body)
	var out []token
	runs, longRuns := 0, 0
	for i := 0; i < len(runes); {
		ch := runes[i]
		switch {
		case ch == '\\' && i+1 < len(runes) && (runes[i+1] == '*' || runes[i+1] == '?'):
			out = append(out, token{tLit, runes[i+1]})
			i += 2
		case ch == '*' && i+1 < len(runes) && runes[i+1] == '*':
			out = append(out, token{kind: tAny})
			runs++
			longRuns++
			i += 2
		case ch == '*':
			out = append(out, token{kind: tRun})
			runs++
			i++
		case ch == '?':
			out = append(out, token{kind: tOne})
			runs++
			i++
		case isSpace(ch):
			out = append(out, token{kind: tSpace})
			i++
		default:
			out = append(out, token{tLit, ch})
			i++
		}
	}
	if runs > MaxWildcards || longRuns > MaxLongWildcards {
		return nil, &Error{l.T("blacklist.too_many_wildcards_at_most_and", i18n.V{"MAX_WILDCARDS": MaxWildcards, "MAX_LONG_WILDCARDS": MaxLongWildcards})}
	}
	return out, nil
}

// EscapeWildcards is text to be matched literally (a replied message): its * and ? lose their meaning.
func EscapeWildcards(text string) string {
	return strings.NewReplacer("*", `\*`, "?", `\?`).Replace(text)
}

// Build makes a trigger from its stored form. An error means the pattern can't be used.
func Build(l i18n.Lang, pattern, action string, seconds int, reason string) (*Trigger, error) {
	kind, body := SplitModifier(pattern)
	t := &Trigger{Pattern: pattern, Kind: kind, Body: body, Action: action, Seconds: seconds, Reason: reason}
	if kind == "plain" || kind == "exact" || kind == "prefix" {
		var err error
		if t.tokens, err = glob(l, body); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// matcher runs the backtracking for one text.
type matcher struct {
	tokens []token
	text   []rune
	end    func(pos int) bool // may the match end here?
	failed map[[2]int]bool
}

func (m *matcher) run(tok, pos int) bool {
	if tok == len(m.tokens) {
		return m.end(pos)
	}
	key := [2]int{tok, pos}
	if m.failed[key] {
		return false
	}
	t := m.tokens[tok]
	ok := false
	switch t.kind {
	case tLit:
		ok = pos < len(m.text) && foldEq(m.text[pos], t.r) && m.run(tok+1, pos+1)
	case tOne:
		ok = pos < len(m.text) && !isSpace(m.text[pos]) && m.run(tok+1, pos+1)
	case tRun, tAny:
		// Greedy, then shorter: as the regular expression did.
		limit := pos
		for limit < len(m.text) && (t.kind == tAny || !isSpace(m.text[limit])) {
			limit++
		}
		for n := limit; n >= pos && !ok; n-- {
			ok = m.run(tok+1, n)
		}
	case tSpace:
		limit := pos
		for limit < len(m.text) && isSpace(m.text[limit]) {
			limit++
		}
		for n := limit; n > pos && !ok; n-- {
			ok = m.run(tok+1, n)
		}
	}
	if !ok {
		m.failed[key] = true
	}
	return ok
}

// matchesText tells whether a plain, exact or prefix trigger matches the text.
func (t *Trigger) matchesText(text []rune) bool {
	m := &matcher{tokens: t.tokens, text: text, failed: map[[2]int]bool{}}
	switch t.Kind {
	case "exact":
		m.end = func(pos int) bool { return pos == len(text) }
		return m.run(0, 0)
	case "prefix":
		m.end = func(int) bool { return true }
		return m.run(0, 0)
	}
	m.end = func(pos int) bool { return pos == len(text) || !isWord(text[pos]) }
	for start := 0; start <= len(text); start++ {
		if start > 0 && isWord(text[start-1]) {
			continue
		}
		if m.run(0, start) {
			return true
		}
	}
	return false
}

// Sticker is what a trigger needs to know about a sticker.
type Sticker struct{ Emoji, Pack string }

// Matches tells whether a message (its prepared text, or its sticker) matches the trigger.
func (t *Trigger) Matches(text []rune, sticker *Sticker) bool {
	switch t.Kind {
	case "sticker":
		return sticker != nil && sticker.Emoji == t.Body
	case "stickerpack":
		return sticker != nil && sticker.Pack != "" && strings.ToLower(sticker.Pack) == t.Body
	}
	return len(text) > 0 && t.matchesText(text)
}

// PrepareText is the text of a message as the triggers see it: trimmed and cut to MaxMatchedText characters.
func PrepareText(text string) []rune {
	r := []rune(strings.TrimFunc(text, isSpace))
	if len(r) > MaxMatchedText {
		r = r[:MaxMatchedText]
	}
	return r
}

// Find is the first trigger, in order, that a message matches and for which accept says yes (nil: any).
func Find(triggers []*Trigger, text string, sticker *Sticker, accept func(*Trigger) bool) *Trigger {
	prepared := PrepareText(text)
	for _, t := range triggers {
		if t.Matches(prepared, sticker) && (accept == nil || accept(t)) {
			return t
		}
	}
	return nil
}

// Word is a trigger that is always plain, whatever it looks like: the join blacklists have no modifiers, so
// "exact:foo" is a text to find, not a kind.
func Word(l i18n.Lang, body string) (*Trigger, error) {
	t := &Trigger{Pattern: body, Kind: "plain", Body: body}
	var err error
	if t.tokens, err = glob(l, body); err != nil {
		return nil, err
	}
	return t, nil
}
