package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"argo/internal/blacklist"
	"argo/internal/i18n"
	"argo/internal/markup"
)

// Backups of one chat: what to save, how it looks in the file, and how it is put back.
//
// A backup is a JSON file:
//
//	{"argo_backup": 1, "created_at": "...", "chat": {"id": ..., "title": ...},
//	 "items": {"notes": [...], "approves": [...], "settings": {...}}}
//
// An item is one thing a chat has: its notes, its filters, its approved users... A file may hold all of them or just
// one, and /import takes whatever is in it. Items are described by the tables below (the columns, their types and
// what is acceptable), so a file from outside is never trusted: every value is converted and checked, and a row that
// does not pass is skipped and counted. Nothing here talks to Telegram.

const (
	BackupFormat       = 1
	MaxBackupFileBytes = 20 * 1024 * 1024
	maxBackupText      = 8000
	maxBackupSeconds   = 366 * 86400
)

// BackupError: the file can't be used; the message says why.
type BackupError struct{ Msg string }

func (e *BackupError) Error() string { return e.Msg }

// Column types: i integer, s text, b yes/no, t time. A "?" after it means the value may be empty.
type backupColumn struct{ name, kind string }

type backupTable struct {
	name    string
	columns []backupColumn
	key     []string // the columns that make a row the same row (besides the chat)
	single  bool     // one row per chat, not a list
	choices map[string][]string
	ranges  map[string][2]int64
	check   func(row map[string]any) error // an error means: skip the row
	limit   int
}

// BackupItem is one thing a chat has.
type BackupItem struct {
	Key, Label string
	tables     []backupTable
	aliases    []string
}

var backupMedia = []string{"photo", "video", "animation", "document", "audio", "voice", "sticker", "video_note"}

func cols(spec ...string) []backupColumn {
	out := make([]backupColumn, len(spec)/2)
	for i := range out {
		out[i] = backupColumn{spec[2*i], spec[2*i+1]}
	}
	return out
}

var backupNameRE = regexp.MustCompile(`^[\p{L}\p{N}_.\-]{1,64}$`)

func backupContent(row map[string]any) error {
	s, _ := row["content"].(string)
	return markup.Check(i18n.English, s)
}

func backupNote(row map[string]any) error {
	name, _ := row["name"].(string)
	name = strings.ToLower(strings.TrimLeft(strings.TrimSpace(name), "#"))
	if !backupNameRE.MatchString(name) {
		return errors.New("not a note name")
	}
	row["name"] = name
	return backupContent(row)
}

func backupBlacklisted(row map[string]any) error {
	pattern, _ := row["pattern"].(string)
	action, _ := row["action"].(string)
	seconds, _ := row["action_seconds"].(int64)
	reason, _ := row["reason"].(string)
	_, err := blacklist.Build(i18n.English, pattern, action, int(seconds), reason)
	return err
}

func backupGreeting(row map[string]any) error {
	kinds, _ := row["clean_service_types"].(string)
	for _, word := range strings.Split(kinds, ",") {
		known := word == ""
		for _, kind := range CleanKinds {
			known = known || word == kind
		}
		if !known {
			return fmt.Errorf("clean_service_types has a value I don't know")
		}
	}
	if old, _ := row["clean_service"].(bool); old && kinds == "" { // a backup from before the kinds: the old switch meant join and leave
		row["clean_service_types"] = "join,leave"
	}
	if _, ok := row["clean_service"]; ok {
		row["clean_service"] = false
	}
	for _, kind := range []string{"welcome", "goodbye"} {
		if s, _ := row[kind+"_content"].(string); s != "" {
			if err := markup.Check(i18n.English, s); err != nil {
				return err
			}
		}
	}
	return nil
}

var blacklistActions = []string{"del", "warn", "ban", "kick", "mute", "tban", "tmute"}

