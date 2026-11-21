package core

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

// Locks: kinds of messages a chat does not allow. The commands and the action taken are in
// modules/locks.go.
//
// A lock is a name from Locks. While it is on, the bot deletes the messages of that kind sent by anyone who is not
// an admin or approved. Some locks can be eased with the allowlist: links, forwards, commands, stickers... that are
// named in it stay allowed.

const (
	MaxAllowed = 200
	MaxEntry   = 200
)

// LockKind is one lockable kind of message.
type LockKind struct {
	Name        string
	Description string
	Allowable   bool
}

// LockGroup is a group of kinds, as /locktypes shows them.
type LockGroup struct {
	Title string
	Kinds []LockKind
}

// LockGroups lists every lock, grouped for /locktypes.
var LockGroups = []LockGroup{
	{"Everything", []LockKind{
		{"all", "Every message of everyone who is not an admin or approved. Turns the chat into read-only.", false},
	}},
	{"Media", []LockKind{
		{"photo", "Photos.", false},
		{"video", "Videos.", false},
		{"videonote", "Round video messages.", false},
		{"gif", "GIFs.", false},
		{"audio", "Music and other audio files.", false},
		{"voice", "Voice messages.", false},
		{"document", "Files, including photos and videos sent uncompressed.", false},
		{"album", "Photos or files sent together as an album.", false},
		{"sticker", "All stickers. Allowlist a sticker pack to keep it.", true},
		{"stickeranimated", "Animated and video stickers.", true},
		{"stickerpremium", "Premium stickers, the ones with a full-screen effect.", true},
	}},
	{"Content", []LockKind{
		{"text", "Anything with text, captions included.", false},
		{"poll", "Polls and quizzes.", false},
		{"contact", "Shared contacts.", false},
		{"location", "Shared locations and venues.", false},
		{"game", "Games from bots.", false},
		{"emojigame", "Dice, darts, bowling and the other emoji games.", false},
		{"checklist", "Telegram checklists.", false},
		{"spoiler", "Text hidden behind a spoiler.", false},
		{"emojicustom", "Custom (animated) emoji.", false},
		{"button", "Messages with buttons under them, usually from inline bots.", false},
	}},
	{"Links and mentions", []LockKind{
		{"url", "Any link. Allowlist a site to keep it.", true},
		{"email", "Email addresses.", false},
		{"phone", "Phone numbers.", false},
		{"invitelink", "Links and @names that lead to other groups and channels: the classic advertising spam.", true},
		{"botlink", "Links and @names that lead to bots.", true},
		{"cashtag", "Stock and crypto tags in capitals, like $BTC.", true},
		{"command", "Messages that start with a /command.", true},
	}},
	{"Where it came from", []LockKind{
		{"forward", "Every forwarded message.", true},
		{"forwarduser", "Messages forwarded from a person.", true},
		{"forwardbot", "Messages forwarded from a bot.", true},
		{"forwardchannel", "Messages forwarded from a channel.", true},
		{"inline", "Messages sent through an inline bot, like @gif or @vid.", true},
		{"anonchannel", "People writing as a channel instead of as themselves. Posts from the chat's linked channel are not touched.", true},
		{"externalreply", "Replies to a message from another chat.", true},
		{"bot", "Anyone but an admin adding a bot to the chat. The bot is removed.", false},
	}},
	{"Writing", []LockKind{
		{"cjk", "Chinese, Japanese and Korean characters.", false},
		{"cyrillic", "Cyrillic characters: Russian, Ukrainian, Serbian...", false},
		{"rtl", "Right-to-left writing: Arabic, Persian, Hebrew...", false},
		{"zalgo", "Text with piles of stacked marks, Z̷a̶l̸g̵o̷ style, that spills over other messages.", false},
	}},
}

// LockNames is every lock name in the order of LockGroups.
var LockNames = func() []string {
	var out []string
	for _, g := range LockGroups {
		for _, k := range g.Kinds {
			out = append(out, k.Name)
		}
	}
	return out
}()

