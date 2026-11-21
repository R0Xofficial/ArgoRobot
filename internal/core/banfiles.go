package core

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Ban lists as files: the formats of /fedexport, /fedimport, /gexport and /gimport.
//
//	csv      user_id,name,reason,banned_by,banned_at
//	minicsv  user_id,reason
//	json     a list of {"user_id": ..., "name": ..., "reason": ...}; a file with one such object per line is read too
//	human    one line per user, like: Name [123456] - reason
//
// Nothing here talks to Telegram or the database.

var BanFileFormats = []string{"csv", "minicsv", "json", "human"}

const (
	MaxImportBytes = 20 * 1024 * 1024 // what a bot can download
	MaxImportRows  = 200_000
)

// BanFileRow is one line of a ban list.
type BanFileRow struct {
	UserID    int64
	Name      string
	Reason    string
	BannedBy  string
	CreatedAt time.Time
}

// ValidBanFormat tells whether the word names a format.
func ValidBanFormat(f string) bool {
	for _, k := range BanFileFormats {
		if k == f {
			return true
		}
	}
	return false
}

func stamp(r BanFileRow) string {
	if r.CreatedAt.IsZero() {
		return ""
	}
	return r.CreatedAt.UTC().Format("2006-01-02 15:04:05")
}

// RenderBanExport is (file content, extension) of a ban list.
func RenderBanExport(rows []BanFileRow, format string) ([]byte, string) {
	switch format {
	case "json":
		type entry struct {
			UserID   int64  `json:"user_id"`
			Name     string `json:"name"`
			Reason   string `json:"reason"`
			BannedBy string `json:"banned_by"`
			BannedAt string `json:"banned_at"`
		}
		list := make([]entry, 0, len(rows))
		for _, r := range rows {
			list = append(list, entry{r.UserID, r.Name, r.Reason, r.BannedBy, stamp(r)})
		}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", " ")
		_ = enc.Encode(list)
		return bytes.TrimRight(buf.Bytes(), "\n"), "json"
	case "human":
		lines := make([]string, 0, len(rows))
		for _, r := range rows {
			name := r.Name
			if name == "" {
				name = "Unknown"
			}
			line := fmt.Sprintf("%s [%d]", name, r.UserID)
			if r.Reason != "" {
				line += " - " + r.Reason
			}
			lines = append(lines, line)
		}
		return []byte(strings.Join(lines, "\n")), "txt"
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if format == "minicsv" {
		_ = w.Write([]string{"user_id", "reason"})
		for _, r := range rows {
			_ = w.Write([]string{strconv.FormatInt(r.UserID, 10), r.Reason})
		}
	} else {
		_ = w.Write([]string{"user_id", "name", "reason", "banned_by", "banned_at"})
		for _, r := range rows {
			_ = w.Write([]string{strconv.FormatInt(r.UserID, 10), r.Name, r.Reason, r.BannedBy, stamp(r)})
		}
	}
	w.Flush()
	return buf.Bytes(), "csv"
}

var (
	idOnlyRE   = regexp.MustCompile(`^-?\d{1,15}$`)
	csvHeadRE  = regexp.MustCompile(`(?i)^(user_id|id)\b`)
	digitsLead = regexp.MustCompile(`^\d+\s*(,|$)`)
	bracketRE  = regexp.MustCompile(`\[(\d{1,15})\]`)
	bareIDRE   = regexp.MustCompile(`^\s*(\d{1,15})\b`)
	dashLead   = regexp.MustCompile(`^\s*[-:–—,]\s*`)
)

func idOf(s string) int64 {
	s = strings.TrimSpace(s)
	if !idOnlyRE.MatchString(s) {
		return 0
	}
	n, _ := strconv.ParseInt(s, 10, 64)
	if n <= 0 {
		return 0
	}
	return n
}

func jsonLines(text string) ([]any, error) {
	var found []any
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(strings.TrimSpace(line), ",")
		if line == "" || line == "[" || line == "]" {
			continue
		}
		var v any
		if json.Unmarshal([]byte(line), &v) == nil {
			found = append(found, v)
		}
	}
	if len(found) == 0 {
		return nil, errors.New("That is not valid JSON.")
	}
	return found, nil
}

