// Package text builds Telegram HTML safely. The bot sends HTML, so anything not written by us (names,
// titles, exception messages) is escaped first.
package text

import (
	"fmt"
	"html"
	"regexp"
	"strings"
	"unicode/utf8"

	"argo/internal/i18n"
)

var escaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// Esc escapes text for use between HTML tags. Only &, < and > matter there; quotes stay readable.
func Esc(value any) string { return escaper.Replace(fmt.Sprint(value)) }

var tagRE = regexp.MustCompile(`<[^>]*>`)

// StripTags is the plain-text version of an HTML message (the fallback when Telegram rejects the markup).
func StripTags(s string) string { return html.UnescapeString(tagRE.ReplaceAllString(s, "")) }

// Truncate shortens text to at most limit characters.
func Truncate(s string, limit int, suffix ...string) string {
	suf := "..."
	if len(suffix) > 0 {
		suf = suffix[0]
	}
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	runes := []rune(s)
	keep := limit - utf8.RuneCountInString(suf)
	if keep < 0 {
		keep = 0
	}
	return string(runes[:keep]) + suf
}

// UserLink is a clickable mention that opens the profile. Without a known name there is nothing to click: user[id].
func UserLink(id int64, name string) string {
	if strings.TrimSpace(name) == "" {
		return fmt.Sprintf("user[%d]", id)
	}
	return fmt.Sprintf(`<a href="tg://user?id=%d">%s</a>`, id, Esc(name))
}

// UserRef is the profile link followed by the id: Name [123456789].
func UserRef(id int64, name string) string {
	return fmt.Sprintf("%s [<code>%d</code>]", UserLink(id, name), id)
}

// DurationText turns 90 into '1 minute 30 seconds' (the unit words come from the language's time.* tags).
func DurationText(l i18n.Lang, seconds int) string {
	var parts []string
	for _, unit := range []struct {
		name string
		size int
	}{{"day", 86400}, {"hour", 3600}, {"minute", 60}, {"second", 1}} {
		count := seconds / unit.size
		seconds %= unit.size
		if count > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", count, l.PluralTag("time."+unit.name, count)))
		}
	}
	return strings.Join(parts, " ")
}

// ParseSwitch is true for on/yes, false for off/no; ok is false for anything else. Case does not matter.
func ParseSwitch(value string) (on, ok bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "on", "yes":
		return true, true
	case "off", "no":
		return false, true
	}
	return false, false
}