// IsLock tells whether the name is a lock.
func IsLock(name string) bool {
	for _, n := range LockNames {
		if n == name {
			return true
		}
	}
	return false
}

var (
	cjkRE      = regexp.MustCompile(`[\x{3040}-\x{30ff}\x{3130}-\x{318f}\x{3400}-\x{4dbf}\x{4e00}-\x{9fff}\x{ac00}-\x{d7af}\x{f900}-\x{faff}]`)
	cyrillicRE = regexp.MustCompile(`[\x{0400}-\x{052f}]`)
	rtlRE      = regexp.MustCompile(`[\x{0590}-\x{08ff}\x{fb1d}-\x{fdff}\x{fe70}-\x{feff}]`)
)

const stackedMarks = 4 // combining marks in a row that make text "zalgo"

var telegramHosts = map[string]bool{"t.me": true, "telegram.me": true, "telegram.dog": true}
var notChats = map[string]bool{
	"addstickers": true, "addemoji": true, "addlist": true, "addtheme": true, "share": true, "proxy": true, "socks": true,
	"setlanguage": true, "boost": true, "invoice": true, "giftcode": true, "m": true, "login": true, "contact": true,
	"iv": true, "bg": true, "confirmphone": true,
}

// LockConfig is the locks of a chat.
type LockConfig struct {
	Locked  map[string]bool
	Warn    bool
	Allow   map[string]bool
	Mode    string // what breaking a lock does besides deleting the message
	Seconds int    // the length of a tban or tmute
}

// Action is del, warn, ban, kick, mute, tban or tmute.
func (l *LockConfig) Action() string {
	if l.Mode != "del" {
		return l.Mode
	}
	if l.Warn {
		return "warn"
	}
	return "del"
}

// LockConfig reads the locks of a chat.
func (a *App) LockConfig(ctx context.Context, chatID int64) (*LockConfig, error) {
	if cfg, ok := a.locks.get(chatID); ok {
		return cfg, nil
	}
	cfg := &LockConfig{Locked: map[string]bool{}, Allow: map[string]bool{}, Mode: "del"}
	rows, err := a.DB.Pool.Query(ctx, "SELECT item FROM locks WHERE chat_id = $1", chatID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var item string
		if err := rows.Scan(&item); err != nil {
			rows.Close()
			return nil, err
		}
		cfg.Locked[item] = true
	}
	rows.Close()
	if _, err := a.DB.Found(ctx, "SELECT warn, mode, mode_seconds FROM lock_settings WHERE chat_id = $1", []any{chatID}, &cfg.Warn, &cfg.Mode, &cfg.Seconds); err != nil {
		return nil, err
	}
	if cfg.Mode == "" {
		cfg.Mode = "del"
	}
	rows, err = a.DB.Pool.Query(ctx, "SELECT entry FROM lock_allowlist WHERE chat_id = $1 ORDER BY entry", chatID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			rows.Close()
			return nil, err
		}
		cfg.Allow[e] = true
	}
	rows.Close()
	a.locks.put(chatID, cfg)
	return cfg, nil
}

// Lock turns locks on.
func (a *App) Lock(ctx context.Context, chatID int64, items []string) error {
	for _, item := range items {
		if _, err := a.DB.Exec(ctx, "INSERT INTO locks (chat_id, item) VALUES ($1, $2) ON CONFLICT DO NOTHING", chatID, item); err != nil {
			return err
		}
	}
	a.locks.drop(chatID)
	return nil
}

// Unlock turns locks off.
func (a *App) Unlock(ctx context.Context, chatID int64, items []string) error {
	for _, item := range items {
		if _, err := a.DB.Exec(ctx, "DELETE FROM locks WHERE chat_id = $1 AND item = $2", chatID, item); err != nil {
			return err
		}
	}
	a.locks.drop(chatID)
	return nil
}