func asText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatInt(int64(x), 10)
	}
	return fmt.Sprint(v)
}

// ParseBanImport reads (user id, name, reason) rows from a file's text. The error is a sentence for the user.
func ParseBanImport(text, format string) ([]GbanRow, error) {
	text = strings.TrimSpace(strings.TrimPrefix(text, "\xef\xbb\xbf"))
	if text == "" {
		return nil, errors.New("The file is empty.")
	}
	if format == "" {
		first := strings.SplitN(text, "\n", 2)[0]
		switch {
		case text[0] == '[' || text[0] == '{':
			format = "json"
		case csvHeadRE.MatchString(text) || digitsLead.MatchString(first):
			format = "csv"
		default:
			format = "human"
		}
	}
	var rows []GbanRow
	switch format {
	case "json":
		var data any
		if err := json.Unmarshal([]byte(text), &data); err != nil {
			lines, err := jsonLines(text)
			if err != nil {
				return nil, err
			}
			data = lines
		}
		if obj, ok := data.(map[string]any); ok {
			switch {
			case obj["bans"] != nil:
				data = obj["bans"]
			case obj["users"] != nil:
				data = obj["users"]
			default:
				data = []any{obj}
			}
		}
		list, ok := data.([]any)
		if !ok {
			return nil, errors.New("I expected a list of bans.")
		}
		for _, e := range list {
			var id int64
			var name, reason string
			if obj, ok := e.(map[string]any); ok {
				raw := obj["user_id"]
				if raw == nil {
					raw = obj["id"]
				}
				id, name, reason = idOf(asText(raw)), asText(obj["name"]), asText(obj["reason"])
			} else {
				id = idOf(asText(e))
			}
			if id != 0 {
				rows = append(rows, GbanRow{id, reason, name})
			}
		}
	case "csv", "minicsv":
		r := csv.NewReader(strings.NewReader(text))
		r.FieldsPerRecord = -1
		r.LazyQuotes = true
		table, _ := r.ReadAll()
		columns := map[string]int{"user_id": 0, "name": 1, "reason": 2}
		if format == "minicsv" {
			columns = map[string]int{"user_id": 0, "reason": 1}
		}
		if len(table) > 0 {
			first := ""
			if len(table[0]) > 0 {
				first = table[0][0]
			}
			if idOf(first) == 0 {
				header := table[0]
				table = table[1:]
				found := map[string]int{}
				for i, h := range header {
					h = strings.ToLower(strings.TrimSpace(h))
					if _, dup := found[h]; !dup {
						found[h] = i
					}
				}
				picked := map[string]int{}
				for _, key := range []string{"user_id", "name", "reason"} {
					if i, ok := found[key]; ok {
						picked[key] = i
					}
				}
				if len(picked) > 0 {
					columns = picked
				}
				if _, ok := columns["user_id"]; !ok {
					if i, ok := found["id"]; ok {
						columns["user_id"] = i
					}
				}
			}
		}
		cell := func(row []string, key string) string {
			i, ok := columns[key]
			if !ok || i >= len(row) {
				return ""
			}
			return strings.TrimSpace(row[i])
		}
		for _, row := range table {
			if id := idOf(cell(row, "user_id")); id != 0 {
				rows = append(rows, GbanRow{id, cell(row, "reason"), cell(row, "name")})
			}
		}
	default:
		for _, line := range strings.Split(text, "\n") {
			loc := bracketRE.FindStringSubmatchIndex(line)
			if loc == nil {
				loc = bareIDRE.FindStringSubmatchIndex(line)
			}
			if loc == nil {
				continue
			}
			id, _ := strconv.ParseInt(line[loc[2]:loc[3]], 10, 64)
			before, after := line[:loc[0]], line[loc[1]:]
			rows = append(rows, GbanRow{id, strings.TrimSpace(dashLead.ReplaceAllString(after, "")), strings.TrimSpace(before)})
		}
	}
	if len(rows) == 0 {
		return nil, errors.New("I found no user IDs in that file.")
	}
	if len(rows) > MaxImportRows {
		return nil, fmt.Errorf("That is too many bans: at most %d at once.", MaxImportRows)
	}
	return rows, nil
}