func backupItems() []BackupItem {
	return []BackupItem{
		{Key: "settings", Label: "Chat settings and the warn limit", aliases: []string{"chat", "chatsettings"}, tables: []backupTable{
			{name: "chat_settings", columns: cols("staff_help", "b", "gban_enforce", "b", "admin_errors", "b", "anon_admin", "b", "clean_commands", "b", "reports", "b", "anti_channel_pin", "b", "clean_linked", "b"), single: true},
			{name: "warn_settings", columns: cols("warn_limit", "i"), single: true, ranges: map[string][2]int64{"warn_limit": {1, 10}}},
		}},
		{Key: "antiflood", Label: "Antiflood settings", aliases: []string{"flood"}, tables: []backupTable{
			{name: "flood_settings", columns: cols("flood_limit", "i", "timer_count", "i", "timer_seconds", "i", "mode", "s", "mode_seconds", "i", "clear_flood", "b", "antichannel", "b"),
				single: true, choices: map[string][]string{"mode": {"ban", "kick", "mute", "tban", "tmute"}},
				ranges: map[string][2]int64{"flood_limit": {0, 100}, "timer_count": {0, 100}, "timer_seconds": {0, 86400}, "mode_seconds": {0, maxBackupSeconds}}},
		}},
		{Key: "locks", Label: "Locks, the allowlist and warning on locks", aliases: []string{"lock"}, tables: []backupTable{
			{name: "locks", columns: cols("item", "s"), key: []string{"item"}, choices: map[string][]string{"item": LockNames}, limit: 1000},
			{name: "lock_settings", columns: cols("warn", "b"), single: true},
			{name: "lock_allowlist", columns: cols("entry", "s"), key: []string{"entry"}, limit: 500},
		}},
		{Key: "notes", Label: "Notes", tables: []backupTable{
			{name: "notes", columns: cols("name", "s", "content", "s", "media_type", "s?", "media_file_id", "s?", "created_by", "i?", "created_at", "t?"),
				key: []string{"name"}, choices: map[string][]string{"media_type": backupMedia}, check: backupNote, limit: 300},
		}},
		{Key: "filters", Label: "Filters", tables: []backupTable{
			{name: "filters", columns: cols("pattern", "s", "content", "s", "media_type", "s?", "media_file_id", "s?", "created_by", "i?"),
				key: []string{"pattern"}, choices: map[string][]string{"media_type": backupMedia}, check: backupContent, limit: 500},
		}},
		{Key: "blacklist", Label: "The blacklist and its settings", aliases: []string{"blacklists"}, tables: []backupTable{
			{name: "blacklist", columns: cols("pattern", "s", "action", "s?", "action_seconds", "i", "reason", "s?", "added_by", "i?"),
				key: []string{"pattern"}, choices: map[string][]string{"action": blacklistActions}, ranges: map[string][2]int64{"action_seconds": {0, maxBackupSeconds}},
				check: backupBlacklisted, limit: blacklist.MaxTriggers},
			{name: "blacklist_settings", columns: cols("mode", "s", "mode_seconds", "i", "delete_matches", "b"), single: true,
				choices: map[string][]string{"mode": append([]string{"off"}, blacklistActions...)}, ranges: map[string][2]int64{"mode_seconds": {0, maxBackupSeconds}}},
		}},
		{Key: "joinblacklist", Label: "The join blacklists", aliases: []string{"joinblacklists"}, tables: []backupTable{
			{name: "join_blacklist", columns: cols("kind", "s", "pattern", "s"), key: []string{"kind", "pattern"},
				choices: map[string][]string{"kind": JoinKinds}, limit: 1000},
		}},
		{Key: "greetings", Label: "Welcome and goodbye messages", aliases: []string{"welcome", "goodbye", "welcomes"}, tables: []backupTable{
			{name: "greetings", columns: cols("welcome_on", "b", "welcome_content", "s?", "welcome_media_type", "s?", "welcome_media_file_id", "s?",
				"goodbye_on", "b", "goodbye_content", "s?", "goodbye_media_type", "s?", "goodbye_media_file_id", "s?",
				"welcome_mute", "b", "clean_welcome", "b", "clean_service", "b", "clean_service_types", "s"),
				single: true, choices: map[string][]string{"welcome_media_type": backupMedia, "goodbye_media_type": backupMedia}, check: backupGreeting},
		}},
		{Key: "captcha", Label: "Captcha settings", aliases: []string{"captchas"}, tables: []backupTable{
			{name: "captcha_settings", columns: cols("enabled", "b", "mode", "s", "rules", "b", "mute_seconds", "i?", "kick", "b", "kick_seconds", "i", "button_text", "s?"),
				single: true, choices: map[string][]string{"mode": CaptchaModes}, ranges: map[string][2]int64{"mute_seconds": {0, maxBackupSeconds}, "kick_seconds": {60, maxBackupSeconds}}},
		}},
		{Key: "rules", Label: "The rules", tables: []backupTable{
			{name: "rules", columns: cols("content", "s", "media_type", "s?", "media_file_id", "s?"), single: true,
				choices: map[string][]string{"media_type": backupMedia}, check: backupContent},
		}},
		{Key: "warns", Label: "Warns", aliases: []string{"warn"}, tables: []backupTable{
			{name: "warns", columns: cols("user_id", "i", "user_name", "s?", "reason", "s?", "warned_by", "i?", "warned_by_name", "s?", "warned_by_staff", "b", "created_at", "t"),
				key: []string{"user_id", "created_at"}, limit: 50000},
		}},
		{Key: "approves", Label: "Approved users", aliases: []string{"approved", "approvals", "approve"}, tables: []backupTable{
			{name: "approvals", columns: cols("user_id", "i", "name", "s?", "approved_by", "i?", "approved_by_name", "s?", "approved_at", "t?"),
				key: []string{"user_id"}, limit: 50000},
		}},
	}
}