// SetLockMode sets what breaking a lock does besides deleting: del, warn, ban, kick, mute, tban or tmute.
func (a *App) SetLockMode(ctx context.Context, chatID int64, mode string, seconds int) error {
	_, err := a.DB.Exec(ctx, `INSERT INTO lock_settings (chat_id, warn, mode, mode_seconds) VALUES ($1, $2, $3, $4)
ON CONFLICT (chat_id) DO UPDATE SET warn = EXCLUDED.warn, mode = EXCLUDED.mode, mode_seconds = EXCLUDED.mode_seconds`,
		chatID, mode == "warn", mode, seconds)
	a.locks.drop(chatID)
	return err
}

// AllowAdd adds entries to the allowlist and returns how many were new.
func (a *App) AllowAdd(ctx context.Context, chatID int64, entries []string) (int, error) {
	cfg, err := a.LockConfig(ctx, chatID)
	if err != nil {
		return 0, err
	}
	added := 0
	for _, e := range entries {
		if cfg.Allow[e] {
			continue
		}
		if _, err := a.DB.Exec(ctx, "INSERT INTO lock_allowlist (chat_id, entry) VALUES ($1, $2) ON CONFLICT DO NOTHING", chatID, e); err != nil {
			return added, err
		}
		added++
	}
	a.locks.drop(chatID)
	return added, nil
}

// AllowRemove removes entries from the allowlist and returns how many were there.
func (a *App) AllowRemove(ctx context.Context, chatID int64, entries []string) (int, error) {
	cfg, err := a.LockConfig(ctx, chatID)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		if !cfg.Allow[e] {
			continue
		}
		if _, err := a.DB.Exec(ctx, "DELETE FROM lock_allowlist WHERE chat_id = $1 AND entry = $2", chatID, e); err != nil {
			return removed, err
		}
		removed++
	}
	a.locks.drop(chatID)
	return removed, nil
}

// AllowClear empties the allowlist and returns how many entries it held.
func (a *App) AllowClear(ctx context.Context, chatID int64) (int, error) {
	n, err := a.DB.Exec(ctx, "DELETE FROM lock_allowlist WHERE chat_id = $1", chatID)
	a.locks.drop(chatID)
	return int(n), err
}

// SortedAllow is the allowlist in alphabetical order.
func (l *LockConfig) SortedAllow() []string {
	out := make([]string, 0, len(l.Allow))
	for e := range l.Allow {
		out = append(out, e)
	}
	sort.Strings(out)
	return out
}

// --- Allowlist entries -----------------------------------------------------------------------

type splitURL struct{ host, path string }

// splitLink is the host and path of a text, with "//" put in front when it has no scheme: the lower-case host and the path.
func splitLink(text string) splitURL {
	if !strings.Contains(text, "://") {
		text = "//" + text
	}
	u, err := url.Parse(text)
	if err != nil {
		return splitURL{}
	}
	return splitURL{strings.ToLower(u.Hostname()), u.Path}
}

// telegramTarget is ("chat", name), ("pack", name) or ("invite", "") for a Telegram link; ok is false otherwise.
func telegramTarget(link string) (kind, name string, ok bool) {
	parts := splitLink(link)
	if !telegramHosts[parts.host] {
		return "", "", false
	}
	var segments []string
	for _, s := range strings.Split(parts.path, "/") {
		if s != "" {
			segments = append(segments, s)
		}
	}
	if len(segments) == 0 {
		return "", "", false
	}
	first := strings.ToLower(segments[0])
	switch {
	case (first == "addstickers" || first == "addemoji") && len(segments) > 1:
		return "pack", strings.ToLower(segments[1]), true
	case strings.HasPrefix(first, "+") || first == "joinchat" || first == "c":
		return "invite", "", true
	case notChats[first]:
		return "", "", false
	}
	return "chat", first, true
}

// plainURL is host/path in lowercase, without scheme, www., query or trailing slash.
func plainURL(link string) string {
	parts := splitLink(strings.TrimSpace(link))
	host := strings.TrimPrefix(parts.host, "www.")
	return strings.ToLower(host + strings.TrimRight(parts.path, "/"))
}

// EntryError is a sentence about a bad allowlist entry.
type EntryError struct{ Msg string }

func (e *EntryError) Error() string { return e.Msg }

