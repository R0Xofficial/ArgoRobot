// Package i18n holds the languages of the bot's texts.
//
// Code never contains the text itself, only a tag: l.T("bans.was_banned", V{"who": name}).
// The texts live in locales/<code>.json, one file per language, as {"tag": "text"} with {placeholders}.
// The files are the same ones the Python version reads. locales/en.json is the reference and has every
// tag; another language may lack some, and a missing tag is shown in English, so a missing line is never
// an error. Besides the texts a file holds its own settings: meta.name, meta.flag, meta.plural
// ("one-other" or "one-few-many") and the forms of the time units (time.day ...).
package i18n

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const Default = "en"

// V are the values filled into {placeholders}.
type V map[string]any

type catalogs struct {
	mu     sync.RWMutex
	dir    string
	langs  map[string]map[string]string
	order  []string
	flags  map[string]string
	names  map[string]string
	loaded bool
}

var all = &catalogs{}

// Load finds every language in dir: a <code>.json file is a language. English comes first, then by code.
func Load(dir string) error {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return err
	}
	langs := map[string]map[string]string{}
	names := map[string]string{}
	flags := map[string]string{}
	var codes []string
	for _, file := range files {
		code := strings.TrimSuffix(filepath.Base(file), ".json")
		raw, err := os.ReadFile(file)
		if err != nil {
			slog.Error("Could not read a language", "code", code, "err", err)
			continue
		}
		cat := map[string]string{}
		if err := json.Unmarshal(raw, &cat); err != nil {
			slog.Error("Could not load a language", "code", code, "err", err)
			continue
		}
		if len(cat) == 0 {
			continue
		}
		langs[code] = cat
		names[code] = firstNonEmpty(cat["meta.name"], code)
		if cat["meta.flag"] != "" {
			flags[code] = cat["meta.flag"]
		}
		codes = append(codes, code)
	}
	if _, ok := langs[Default]; !ok {
		return fmt.Errorf("%s/%s.json is missing: English is the reference language", dir, Default)
	}
	sort.Slice(codes, func(i, j int) bool {
		if (codes[i] == Default) != (codes[j] == Default) {
			return codes[i] == Default
		}
		return codes[i] < codes[j]
	})
	all.mu.Lock()
	all.dir, all.langs, all.order, all.names, all.flags, all.loaded = dir, langs, codes, names, flags, true
	all.mu.Unlock()
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// Codes lists the known languages, English first.
func Codes() []string {
	all.mu.RLock()
	defer all.mu.RUnlock()
	return append([]string(nil), all.order...)
}

// Known reports whether code is a language.
func Known(code string) bool {
	all.mu.RLock()
	defer all.mu.RUnlock()
	_, ok := all.langs[code]
	return ok
}

// Label is a language as shown to people: its flag and its own name.
func Label(code string) string {
	all.mu.RLock()
	defer all.mu.RUnlock()
	name, ok := all.names[code]
	if !ok {
		return code
	}
	if flag := all.flags[code]; flag != "" {
		return flag + " " + name
	}
	return name
}

// Lang is a language code; its methods write texts in that language.
type Lang string

// English is for logs and for the commands of the bot's staff.
const English Lang = Default

func (l Lang) code() string {
	if Known(string(l)) {
		return string(l)
	}
	return Default
}

// find returns the text of a tag in this language, else in English. local says it came from this language.
func (l Lang) find(tag string) (text string, local, found bool) {
	all.mu.RLock()
	defer all.mu.RUnlock()
	if code := l.code(); code != Default {
		if t, ok := all.langs[code][tag]; ok {
			return t, true, true
		}
	}
	t, ok := all.langs[Default][tag]
	return t, false, ok
}

// T returns the text behind a tag in this language, with the {placeholders} filled from the values.
// An unknown tag is shown as it is.
func (l Lang) T(tag string, values ...V) string {
	text, local, found := l.find(tag)
	if !found {
		text = tag // not a known tag: show it as it is
	}
	if len(values) == 0 {
		return text
	}
	out, err := format(text, values[0])
	if err != nil {
		slog.Warn("Broken text", "tag", tag, "lang", string(l), "err", err)
		if local {
			english, _, _ := English.find(tag)
			if out, err = format(english, values[0]); err == nil {
				return out
			}
		}
		return text
	}
	return out
}