// BackupItems are the things a chat has, in the order /exportitems lists them.
var BackupItems = backupItems()

func backupByName(name string) *BackupItem {
	for i := range BackupItems {
		it := &BackupItems[i]
		if it.Key == name {
			return it
		}
		for _, a := range it.aliases {
			if a == name {
				return it
			}
		}
	}
	return nil
}

// ResolveBackupItems are the items the words name (all of them without words). An unknown word is a BackupError.
func ResolveBackupItems(words []string) ([]*BackupItem, error) {
	var out []*BackupItem
	if len(words) == 0 || (len(words) == 1 && words[0] == "all") {
		for i := range BackupItems {
			out = append(out, &BackupItems[i])
		}
		return out, nil
	}
	for _, w := range words {
		it := backupByName(strings.ToLower(w))
		if it == nil {
			keys := make([]string, len(BackupItems))
			for i, b := range BackupItems {
				keys[i] = b.Key
			}
			return nil, &BackupError{fmt.Sprintf("I don't know \"%s\". Items: %s.", w, strings.Join(keys, ", "))}
		}
		dup := false
		for _, o := range out {
			dup = dup || o == it
		}
		if !dup {
			out = append(out, it)
		}
	}
	return out, nil
}

// --- Export -------------------------------------------------------------------------------------

type orderedJSON struct {
	keys []string
	vals map[string]any
}

func newOrdered() *orderedJSON { return &orderedJSON{vals: map[string]any{}} }

func (o *orderedJSON) set(k string, v any) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

