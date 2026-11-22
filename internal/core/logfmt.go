package core

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The console log has the Python bot's format: time | LEVEL | module | message, e.g.
//
//	2026-11-22 13:04:46 | INFO     | modules.gbans | command=/fban user=7410298411 (@ItsR0Xx) chat=-1002345803559 [supergroup]
//	2026-11-22 00:43:29 | ERROR    | updater | failed to get updates; sleeping 1s error=context deadline exceeded
//
// The module is the "module" attribute when a log call gives one (the router does for commands and buttons) and
// otherwise the source file of the call, so every line says which part of the bot wrote it. A value with a line
// break goes on indented lines below.

var (
	tokenURL = regexp.MustCompile(`(?:https?://)?api\.telegram\.org/(?:file/)?bot[^/\s"']+/(\w+)`)
	botToken = regexp.MustCompile(`\b\d{6,12}:[A-Za-z0-9_-]{30,}\b`)
)

// CleanLogText keeps the bot token out of the logs (Go's HTTP errors print the whole request URL, token included)
// and shortens a Telegram request URL to the name of the method.
func CleanLogText(s string) string {
	s = tokenURL.ReplaceAllString(s, "$1")
	return botToken.ReplaceAllString(s, "<token>")
}

// PrettyHandler writes the console log in the format above.
type PrettyHandler struct {
	out   io.Writer
	mu    *sync.Mutex
	level slog.Leveler
	attrs []slog.Attr
	group string
}

func NewPrettyHandler(out io.Writer, level slog.Leveler) *PrettyHandler {
	return &PrettyHandler{out: out, mu: &sync.Mutex{}, level: level}
}

func (h *PrettyHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level.Level() }

func (h *PrettyHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *h
	c.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &c
}

func (h *PrettyHandler) WithGroup(name string) slog.Handler {
	c := *h
	if name != "" {
		c.group += name + "."
	}
	return &c
}

func prettyLevel(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "ERROR"
	case l >= slog.LevelWarn:
		return "WARNING"
	case l >= slog.LevelInfo:
		return "INFO"
	}
	return "DEBUG"
}

// sourceModule names the part of the bot that wrote a record, from the file of the log call:
// internal/modules/gbans.go is modules.gbans, cmd/argo/main.go is main.
func sourceModule(pc uintptr) string {
	if pc == 0 {
		return "main"
	}
	f, _ := runtime.CallersFrames([]uintptr{pc}).Next()
	file := filepath.ToSlash(f.File)
	for _, root := range []string{"/internal/", "/cmd/"} {
		if i := strings.LastIndex(file, root); i >= 0 {
			rest := strings.TrimSuffix(file[i+len(root):], ".go")
			if root == "/cmd/" {
				return "main"
			}
			return strings.ReplaceAll(rest, "/", ".")
		}
	}
	return strings.TrimSuffix(path.Base(file), ".go")
}

func prettyValue(v string) string {
	if v == "" || strings.ContainsAny(v, " \t\"=") {
		return strconv.Quote(v)
	}
	return v
}

// logWhoAt writes a person the way the Python log does: " user=id (@name)".
func logWhoAt(attrs map[string]string) string {
	id := attrs["user"]
	if id == "" {
		return ""
	}
	if name := attrs["username"]; name != "" {
		return " user=" + id + " (@" + name + ")"
	}
	return " user=" + id
}

func (h *PrettyHandler) Handle(_ context.Context, r slog.Record) error {
	var all []slog.Attr
	all = append(all, h.attrs...)
	r.Attrs(func(a slog.Attr) bool { all = append(all, a); return true })

	values := map[string]string{}
	var order []string
	for _, a := range all {
		key := h.group + a.Key
		values[key] = CleanLogText(a.Value.Resolve().String())
		order = append(order, key)
	}
	used := map[string]bool{}
	take := func(keys ...string) {
		for _, k := range keys {
			used[k] = true
		}
	}

	module := values["module"]
	take("module")
	if module == "" {
		module = sourceModule(r.PC)
	}
	if c := values["component"]; c != "" {
		module = c
		take("component")
	}

	when := r.Time
	if when.IsZero() {
		when = time.Now()
	}
	msg := CleanLogText(r.Message)
	var line strings.Builder
	switch {
	case msg == "command" && values["command"] != "":
		line.WriteString("command=" + values["command"])
		line.WriteString(logWhoAt(values))
		if values["chat"] != "" {
			line.WriteString(" chat=" + values["chat"])
		}
		if values["type"] != "" {
			line.WriteString(" [" + values["type"] + "]")
		}
		take("command", "user", "username", "type", "chat")
	case msg == "callback" && values["data"] != "":
		line.WriteString("callback=" + values["data"])
		line.WriteString(logWhoAt(values))
		take("data", "user", "username")
	default:
		line.WriteString(msg)
	}

	var long []string // values with line breaks go on their own lines
	for _, k := range order {
		if used[k] {
			continue
		}
		used[k] = true
		v := values[k]
		name := k
		if name == "err" {
			name = "error"
		}
		if strings.Contains(v, "\n") {
			long = append(long, "    "+name+": "+strings.ReplaceAll(strings.TrimRight(v, "\n"), "\n", "\n        "))
			continue
		}
		line.WriteString(" " + name + "=" + prettyValue(v))
	}
	head := fmt.Sprintf("%s | %-8s | %s | %s", when.Format("2006-01-02 15:04:05"), prettyLevel(r.Level), module, line.String())
	text := strings.Join(append([]string{head}, long...), "\n") + "\n"

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.out, text)
	return err
}
