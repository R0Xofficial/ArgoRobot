// Package markup turns the formatted messages users write into Telegram messages (core/markup.py).
//
// Used by modules that save a message and send it later (notes, rules, filters, welcomes). The text is kept as the
// person wrote it and turned into a message only when it is sent. Nothing here talks to Telegram.
//
// Formatting: Telegram's own HTML (<b> <i> <u> <s> <tg-spoiler> <code> <pre> <blockquote> <a href>). Anything else
// that looks like a tag, and every stray < > &, is shown as the text it is.
//
// Fillings (replaced by facts about whoever triggered the message):
// {first} {last} {fullname} {username} {mention} {id} {chatname} {count}; a backslash in front makes it literal.
//
// Options (removed from the text, they change how it is sent): {preview} {protect} {nonotif} {mediaspoiler}.
//
// Buttons (removed from the text, shown under the message): [Text](buttonurl://example.com) and
// [Text](buttonurl://example.com:same) for a button on the row of the previous one.
//
// Random content: variants separated by %%%, one of them is picked each time.
package markup

import (
	"fmt"
	"html"
	"math/rand/v2"
	"regexp"
	"sort"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"argo/internal/i18n"
	"argo/internal/text"
)

const (
	MaxButtons       = 20
	MaxButtonsPerRow = 8
	MaxVariants      = 20
	MaxButtonText    = 64
)

var (
	Fillings = []string{"first", "last", "fullname", "username", "mention", "id", "chatname", "count"}
	Options  = []string{"preview", "protect", "nonotif", "mediaspoiler"}
)

// Error is a message for people: the text can't be turned into a message, and why.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// --- Safe HTML ---

var (
	hostRE       = regexp.MustCompile(`^(?:[\p{L}\p{N}_.~-]+@)?[\p{L}\p{N}_.-]+\.[\p{L}\p{N}_-]{2,}(?::\d{1,5})?$`)
	tagRE        = regexp.MustCompile(`<(/?)([A-Za-z][\p{L}\p{N}_-]*)((?:\s[^<>]*)?)>`)
	ampRE        = regexp.MustCompile(`^(?:amp|lt|gt|quot|#\d{1,7}|#[xX][0-9a-fA-F]{1,6});`)
	hrefRE       = regexp.MustCompile(`(?i)href\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	classRE      = regexp.MustCompile(`(?i)class\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	schemeRE     = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.-]*):`)
	langRE       = regexp.MustCompile(`^language-[\p{L}\p{N}_+#.-]{1,30}$`)
	preLangRE    = regexp.MustCompile(`^[\p{L}\p{N}_+#.-]{1,30}$`)
	expandableRE = regexp.MustCompile(`(?i)\bexpandable\b`)
)

var tagNames = map[string]string{
	"b": "b", "strong": "b", "i": "i", "em": "i", "u": "u", "ins": "u", "s": "s", "strike": "s", "del": "s",
	"tg-spoiler": "tg-spoiler", "code": "code", "pre": "pre", "a": "a", "blockquote": "blockquote",
}

// parsedURL is what Python's urlparse gives for the parts that matter here.
type parsedURL struct{ scheme, netloc, path string }

func urlparse(u string) parsedURL {
	u = strings.NewReplacer("\t", "", "\r", "", "\n", "").Replace(u)
	var p parsedURL
	if m := schemeRE.FindStringSubmatch(u); m != nil {
		p.scheme = strings.ToLower(m[1])
		u = u[len(m[0]):]
	}
	if strings.HasPrefix(u, "//") {
		u = u[2:]
		end := strings.IndexAny(u, "/?#")
		if end < 0 {
			end = len(u)
		}
		p.netloc, u = u[:end], u[end:]
	}
	end := strings.IndexAny(u, "?#")
	if end < 0 {
		end = len(u)
	}
	p.path = u[:end]
	return p
}

func webURLOK(p parsedURL) bool {
	return (p.scheme == "http" || p.scheme == "https") && hostRE.MatchString(p.netloc)
}

func safeURL(u string) (string, bool) {
	u = strings.TrimSpace(html.UnescapeString(u))
	p := urlparse(u)
	if webURLOK(p) || (p.scheme == "tg" && (p.netloc != "" || p.path != "")) {
		return u, true
	}
	return "", false
}