var (
	intRE      = regexp.MustCompile(`^-?[0-9]+$`)
	atNameRE   = regexp.MustCompile(`^[a-z0-9_]{3,32}$`)
	cashtagRE  = regexp.MustCompile(`^[A-Z]{1,8}$`)
	wordNameRE = func(s string) bool {
		n := utf8.RuneCountInString(s)
		if n < 1 || n > 32 {
			return false
		}
		for _, r := range s {
			if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_') {
				return false
			}
		}
		return true
	}
)

func cut(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}

// NormalizeEntry is the stored form of an allowlist entry typed by a person.
func NormalizeEntry(l interface {
	T(tag string, values ...V) string
}, token string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" || utf8.RuneCountInString(token) > MaxEntry {
		return "", &EntryError{l.T("locks.is_not_a_valid_entry", V{"v": cut(token, 30)})}
	}
	switch {
	case intRE.MatchString(token):
		n, err := strconv.ParseInt(token, 10, 64)
		if err == nil {
			return strconv.FormatInt(n, 10), nil
		}
		return token, nil
	case strings.HasPrefix(token, "@"):
		name := strings.ToLower(token[1:])
		if !atNameRE.MatchString(name) {
			return "", &EntryError{l.T("locks.is_not_a_valid_name", V{"token": token})}
		}
		return "@" + name, nil
	case strings.HasPrefix(token, "/"):
		name := strings.ToLower(strings.SplitN(token[1:], "@", 2)[0])
		if !wordNameRE(name) {
			return "", &EntryError{l.T("locks.is_not_a_valid_command", V{"token": token})}
		}
		return "/" + name, nil
	case strings.HasPrefix(token, "$"):
		tag := strings.ToUpper(token[1:])
		if !cashtagRE.MatchString(tag) {
			return "", &EntryError{l.T("locks.is_not_a_valid_cashtag", V{"token": token})}
		}
		return "$" + tag, nil
	}
	if kind, name, ok := telegramTarget(token); ok {
		switch kind {
		case "pack":
			return "stickerpack:" + name, nil
		case "chat":
			return "@" + name, nil
		}
	}
	plain := plainURL(token)
	if !strings.Contains(strings.SplitN(plain, "/", 2)[0], ".") {
		return "", &EntryError{l.T("locks.is_not_a_link_id_name", V{"v": cut(token, 30)})}
	}
	return plain, nil
}

func urlAllowed(allow map[string]bool, link string) bool {
	if len(allow) == 0 {
		return false
	}
	if kind, name, ok := telegramTarget(link); ok {
		switch kind {
		case "pack":
			return allow["stickerpack:"+name]
		case "chat":
			return allow["@"+name] || plainAllowed(allow, link)
		}
	}
	return plainAllowed(allow, link)
}

func plainAllowed(allow map[string]bool, link string) bool {
	plain := plainURL(link)
	host := strings.SplitN(plain, "/", 2)[0]
	for entry := range allow {
		if strings.ContainsRune("@/$-0123456789", rune(entry[0])) || strings.HasPrefix(entry, "stickerpack:") {
			continue
		}
		if plain == entry || strings.HasPrefix(plain, entry+"/") || host == entry || strings.HasSuffix(host, "."+entry) {
			return true
		}
	}
	return false
}

// --- Looking at a message --------------------------------------------------------------------

func utf16Part(text string, start, length int64) string {
	u := utf16.Encode([]rune(text))
	end := start + length
	if start < 0 || start > int64(len(u)) {
		return ""
	}
	if end > int64(len(u)) {
		end = int64(len(u))
	}
	return string(utf16.Decode(u[start:end]))
}

func messageEntities(m *gotgbot.Message) (string, []gotgbot.MessageEntity) {
	if m.Text != "" {
		return m.Text, m.Entities
	}
	return m.Caption, m.CaptionEntities
}

func addKeys(keys map[string]bool, user *gotgbot.User, chat *gotgbot.Chat) {
	if user != nil {
		keys[strconv.FormatInt(user.Id, 10)] = true
		if user.Username != "" {
			keys["@"+strings.ToLower(user.Username)] = true
		}
	}
	if chat != nil {
		keys[strconv.FormatInt(chat.Id, 10)] = true
		if chat.Username != "" {
			keys["@"+strings.ToLower(chat.Username)] = true
		}
	}
}