func (o *orderedJSON) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		vb, err := marshalNoEscape(o.vals[k])
		if err != nil {
			return nil, err
		}
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func marshalNoEscape(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

func isoTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.999999-07:00") }

func (a *App) exportTable(ctx context.Context, t backupTable, chatID int64) (any, int, error) {
	names := make([]string, len(t.columns))
	for i, c := range t.columns {
		names[i] = c.name
	}
	order := ""
	if len(t.key) > 0 {
		order = " ORDER BY " + strings.Join(t.key, ", ")
	}
	rows, err := a.DB.Pool.Query(ctx, fmt.Sprintf("SELECT %s FROM %s WHERE chat_id = $1%s", strings.Join(names, ", "), t.name, order), chatID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var data []any
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, 0, err
		}
		row := newOrdered()
		for i, c := range t.columns {
			v := vals[i]
			switch x := v.(type) {
			case time.Time:
				v = isoTime(x)
			case int16:
				v = int64(x)
			case int32:
				v = int64(x)
			}
			row.set(c.name, v)
		}
		data = append(data, row)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if len(data) == 0 {
		return nil, 0, nil
	}
	if t.single {
		return data[0], 1, nil
	}
	return data, len(data), nil
}

// ExportBackup is the file content and how many entries each item has. Items the chat has nothing for are left out.
func (a *App) ExportBackup(ctx context.Context, chatID int64, title string, items []*BackupItem) ([]byte, [][2]any, error) {
	body := newOrdered()
	var counts [][2]any
	for _, item := range items {
		parts := newOrdered()
		total := 0
		for _, t := range item.tables {
			data, n, err := a.exportTable(ctx, t, chatID)
			if err != nil {
				return nil, nil, err
			}
			if data == nil {
				continue
			}
			parts.set(t.name, data)
			total += n
		}
		if len(parts.keys) > 0 {
			// An item with a single table is the list itself; with several, one entry per table.
			if len(item.tables) == 1 {
				body.set(item.Key, parts.vals[parts.keys[0]])
			} else {
				body.set(item.Key, parts)
			}
			counts = append(counts, [2]any{item.Key, total})
		}
	}
	chat := newOrdered()
	chat.set("id", chatID)
	if title != "" {
		chat.set("title", title)
	} else {
		chat.set("title", nil)
	}
	doc := newOrdered()
	doc.set("argo_backup", BackupFormat)
	doc.set("created_at", time.Now().UTC().Format("2006-01-02T15:04:05-07:00"))
	doc.set("chat", chat)
	doc.set("items", body)
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(doc); err != nil {
		return nil, nil, err
	}
	return bytes.TrimRight(out.Bytes(), "\n"), counts, nil
}

// --- Import -------------------------------------------------------------------------------------

// ReadBackup are the items of a backup file. A file that is not one is a BackupError.
func ReadBackup(raw []byte) (map[string]any, error) {
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	dec := json.NewDecoder(strings.NewReader(strings.ToValidUTF8(string(raw), "�")))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil || dec.More() {
		return nil, &BackupError{"That is not a valid JSON file."}
	}
	m, _ := doc.(map[string]any)
	items, _ := m["items"].(map[string]any)
	if m == nil || m["argo_backup"] == nil || items == nil {
		return nil, &BackupError{"That is not a backup made by /export."}
	}
	if n, ok := m["argo_backup"].(json.Number); !ok || n.String() != "1" && n.String() != "1.0" {
		return nil, &BackupError{"That backup is from a version I don't know."}
	}
	return items, nil
}

var integerRE = regexp.MustCompile(`^-?\d+$`)

func coerceBackup(kind string, v any) (any, error) {
	nullable := strings.HasSuffix(kind, "?")
	kind = strings.TrimSuffix(kind, "?")
	if s, isString := v.(string); v == nil || (isString && s == "") {
		switch {
		case nullable:
			return nil, nil
		case kind == "s" && isString:
			return "", nil
		}
		return nil, errors.New("a value is missing")
	}
	switch kind {
	case "i":
		var text string
		switch x := v.(type) {
		case json.Number:
			text = x.String()
		case string:
			text = x
		default:
			return nil, errors.New("not a number")
		}
		if !integerRE.MatchString(text) {
			return nil, errors.New("not a number")
		}
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, errors.New("not a number")
		}
		return n, nil
	case "b":
		switch x := v.(type) {
		case bool:
			return x, nil
		case json.Number:
			if x.String() == "0" {
				return false, nil
			}
			if x.String() == "1" {
				return true, nil
			}
		}
		return nil, errors.New("not yes/no")
	case "t":
		s := fmt.Sprint(v)
		if str, ok := v.(string); ok {
			s = str
		}
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999", "2006-01-02"} {
			if t, err := time.Parse(layout, s); err == nil {
				return t.UTC(), nil // a time without a zone is UTC
			}
		}
		return nil, errors.New("not a time")
	}
	s, ok := v.(string)
	if !ok {
		return nil, errors.New("not text")
	}
	if utf8.RuneCountInString(s) > maxBackupText {
		return nil, errors.New("too long")
	}
	return s, nil
}

func (t backupTable) clean(raw any) (map[string]any, error) {
	in, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("not an entry")
	}
	isKey := func(c string) bool {
		for _, k := range t.key {
			if k == c {
				return true
			}
		}
		return false
	}
	row := map[string]any{}
	var order []string
	for _, c := range t.columns {
		value, present := in[c.name]
		if !present {
			if isKey(c.name) {
				return nil, fmt.Errorf("%s is missing", c.name)
			}
			continue // the database default is used
		}
		v, err := coerceBackup(c.kind, value)
		if err != nil {
			return nil, err
		}
		if v == nil && c.kind == "t?" {
			continue // the database fills the time in
		}
		if s, ok := v.(string); ok && v != nil {
			if choices, has := t.choices[c.name]; has && !inList(choices, s) {
				return nil, fmt.Errorf("%s has a value I don't know", c.name)
			}
		} else if v != nil {
			if _, has := t.choices[c.name]; has {
				return nil, fmt.Errorf("%s has a value I don't know", c.name)
			}
		}
		if n, ok := v.(int64); ok {
			if r, has := t.ranges[c.name]; has && (n < r[0] || n > r[1]) {
				return nil, fmt.Errorf("%s is out of range", c.name)
			}
		}
		row[c.name] = v
		order = append(order, c.name)
	}
	for _, k := range t.key {
		if v, ok := row[k]; !ok || v == nil || v == "" {
			return nil, fmt.Errorf("%s is missing", k)
		}
	}
	if id, ok := row["user_id"].(int64); ok && id <= 0 {
		return nil, errors.New("not a user")
	}
	if t.check != nil {
		if err := t.check(row); err != nil {
			return nil, err
		}
	}
	row["\x00order"] = order
	return row, nil
}