// escapeText escapes a piece of text for HTML: a bare & becomes &amp; (an entity stays), < and > are escaped.
func escapeText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '&':
			if ampRE.MatchString(s[i+1:]) {
				b.WriteByte('&')
			} else {
				b.WriteString("&amp;")
			}
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func escAttr(s string) string { return strings.ReplaceAll(text.Esc(s), `"`, "&quot;") }

func attr(re *regexp.Regexp, attrs string) (string, bool) {
	m := re.FindStringSubmatchIndex(attrs)
	if m == nil {
		return "", false
	}
	if m[2] >= 0 {
		return attrs[m[2]:m[3]], true
	}
	return attrs[m[4]:m[5]], true
}

type open struct{ tag, closing string }

// ToHTML is Telegram HTML for a text written in the HTML above: the tags Telegram knows are kept (and closed in the
// right order), everything else is shown as the text it is.
func ToHTML(source string) string {
	var out strings.Builder
	var stack []open
	pos := 0
	for _, m := range tagRE.FindAllStringSubmatchIndex(source, -1) {
		out.WriteString(escapeText(source[pos:m[0]]))
		pos = m[1]
		closing := source[m[2]:m[3]] == "/"
		name := strings.ToLower(source[m[4]:m[5]])
		attrs := source[m[6]:m[7]]
		tag, known := tagNames[name]
		if name == "span" {
			cls, _ := attr(classRE, attrs)
			hasSpoiler := false
			for _, e := range stack {
				if e.tag == "tg-spoiler" {
					hasSpoiler = true
				}
			}
			if (closing && hasSpoiler) || cls == "tg-spoiler" {
				tag, known = "tg-spoiler", true
			}
		}
		if !known {
			out.WriteString(escapeText(source[m[0]:m[1]]))
			continue
		}
		if closing {
			depth := -1
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i].tag == tag {
					depth = i
					break
				}
			}
			if depth < 0 {
				continue
			}
			for i := len(stack) - 1; i >= depth; i-- {
				out.WriteString(stack[i].closing)
			}
			stack = stack[:depth]
			continue
		}
		switch tag {
		case "a":
			href, _ := attr(hrefRE, attrs)
			u, ok := safeURL(href)
			if !ok {
				stack = append(stack, open{"a", ""}) // the link is dropped, its text stays
				continue
			}
			out.WriteString(`<a href="` + escAttr(u) + `">`)
			stack = append(stack, open{"a", "</a>"})
		case "code":
			cls, _ := attr(classRE, attrs)
			if langRE.MatchString(cls) {
				out.WriteString(`<code class="` + cls + `">`)
			} else {
				out.WriteString("<code>")
			}
			stack = append(stack, open{"code", "</code>"})
		case "blockquote":
			if expandableRE.MatchString(attrs) {
				out.WriteString("<blockquote expandable>")
			} else {
				out.WriteString("<blockquote>")
			}
			stack = append(stack, open{"blockquote", "</blockquote>"})
		default:
			out.WriteString("<" + tag + ">")
			stack = append(stack, open{tag, "</" + tag + ">"})
		}
	}
	out.WriteString(escapeText(source[pos:]))
	for i := len(stack) - 1; i >= 0; i-- {
		out.WriteString(stack[i].closing)
	}
	return out.String()
}

// --- Message entities to HTML ---

var entityTags = map[string]string{
	"bold": "b", "italic": "i", "underline": "u", "strikethrough": "s", "spoiler": "tg-spoiler",
	"code": "code", "pre": "pre", "blockquote": "blockquote", "expandable_blockquote": "blockquote",
}

// Entity is the part of a Telegram message entity that matters here.
type Entity struct {
	Type     string
	Offset   int // UTF-16 units
	Length   int
	URL      string
	Language string
}

type span struct {
	start, end  int
	open, close string
}