func anyKey(keys, allow map[string]bool) bool {
	for k := range keys {
		if allow[k] {
			return true
		}
	}
	return false
}

func stacked(text string) bool {
	run := 0
	for _, r := range text {
		if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
			run++
			if run >= stackedMarks {
				return true
			}
		} else {
			run = 0
		}
	}
	return false
}

var chatKinds struct {
	mu sync.Mutex
	m  map[string]bool
}

// leadsToChat tells whether the public @name belongs to a group or a channel. Remembered for a while.
func (c *Ctx) leadsToChat(name string) bool {
	key := "@" + name
	chatKinds.mu.Lock()
	known, ok := chatKinds.m[key]
	chatKinds.mu.Unlock()
	if ok {
		return known
	}
	found := false
	if raw, err := c.Bot.RequestWithContext(c.Ctx, "getChat", map[string]any{"chat_id": key}, nil); err == nil {
		var chat struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &chat) == nil {
			found = chat.Type == "group" || chat.Type == "supergroup" || chat.Type == "channel"
		}
	}
	chatKinds.mu.Lock()
	if chatKinds.m == nil || len(chatKinds.m) >= cacheLimit {
		chatKinds.m = map[string]bool{}
	}
	chatKinds.m[key] = found
	chatKinds.mu.Unlock()
	return found
}

func (c *Ctx) telegramLinks(links, mentions []string, allow map[string]bool, lookUp bool) map[string]bool {
	found := map[string]bool{}
	for _, link := range links {
		kind, name, ok := telegramTarget(link)
		if !ok || urlAllowed(allow, link) {
			continue
		}
		switch {
		case kind == "invite" || (kind == "chat" && !strings.HasSuffix(name, "bot")):
			found["invitelink"] = true
		case kind == "chat":
			found["botlink"] = true
		}
	}
	for _, mention := range mentions {
		name := strings.ToLower(strings.TrimLeft(mention, "@"))
		if allow["@"+name] {
			continue
		}
		if strings.HasSuffix(name, "bot") {
			found["botlink"] = true
		} else if lookUp && !found["invitelink"] && c.leadsToChat(name) {
			found["invitelink"] = true
		}
	}
	return found
}

