package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"argo/internal/i18n"
	"argo/internal/text"
)

// The lookups behind /tr, /dict, /time, /weather and /cash. Each asks a free public
// service (no API key) and returns the finished HTML text of the answer. Problems are WebErrors whose message is
// a sentence meant for people. The network goes through GetJSON only, which is what the tests replace.

// WebError: a lookup failed; Msg can be shown to people (already safe for HTML).
type WebError struct {
	Msg      string
	NotFound bool // the service answered, but does not know what was asked for
}

func (e *WebError) Error() string { return e.Msg }

const webTimeout = 10 * time.Second

// GetJSON fetches and decodes a JSON document. 404 is NotFound, any other failure is "unavailable".
var GetJSON = func(ctx context.Context, l i18n.Lang, rawURL string, params map[string]string) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, webTimeout)
	defer cancel()
	unavailable := &WebError{Msg: l.T("web.unavailable")}
	if len(params) > 0 {
		q := url.Values{}
		for k, v := range params {
			q.Set(k, v)
		}
		rawURL += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, unavailable
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("GET failed", "url", rawURL, "err", err)
		return nil, unavailable
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, &WebError{Msg: "Nothing found.", NotFound: true}
	case resp.StatusCode >= 400:
		slog.Warn("GET answered an error", "url", rawURL, "status", resp.StatusCode)
		return nil, unavailable
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, unavailable
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, unavailable
	}
	return v, nil
}

const MaxTranslate = 1500

// LookupLanguages: code -> name. Codes are what people type after /tr.
var LookupLanguages = map[string]string{
	"af": "Afrikaans", "sq": "Albanian", "ar": "Arabic", "hy": "Armenian", "az": "Azerbaijani",
	"eu": "Basque", "be": "Belarusian", "bn": "Bengali", "bs": "Bosnian", "bg": "Bulgarian",
	"ca": "Catalan", "zh": "Chinese", "zh-cn": "Chinese (Simplified)", "zh-tw": "Chinese (Traditional)",
	"hr": "Croatian", "cs": "Czech", "da": "Danish", "nl": "Dutch", "en": "English",
	"eo": "Esperanto", "et": "Estonian", "fi": "Finnish", "fr": "French", "gl": "Galician",
	"ka": "Georgian", "de": "German", "el": "Greek", "gu": "Gujarati", "ht": "Haitian Creole",
	"he": "Hebrew", "hi": "Hindi", "hu": "Hungarian", "is": "Icelandic", "id": "Indonesian",
	"ga": "Irish", "it": "Italian", "ja": "Japanese", "kn": "Kannada", "kk": "Kazakh",
	"ko": "Korean", "la": "Latin", "lv": "Latvian", "lt": "Lithuanian", "mk": "Macedonian",
	"ms": "Malay", "mt": "Maltese", "mr": "Marathi", "mn": "Mongolian", "ne": "Nepali",
	"no": "Norwegian", "fa": "Persian", "pl": "Polish", "pt": "Portuguese", "pa": "Punjabi",
	"ro": "Romanian", "ru": "Russian", "sr": "Serbian", "sk": "Slovak", "sl": "Slovenian",
	"es": "Spanish", "sw": "Swahili", "sv": "Swedish", "tl": "Filipino", "ta": "Tamil",
	"te": "Telugu", "th": "Thai", "tr": "Turkish", "uk": "Ukrainian", "ur": "Urdu",
	"uz": "Uzbek", "vi": "Vietnamese", "cy": "Welsh", "yi": "Yiddish", "zu": "Zulu",
}

var googleCodes = map[string]string{"zh-cn": "zh-CN", "zh-tw": "zh-TW", "he": "iw"}

func languageName(code string) string {
	code = strings.ToLower(code)
	if n, ok := LookupLanguages[code]; ok {
		return n
	}
	if n, ok := LookupLanguages[strings.SplitN(code, "-", 2)[0]]; ok {
		return n
	}
	return code
}

func IsLookupLanguage(word string) bool { _, ok := LookupLanguages[strings.ToLower(word)]; return ok }

// --- JSON navigation ----------------------------------------------------------

func asMap(v any) map[string]any     { m, _ := v.(map[string]any); return m }
func asList(v any) []any             { l, _ := v.([]any); return l }
func asString(v any) string          { s, _ := v.(string); return s }
func asNumber(v any) (float64, bool) { f, ok := v.(float64); return f, ok }

func numberOf(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	}
	return 0, false
}

func (a *App) unavailable(l i18n.Lang) error { return &WebError{Msg: l.T("web.unavailable")} }

// --- /tr ----------------------------------------------------------------------