// Or is like T, but a tag with no text anywhere gives def (for names that are the same in every language).
func (l Lang) Or(tag, def string) string {
	if _, _, ok := l.find(tag); ok {
		return l.T(tag)
	}
	return def
}

// Word is a word from a fixed list (ban, kick, mute ...) in this language; one that is not listed stays as it is.
func (l Lang) Word(value string) string { return l.Or("word."+value, value) }

// Plural picks the form for a number by the language's meta.plural: "one-few-many" (Polish) or the default
// "one-other" (English): `one` for 1 and `many` otherwise.
func (l Lang) Plural(count int, one, few, many string) string {
	all.mu.RLock()
	rule := all.langs[l.code()]["meta.plural"]
	all.mu.RUnlock()
	if rule != "one-few-many" {
		if count == 1 {
			return one
		}
		return many
	}
	switch {
	case count == 1:
		return one
	case count%10 >= 2 && count%10 <= 4 && !(count%100 >= 12 && count%100 <= 14):
		return few
	}
	return many
}

// Forms are the three forms of a tag written as "one|few|many" (a language with two forms writes "one|other").
func (l Lang) Forms(tag string) (string, string, string) {
	parts := strings.Split(l.T(tag), "|")
	for len(parts) < 3 {
		parts = append(parts, parts[len(parts)-1])
	}
	return parts[0], parts[1], parts[2]
}

// PluralTag is the form of a "one|few|many" tag that fits the number.
func (l Lang) PluralTag(tag string, count int) string {
	one, few, many := l.Forms(tag)
	return l.Plural(count, one, few, many)
}

// format fills {name} placeholders. "{{" and "}}" are literal braces; a format spec ({x:.1f}) is applied.
func format(text string, values V) (string, error) {
	var out strings.Builder
	for i := 0; i < len(text); i++ {
		ch := text[i]
		switch {
		case ch == '{' && i+1 < len(text) && text[i+1] == '{':
			out.WriteByte('{')
			i++
		case ch == '}' && i+1 < len(text) && text[i+1] == '}':
			out.WriteByte('}')
			i++
		case ch == '{':
			end := strings.IndexByte(text[i:], '}')
			if end < 0 {
				return "", fmt.Errorf("unclosed { in %q", text)
			}
			field := text[i+1 : i+end]
			i += end
			name, spec, _ := strings.Cut(field, ":")
			name, conv, _ := strings.Cut(name, "!")
			value, ok := values[name]
			if !ok {
				return "", fmt.Errorf("no value for {%s}", name)
			}
			if conv == "r" {
				value = "'" + fmt.Sprint(value) + "'" // the repr of a plain string
			}
			out.WriteString(render(value, spec))
		default:
			out.WriteByte(ch)
		}
	}
	return out.String(), nil
}

func render(value any, spec string) string {
	if t, ok := value.(time.Time); ok && strings.HasPrefix(spec, "%") {
		return strftime(t, spec)
	}
	if spec != "" {
		switch spec[len(spec)-1] {
		case 'f', 'g', 'e':
			return fmt.Sprintf("%"+spec, value)
		case 'd':
			return fmt.Sprintf("%"+spec, value)
		}
	}
	return fmt.Sprint(value)
}

// strftime is the few directives Python's format of a datetime ({now:%H:%M}) is used with here.
func strftime(t time.Time, spec string) string {
	var out strings.Builder
	for i := 0; i < len(spec); i++ {
		if spec[i] != '%' || i+1 == len(spec) {
			out.WriteByte(spec[i])
			continue
		}
		i++
		switch spec[i] {
		case 'H':
			out.WriteString(t.Format("15"))
		case 'M':
			out.WriteString(t.Format("04"))
		case 'S':
			out.WriteString(t.Format("05"))
		case 'd':
			out.WriteString(t.Format("02"))
		case 'm':
			out.WriteString(t.Format("01"))
		case 'Y':
			out.WriteString(t.Format("2006"))
		case 'y':
			out.WriteString(t.Format("06"))
		case 'B':
			out.WriteString(t.Format("January"))
		case 'b':
			out.WriteString(t.Format("Jan"))
		case 'A':
			out.WriteString(t.Format("Monday"))
		case 'a':
			out.WriteString(t.Format("Mon"))
		case 'Z':
			out.WriteString(t.Format("MST"))
		case '%':
			out.WriteByte('%')
		default:
			out.WriteByte('%')
			out.WriteByte(spec[i])
		}
	}
	return out.String()
}