// FindLock is the first locked kind the message belongs to, or "".
func (c *Ctx) FindLock(m *gotgbot.Message, cfg *LockConfig) string {
	locked, allow := cfg.Locked, cfg.Allow
	if len(locked) == 0 {
		return ""
	}
	if locked["all"] {
		return "all"
	}
	text, entities := messageEntities(m)
	kinds := map[string][]string{}
	for _, e := range entities {
		kinds[e.Type] = append(kinds[e.Type], utf16Part(text, e.Offset, e.Length))
	}
	links := append([]string{}, kinds["url"]...)
	for _, e := range entities {
		if e.Type == "text_link" && e.Url != "" {
			links = append(links, e.Url)
		}
	}
	has := func(name string) bool { return locked[name] }

	if has("text") && text != "" {
		return "text"
	}
	switch {
	case has("photo") && len(m.Photo) > 0:
		return "photo"
	case has("video") && m.Video != nil:
		return "video"
	case has("audio") && m.Audio != nil:
		return "audio"
	case has("voice") && m.Voice != nil:
		return "voice"
	case has("poll") && m.Poll != nil:
		return "poll"
	case has("contact") && m.Contact != nil:
		return "contact"
	case has("game") && m.Game != nil:
		return "game"
	case has("checklist") && m.Checklist != nil:
		return "checklist"
	case has("videonote") && m.VideoNote != nil:
		return "videonote"
	case has("gif") && m.Animation != nil:
		return "gif"
	case has("document") && m.Document != nil && m.Animation == nil:
		return "document"
	case has("album") && m.MediaGroupId != "":
		return "album"
	case has("location") && (m.Location != nil || m.Venue != nil):
		return "location"
	case has("emojigame") && m.Dice != nil:
		return "emojigame"
	}
	if s := m.Sticker; s != nil {
		pack := ""
		if s.SetName != "" {
			pack = "stickerpack:" + strings.ToLower(s.SetName)
		}
		if !(pack != "" && allow[pack]) {
			switch {
			case has("sticker"):
				return "sticker"
			case has("stickeranimated") && (s.IsAnimated || s.IsVideo):
				return "stickeranimated"
			case has("stickerpremium") && s.PremiumAnimation != nil:
				return "stickerpremium"
			}
		}
	}
	if has("spoiler") && len(kinds["spoiler"]) > 0 {
		return "spoiler"
	}
	if has("emojicustom") && len(kinds["custom_emoji"]) > 0 {
		return "emojicustom"
	}
	if has("email") && len(kinds["email"]) > 0 {
		return "email"
	}
	if has("phone") && len(kinds["phone_number"]) > 0 {
		return "phone"
	}
	if has("button") && m.ReplyMarkup != nil && len(m.ReplyMarkup.InlineKeyboard) > 0 {
		return "button"
	}
	if has("cashtag") {
		for _, tag := range kinds["cashtag"] {
			if !allow["$"+strings.ToUpper(strings.TrimLeft(tag, "$"))] {
				return "cashtag"
			}
		}
	}
	if has("command") {
		for _, e := range entities {
			if e.Type != "bot_command" || e.Offset != 0 {
				continue
			}
			value := utf16Part(text, e.Offset, e.Length)
			if !allow["/"+strings.ToLower(strings.SplitN(value[1:], "@", 2)[0])] {
				return "command"
			}
		}
	}
	if has("url") {
		for _, link := range links {
			if !urlAllowed(allow, link) {
				return "url"
			}
		}
	}
	if has("invitelink") || has("botlink") {
		found := c.telegramLinks(links, kinds["mention"], allow, has("invitelink"))
		for _, name := range []string{"invitelink", "botlink"} {
			if has(name) && found[name] {
				return name
			}
		}
	}

	if m.ForwardOrigin != nil && !m.IsAutomaticForward {
		o := m.ForwardOrigin.MergeMessageOrigin()
		keys := map[string]bool{}
		addKeys(keys, o.SenderUser, nil)
		addKeys(keys, nil, o.SenderChat)
		addKeys(keys, nil, o.Chat)
		if !anyKey(keys, allow) {
			switch {
			case has("forward"):
				return "forward"
			case has("forwardbot") && o.Type == "user" && o.SenderUser != nil && o.SenderUser.IsBot:
				return "forwardbot"
			case has("forwardchannel") && o.Type == "channel":
				return "forwardchannel"
			case has("forwarduser") && (o.Type == "hidden_user" || (o.Type == "user" && o.SenderUser != nil && !o.SenderUser.IsBot)):
				return "forwarduser"
			}
		}
	}
	if has("inline") && m.ViaBot != nil {
		keys := map[string]bool{}
		addKeys(keys, m.ViaBot, nil)
		if !anyKey(keys, allow) {
			return "inline"
		}
	}
	if has("anonchannel") && m.SenderChat != nil && m.SenderChat.Id != m.Chat.Id {
		keys := map[string]bool{}
		addKeys(keys, nil, m.SenderChat)
		if !anyKey(keys, allow) {
			return "anonchannel"
		}
	}
	if has("externalreply") && m.ExternalReply != nil {
		keys := map[string]bool{}
		addKeys(keys, nil, m.ExternalReply.Chat)
		if m.ExternalReply.Chat == nil || !anyKey(keys, allow) {
			return "externalreply"
		}
	}

	if text != "" {
		switch {
		case has("cjk") && cjkRE.MatchString(text):
			return "cjk"
		case has("cyrillic") && cyrillicRE.MatchString(text):
			return "cyrillic"
		case has("rtl") && rtlRE.MatchString(text):
			return "rtl"
		case has("zalgo") && stacked(text):
			return "zalgo"
		}
	}
	return ""
}