func (a *App) Translate(ctx context.Context, l i18n.Lang, s, target string) (string, error) {
	s = strings.TrimSpace(s)
	tl := target
	if g, ok := googleCodes[strings.ToLower(target)]; ok {
		tl = g
	}
	q := s
	if r := []rune(q); len(r) > MaxTranslate {
		q = string(r[:MaxTranslate])
	}
	data, err := GetJSON(ctx, l, "https://translate.googleapis.com/translate_a/single", map[string]string{"client": "gtx", "sl": "auto", "tl": tl, "dt": "t", "q": q})
	if err != nil {
		return "", err
	}
	list := asList(data)
	if len(list) == 0 || asList(list[0]) == nil {
		return "", a.unavailable(l)
	}
	var translated strings.Builder
	for _, part := range asList(list[0]) {
		if p := asList(part); len(p) > 0 {
			translated.WriteString(asString(p[0]))
		}
	}
	source := "auto"
	if len(list) > 2 {
		if s := asString(list[2]); s != "" {
			source = s
		}
	}
	if translated.Len() == 0 {
		return "", &WebError{Msg: l.T("lookup.i_couldn_t_translate_that"), NotFound: true}
	}
	sourceName := l.T("common.unknown")
	if source != "auto" {
		if source == "iw" {
			source = "he"
		}
		sourceName = languageName(source)
	}
	return l.T("lookup.translation_from_to", i18n.V{"source_name": text.Esc(sourceName), "language_name": text.Esc(languageName(target)), "translated": text.Esc(translated.String())}), nil
}

// --- /dict --------------------------------------------------------------------

const (
	maxMeanings    = 3
	maxDefinitions = 2
)

func (a *App) Dictionary(ctx context.Context, l i18n.Lang, word string) (string, error) {
	word = strings.TrimSpace(word)
	data, err := GetJSON(ctx, l, "https://api.dictionaryapi.dev/api/v2/entries/en/"+url.PathEscape(word), nil)
	if err != nil {
		if we, ok := err.(*WebError); ok && we.NotFound {
			return "", &WebError{Msg: l.T("lookup.i_couldn_t_find_that_word"), NotFound: true}
		}
		return "", err
	}
	list := asList(data)
	if len(list) == 0 || asMap(list[0]) == nil {
		return "", a.unavailable(l)
	}
	entry := asMap(list[0])
	shown := asString(entry["word"])
	if shown == "" {
		shown = word
	}
	lines := []string{l.T("lookup.dictionary"), l.T("lookup.word", i18n.V{"v": text.Esc(shown)})}
	phonetic := asString(entry["phonetic"])
	if phonetic == "" {
		for _, p := range asList(entry["phonetics"]) {
			if t := asString(asMap(p)["text"]); t != "" {
				phonetic = t
				break
			}
		}
	}
	if phonetic != "" {
		lines = append(lines, "• <b>Pronunciation:</b> <code>"+text.Esc(phonetic)+"</code>")
	}
	meanings := asList(entry["meanings"])
	for _, mv := range meanings[:min(len(meanings), maxMeanings)] {
		m := asMap(mv)
		part := asString(m["partOfSpeech"])
		if part == "" {
			part = "other"
		}
		lines = append(lines, "", "<b>"+text.Esc(part)+"</b>")
		defs := asList(m["definitions"])
		for i, dv := range defs[:min(len(defs), maxDefinitions)] {
			d := asMap(dv)
			def, ok := d["definition"].(string)
			if !ok {
				return "", a.unavailable(l)
			}
			lines = append(lines, "• "+text.Esc(def))
			if ex := asString(d["example"]); i == 0 && ex != "" {
				lines = append(lines, "<i>"+text.Esc(ex)+"</i>")
			}
		}
	}
	return strings.Join(lines, "\n"), nil
}

// --- places: /time and /weather -----------------------------------------------

type place struct {
	name, admin1, country, timezone string
	lat, lon                        float64
	raw                             map[string]any
}

func (a *App) findPlace(ctx context.Context, l i18n.Lang, query string) (*place, error) {
	name, rest, _ := strings.Cut(query, ",")
	data, err := GetJSON(ctx, l, "https://geocoding-api.open-meteo.com/v1/search", map[string]string{"name": strings.TrimSpace(name), "count": "10", "language": "en", "format": "json"})
	if err != nil {
		return nil, err
	}
	found := asList(asMap(data)["results"])
	if len(found) == 0 {
		return nil, &WebError{Msg: l.T("lookup.i_couldn_t_find_that_place"), NotFound: true}
	}
	build := func(m map[string]any) *place {
		lat, _ := asNumber(m["latitude"])
		lon, _ := asNumber(m["longitude"])
		return &place{asString(m["name"]), asString(m["admin1"]), asString(m["country"]), asString(m["timezone"]), lat, lon, m}
	}
	rest = strings.ToLower(strings.TrimSpace(rest))
	if rest != "" {
		for _, f := range found {
			m := asMap(f)
			joined := strings.ToLower(strings.Join([]string{fmt.Sprint(orEmpty(m["country"])), fmt.Sprint(orEmpty(m["admin1"])), fmt.Sprint(orEmpty(m["country_code"]))}, " "))
			if strings.Contains(joined, rest) {
				return build(m), nil
			}
		}
	}
	return build(asMap(found[0])), nil
}