// FromEntities is the HTML for a message Telegram already formatted.
//
// literal writes the plain text as it is, so that what it shows stays what it shows (a replied-to message). Without it
// the text is left as typed, so tags written by hand still work next to the formatting made with the app's menu (a
// command's arguments). skip is the number of UTF-16 units to leave out at the start (the command itself).
func FromEntities(src string, entities []Entity, literal bool, skip int) string {
	if src == "" {
		return ""
	}
	runes := []rune(src)
	starts := map[int]int{}
	position := 0
	for index, r := range runes {
		starts[position] = index
		position += len(utf16.Encode([]rune{r}))
	}
	starts[position] = len(runes)

	var spans []*span
	for _, e := range entities {
		start, ok1 := starts[e.Offset-skip]
		end, ok2 := starts[e.Offset+e.Length-skip]
		if e.Offset < skip || !ok1 || !ok2 || end <= start {
			continue
		}
		switch {
		case e.Type == "text_link" && e.URL != "":
			spans = append(spans, &span{start, end, `<a href="` + escAttr(e.URL) + `">`, "</a>"})
		case e.Type == "pre":
			if e.Language != "" && preLangRE.MatchString(e.Language) {
				spans = append(spans, &span{start, end, `<pre><code class="language-` + e.Language + `">`, "</code></pre>"})
			} else {
				spans = append(spans, &span{start, end, "<pre>", "</pre>"})
			}
		default:
			if tag, ok := entityTags[e.Type]; ok {
				opening := "<" + tag + ">"
				if e.Type == "expandable_blockquote" {
					opening = "<blockquote expandable>"
				}
				spans = append(spans, &span{start, end, opening, "</" + tag + ">"})
			}
		}
	}

	var out strings.Builder
	var stack []*span
	for index := 0; index <= len(runes); index++ {
		var ending []*span
		for _, s := range stack {
			if s.end == index {
				ending = append(ending, s)
			}
		}
		if len(ending) > 0 {
			low := len(stack)
			for _, e := range ending {
				for i, s := range stack {
					if s == e && i < low {
						low = i
					}
				}
			}
			above := append([]*span(nil), stack[low:]...)
			for i := len(above) - 1; i >= 0; i-- {
				out.WriteString(above[i].close)
			}
			stack = stack[:low]
			for _, s := range above { // spans that go on are closed and opened again around it
				if s.end != index {
					out.WriteString(s.open)
					stack = append(stack, s)
				}
			}
		}
		var starting []*span
		for _, s := range spans {
			if s.start == index {
				starting = append(starting, s)
			}
		}
		sort.SliceStable(starting, func(i, j int) bool { return starting[i].end > starting[j].end })
		for _, s := range starting {
			out.WriteString(s.open)
			stack = append(stack, s)
		}
		if index < len(runes) {
			if literal {
				out.WriteString(text.Esc(string(runes[index])))
			} else {
				out.WriteRune(runes[index])
			}
		}
	}
	return out.String()
}

// --- Random content, fillings, buttons ---

// Variants are the texts one saved message can turn into (split on %%%).
func Variants(source string) []string {
	var found []string
	for _, part := range strings.Split(source, "%%%") {
		if p := strings.TrimSpace(part); p != "" {
			found = append(found, p)
		}
	}
	if len(found) == 0 {
		return []string{""}
	}
	return found
}

// Pick chooses one variant at random.
func Pick(source string) string {
	v := Variants(source)
	return v[rand.IntN(len(v))]
}

// Button is a link button: its text and URL.
type Button struct{ Text, URL string }

// Parsed is one variant turned into HTML, buttons and options.
type Parsed struct {
	HTML    string
	Buttons [][]Button
	Options map[string]bool
}

var (
	buttonRE    = regexp.MustCompile(`(?i)\[([^\]]+)\]\(buttonurl://([^)\s]+)\)`)
	unescapeRE  = regexp.MustCompile(`\\(\{(?:` + strings.Join(append(append([]string{}, Fillings...), Options...), "|") + `)\})`)
	anyTagRE    = regexp.MustCompile(`<[^>]*>`)
	schemeURLRE = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.-]*://`)
)

// replaceTokens replaces {name} for the given names, unless a backslash stands right before the brace.
// fn gets the name; the token is replaced by what fn returns. seen, if not nil, collects the names found.
func replaceTokens(s string, names []string, fn func(name string) string) string {
	var out strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '{' && (i == 0 || s[i-1] != '\\') {
			if end := strings.IndexByte(s[i:], '}'); end > 0 {
				name := s[i+1 : i+end]
				for _, n := range names {
					if n == name {
						out.WriteString(fn(name))
						i += end + 1
						goto next
					}
				}
			}
		}
		out.WriteByte(s[i])
		i++
	next:
	}
	return out.String()
}