func (a *App) insertBackupRow(ctx context.Context, t backupTable, chatID int64, row map[string]any, overwrite bool) (bool, error) {
	order := row["\x00order"].([]string)
	names := append([]string{"chat_id"}, order...)
	marks := make([]string, len(names))
	args := []any{chatID}
	for i := range names {
		marks[i] = "$" + strconv.Itoa(i+1)
	}
	for _, c := range order {
		args = append(args, row[c])
	}
	set := func(skipKey bool) string {
		var u []string
		for _, c := range order {
			if skipKey && contains2(t.key, c) {
				continue
			}
			u = append(u, c+" = EXCLUDED."+c)
		}
		return strings.Join(u, ", ")
	}
	var sql string
	switch {
	case t.single:
		if overwrite && len(order) > 0 {
			sql = fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON CONFLICT (chat_id) DO UPDATE SET %s RETURNING chat_id", t.name, strings.Join(names, ", "), strings.Join(marks, ", "), set(false))
		} else {
			sql = fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON CONFLICT (chat_id) DO NOTHING RETURNING chat_id", t.name, strings.Join(names, ", "), strings.Join(marks, ", "))
		}
	case t.name == "warns": // no natural key: the same warn is the same user at the same time
		pos := func(c string) int {
			for i, n := range names {
				if n == c {
					return i + 1
				}
			}
			return 0
		}
		sql = fmt.Sprintf("INSERT INTO warns (%s) SELECT %s WHERE NOT EXISTS (SELECT 1 FROM warns WHERE chat_id = $1 AND user_id = $%d AND created_at = $%d) RETURNING id",
			strings.Join(names, ", "), strings.Join(marks, ", "), pos("user_id"), pos("created_at"))
	default:
		verb := "DO NOTHING"
		if overwrite {
			if u := set(true); u != "" {
				verb = "DO UPDATE SET " + u
			}
		}
		sql = fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON CONFLICT (%s) %s RETURNING chat_id", t.name, strings.Join(names, ", "), strings.Join(marks, ", "),
			strings.Join(append([]string{"chat_id"}, t.key...), ", "), verb)
	}
	var got any
	err := a.DB.Pool.QueryRow(ctx, sql, args...).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func contains2(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// BackupCounts: how many entries were added, kept (already there) and skipped (unusable).
type BackupCounts struct{ Added, Kept, Skipped int }

func (a *App) importTable(ctx context.Context, t backupTable, chatID int64, data any, overwrite bool, counts *BackupCounts) error {
	var entries []any
	if t.single {
		if _, ok := data.(map[string]any); !ok {
			counts.Skipped++
			return nil
		}
		entries = []any{data}
	} else {
		list, ok := data.([]any)
		if !ok {
			counts.Skipped++
			return nil
		}
		entries = list
	}
	limit := t.limit
	if limit == 0 {
		limit = 1000
	}
	if len(entries) > limit {
		counts.Skipped += len(entries) - limit
		entries = entries[:limit]
	}
	var rows []map[string]any
	for _, e := range entries {
		row, err := t.clean(e)
		if err != nil { // a bad row only costs itself
			counts.Skipped++
			continue
		}
		rows = append(rows, row)
	}
	if overwrite && !t.single {
		if _, err := a.DB.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE chat_id = $1", t.name), chatID); err != nil {
			return err
		}
	}
	for _, row := range rows {
		changed, err := a.insertBackupRow(ctx, t, chatID, row, overwrite)
		if err != nil {
			return err
		}
		if changed {
			counts.Added++
		} else {
			counts.Kept++
		}
	}
	return nil
}

// ImportBackup puts the wanted items of a backup into the chat. Without overwrite what the chat has is kept and only what
// is missing is added; with it the chat's own entries of that item are replaced. It returns, per item, how many entries
// were added, kept and skipped.
func (a *App) ImportBackup(ctx context.Context, chatID int64, content map[string]any, wanted []*BackupItem, overwrite bool) ([]struct {
	Key    string
	Counts BackupCounts
}, error) {
	var result []struct {
		Key    string
		Counts BackupCounts
	}
	defer a.forgetChat(chatID) // the caches of the modules
	for _, item := range wanted {
		data, ok := content[item.Key]
		if !ok || data == nil {
			continue
		}
		var counts BackupCounts
		for _, t := range item.tables {
			part := data
			if len(item.tables) > 1 {
				m, _ := data.(map[string]any)
				part = m[t.name]
			}
			if part == nil {
				continue
			}
			if err := a.importTable(ctx, t, chatID, part, overwrite, &counts); err != nil {
				return result, err
			}
		}
		result = append(result, struct {
			Key    string
			Counts BackupCounts
		}{item.Key, counts})
	}
	return result, nil
}