func orEmpty(v any) any {
	if v == nil {
		return ""
	}
	return v
}

func (p *place) title() string {
	var parts []string
	if p.name != "" {
		parts = append(parts, p.name)
	}
	if p.admin1 != "" && p.admin1 != p.name {
		parts = append(parts, p.admin1)
	}
	if p.country != "" {
		parts = append(parts, p.country)
	}
	return strings.Join(parts, ", ")
}

var zoneByLower map[string]string

func init() {
	zoneByLower = map[string]string{}
	for _, n := range zoneNames {
		zoneByLower[strings.ToLower(n)] = n
	}
}

func offsetText(t time.Time) string {
	_, secs := t.Zone()
	minutes := secs / 60
	sign := "+"
	if minutes < 0 {
		sign, minutes = "-", -minutes
	}
	return fmt.Sprintf("UTC%s%02d:%02d", sign, minutes/60, minutes%60)
}

func (a *App) TimeIn(ctx context.Context, l i18n.Lang, query string) (string, error) {
	var title, zone string
	if z, ok := zoneByLower[strings.ToLower(strings.ReplaceAll(strings.TrimSpace(query), " ", "_"))]; ok {
		title, zone = z, z
	} else {
		p, err := a.findPlace(ctx, l, query)
		if err != nil {
			return "", err
		}
		title, zone = p.title(), p.timezone
		if zone == "" {
			return "", &WebError{Msg: l.T("lookup.i_couldn_t_find_the_timezone"), NotFound: true}
		}
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return "", &WebError{Msg: l.T("lookup.i_couldn_t_find_the_timezone"), NotFound: true}
	}
	now := time.Now().In(loc)
	return l.T("lookup.time_in_time_date_timezone", i18n.V{"title": text.Esc(title), "now": now, "zone_name": text.Esc(zone), "offset_text": offsetText(now)}), nil
}

var compass = []string{"N", "NE", "E", "SE", "S", "SW", "W", "NW"}

var weatherCodes = []int{0, 1, 2, 3, 45, 48, 51, 53, 55, 56, 57, 61, 63, 65, 66, 67, 71, 73, 75, 77, 80, 81, 82, 85, 86, 95, 96, 99}

func (a *App) Weather(ctx context.Context, l i18n.Lang, query string) (string, error) {
	p, err := a.findPlace(ctx, l, query)
	if err != nil {
		return "", err
	}
	data, err := GetJSON(ctx, l, "https://api.open-meteo.com/v1/forecast", map[string]string{
		"latitude": strconv.FormatFloat(p.lat, 'f', -1, 64), "longitude": strconv.FormatFloat(p.lon, 'f', -1, 64),
		"current":         "temperature_2m,apparent_temperature,relative_humidity_2m,precipitation,weather_code,wind_speed_10m,wind_direction_10m",
		"wind_speed_unit": "kmh", "timezone": "auto",
	})
	if err != nil {
		return "", err
	}
	now := asMap(asMap(data)["current"])
	get := func(k string) (float64, bool) { return numberOf(now[k]) }
	temp, ok1 := get("temperature_2m")
	feels, ok2 := get("apparent_temperature")
	wind, ok3 := get("wind_speed_10m")
	dir, ok4 := get("wind_direction_10m")
	hum, ok5 := get("relative_humidity_2m")
	rain, ok6 := get("precipitation")
	code, ok7 := get("weather_code")
	if !(ok1 && ok2 && ok3 && ok4 && ok5 && ok6 && ok7) {
		return "", a.unavailable(l)
	}
	condition := "Unknown"
	for _, c := range weatherCodes {
		if c == int(code) {
			condition = l.T("lookup.weather_codes." + strconv.Itoa(c))
		}
	}
	f := func(c float64) float64 { return c*9/5 + 32 }
	direction := compass[int(math.Floor((dir+22.5)/45))%8]
	return strings.Join([]string{
		l.T("lookup.weather_in", i18n.V{"place_title": text.Esc(p.title())}),
		l.T("lookup.conditions", i18n.V{"get": condition}),
		l.T("lookup.temperature_c_f", i18n.V{"temp": temp, "fahrenheit": f(temp)}),
		l.T("lookup.feels_like_c_f", i18n.V{"feels": feels, "fahrenheit": f(feels)}),
		l.T("lookup.humidity", i18n.V{"float": hum}),
		l.T("lookup.wind_km_h_mph_from", i18n.V{"wind": wind, "v": wind / 1.609344, "direction": direction}),
		l.T("lookup.precipitation_mm", i18n.V{"float": rain}),
	}, "\n"), nil
}