func buttonURL(raw string) (string, error) {
	u := html.UnescapeString(raw)
	if !schemeURLRE.MatchString(u) {
		u = "https://" + u
	}
	p := urlparse(u)
	if !(webURLOK(p) || (p.scheme == "tg" && (p.netloc != "" || p.path != ""))) {
		return "", &Error{fmt.Sprintf(`The button link "%s" isn't valid. Use a link like https://example.com.`, html.UnescapeString(raw))}
	}
	return u, nil
}

// Parse turns one variant into HTML, buttons and options. values maps fillings to their (plain) text; with nil the
// fillings get stand-in text: that is how a message is checked when it is saved.
func Parse(l i18n.Lang, source string, values map[string]string) (*Parsed, error) {
	options := map[string]bool{}
	body := replaceTokens(source, Options, func(name string) string { options[name] = true; return "" })

	get := func(name string) string {
		if values == nil {
			return "x"
		}
		if v, ok := values[name]; ok {
			return v
		}
		return "x"
	}
	body = replaceTokens(body, Fillings, func(name string) string {
		value := get(name)
		userID := "1"
		if values != nil {
			if v, ok := values["id"]; ok {
				userID = v
			}
		}
		if name == "mention" || (name == "username" && value == "") {
			// Someone without a @username is mentioned by name instead.
			shown := value
			if shown == "" && values != nil {
				shown = values["fullname"]
			}
			if shown == "" {
				shown = "x"
			}
			return `<a href="tg://user?id=` + escAttr(userID) + `">` + text.Esc(shown) + `</a>`
		}
		return text.Esc(value)
	})

	var rows [][]Button
	count := 0
	var failure error
	body = buttonRE.ReplaceAllStringFunc(body, func(match string) string {
		if failure != nil {
			return ""
		}
		m := buttonRE.FindStringSubmatch(match)
		label := strings.TrimSpace(html.UnescapeString(anyTagRE.ReplaceAllString(m[1], "")))
		raw := m[2]
		same := strings.HasSuffix(strings.ToLower(raw), ":same")
		if same {
			raw = raw[:len(raw)-len(":same")]
		}
		if label == "" {
			failure = &Error{l.T("markup.a_button_needs_a_text")}
			return ""
		}
		if utf8.RuneCountInString(label) > MaxButtonText {
			failure = &Error{l.T("markup.a_button_text_can_be_at", i18n.V{"MAX_BUTTON_TEXT": MaxButtonText})}
			return ""
		}
		u, err := buttonURL(raw)
		if err != nil {
			failure = err
			return ""
		}
		count++
		if count > MaxButtons {
			failure = &Error{l.T("markup.at_most_buttons_per_message", i18n.V{"MAX_BUTTONS": MaxButtons})}
			return ""
		}
		if same && len(rows) > 0 && len(rows[len(rows)-1]) < MaxButtonsPerRow {
			rows[len(rows)-1] = append(rows[len(rows)-1], Button{label, u})
		} else {
			rows = append(rows, []Button{{label, u}})
		}
		return ""
	})
	if failure != nil {
		return nil, failure
	}
	if strings.Contains(strings.ToLower(body), "buttonurl://") {
		return nil, &Error{"A button is written like [Text](buttonurl://example.com)."}
	}
	body = unescapeRE.ReplaceAllString(body, "$1") // \{first} is the text {first}
	return &Parsed{HTML: ToHTML(strings.TrimSpace(body)), Buttons: rows, Options: options}, nil
}

// Check returns an *Error when a saved message could not be sent (every variant is tried).
func Check(l i18n.Lang, source string) error {
	parts := Variants(source)
	if len(parts) > MaxVariants {
		return &Error{l.T("markup.at_most_random_variants_separated_by", i18n.V{"MAX_VARIANTS": MaxVariants})}
	}
	for _, part := range parts {
		if _, err := Parse(l, part, nil); err != nil {
			return err
		}
	}
	return nil
}