// --- /cash --------------------------------------------------------------------

const MaxAmount = 1e15

// ParseAmount reads "12,5", "1,000.50"; ok is false for anything that is not a positive finite amount.
func ParseAmount(s string) (float64, bool) {
	if strings.Count(s, ",") == 1 && !strings.Contains(s, ".") {
		s = strings.Replace(s, ",", ".", 1)
	} else {
		s = strings.ReplaceAll(s, ",", "")
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 || v > MaxAmount {
		return 0, false
	}
	return v, true
}

func money(v float64) string {
	if v < 1 {
		return strconv.FormatFloat(v, 'g', 6, 64)
	}
	s := strconv.FormatFloat(v, 'f', 2, 64)
	whole, frac, _ := strings.Cut(s, ".")
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String() + "." + frac
}

func (a *App) Currency(ctx context.Context, l i18n.Lang, amount float64, source, target string) (string, error) {
	source, target = strings.ToUpper(source), strings.ToUpper(target)
	data, err := GetJSON(ctx, l, "https://open.er-api.com/v6/latest/"+url.PathEscape(source), nil)
	if err != nil {
		return "", err
	}
	m := asMap(data)
	if m == nil || asString(m["result"]) != "success" {
		if m != nil && asString(m["error-type"]) == "unsupported-code" {
			return "", &WebError{Msg: l.T("lookup.i_don_t_know_the_currency", i18n.V{"source": text.Esc(source)}), NotFound: true}
		}
		return "", a.unavailable(l)
	}
	rate, ok := asNumber(asMap(m["rates"])[target])
	if !ok {
		return "", &WebError{Msg: l.T("lookup.i_don_t_know_the_currency_2", i18n.V{"target": text.Esc(target)}), NotFound: true}
	}
	updated := asString(m["time_last_update_utc"])
	if r := []rune(updated); len(r) > 16 {
		updated = string(r[:16])
	}
	lines := []string{
		l.T("lookup.currency"),
		l.T("lookup.amount", i18n.V{"money": money(amount), "source": source}),
		l.T("lookup.result", i18n.V{"money": money(amount * rate), "target": target}),
		l.T("lookup.rate_1", i18n.V{"source": source, "rate": rate, "target": target}),
	}
	if updated != "" {
		lines = append(lines, "• <b>Updated:</b> <code>"+text.Esc(updated)+"</code>")
	}
	return strings.Join(lines, "\n"), nil
}

// --- /ud ----------------------------------------------------------------------

const maxUDText = 600

func cleanUD(s string) string {
	s = strings.TrimSpace(strings.NewReplacer("[", "", "]", "", "\r", "").Replace(s))
	if r := []rune(s); len(r) > maxUDText {
		return strings.TrimRightFunc(string(r[:maxUDText-1]), unicode.IsSpace) + "…"
	}
	return s
}

func (a *App) Urban(ctx context.Context, l i18n.Lang, term string) (string, error) {
	data, err := GetJSON(ctx, l, "https://api.urbandictionary.com/v0/define", map[string]string{"term": strings.TrimSpace(term)})
	if err != nil {
		return "", err
	}
	entries := asList(asMap(data)["list"])
	if len(entries) == 0 {
		return "", &WebError{Msg: l.T("lookup.urban_dictionary_doesn_t_know_that"), NotFound: true}
	}
	votes := func(m map[string]any, k string) int { f, _ := numberOf(m[k]); return int(f) }
	var maps []map[string]any
	for _, e := range entries {
		if m := asMap(e); m != nil {
			maps = append(maps, m)
		}
	}
	if len(maps) == 0 {
		return "", a.unavailable(l)
	}
	sort.SliceStable(maps, func(i, j int) bool { return votes(maps[i], "thumbs_up") > votes(maps[j], "thumbs_up") })
	best := maps[0]
	def, ok := best["definition"].(string)
	if !ok {
		return "", a.unavailable(l)
	}
	word := asString(best["word"])
	if word == "" {
		word = term
	}
	lines := []string{
		l.T("lookup.urban_dictionary"),
		l.T("lookup.term", i18n.V{"v": text.Esc(word)}),
		l.T("lookup.votes_up_down", i18n.V{"v": votes(best, "thumbs_up"), "v2": votes(best, "thumbs_down")}),
		"", text.Esc(cleanUD(def)),
	}
	if ex := asString(best["example"]); ex != "" {
		lines = append(lines, "", "<i>"+text.Esc(cleanUD(ex))+"</i>")
	}
	return strings.Join(lines, "\n"), nil
}
