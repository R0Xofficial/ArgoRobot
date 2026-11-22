package core

import (
	"context"
	"io"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The console log: one readable line per entry, e.g.
//
//	2026-11-22 13:04:46 INFO  command /fban  @ItsR0Xx (7410298411)  supergroup -1002345803559
//	2026-11-22 00:43:29 ERROR [updater] failed to get updates; sleeping 1s
//	    error: failed to execute POST request to getUpdates: context deadline exceeded
//
// Times are UTC. A value that is long or an error goes on its own indented line.

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
		return "WARN "
	case l >= slog.LevelInfo:
		return "INFO "
	}
	return "DEBUG"
}

func prettyValue(v string) string {
	if v == "" || strings.ContainsAny(v, " \t\"=") {
		return strconv.Quote(v)
	}
	return v
}

// logWho writes a person the way the log shows them: "@name (id)", or just the id.
func logWho(attrs map[string]string) string {
	id := attrs["user"]
	if name := attrs["username"]; name != "" {
		return "@" + name + " (" + id + ")"
	}
	if id == "" {
		return ""
	}
	return "user " + id
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

	when := r.Time
	if when.IsZero() {
		when = time.Now()
	}
	var line strings.Builder
	line.WriteString(when.UTC().Format("2006-01-02 15:04:05") + " " + prettyLevel(r.Level) + " ")
	if c := values["component"]; c != "" {
		line.WriteString("[" + c + "] ")
		take("component")
	}

	msg := CleanLogText(r.Message)
	switch {
	case msg == "command" && values["command"] != "":
		line.WriteString("command " + values["command"])
		if p := logWho(values); p != "" {
			line.WriteString("  " + p)
		}
		if values["type"] != "" || values["chat"] != "" {
			line.WriteString("  " + strings.TrimSpace(values["type"]+" "+values["chat"]))
		}
		take("command", "user", "username", "type", "chat")
	case msg == "callback" && values["data"] != "":
		line.WriteString("callback " + values["data"])
		if p := logWho(values); p != "" {
			line.WriteString("  " + p)
		}
		take("data", "user", "username")
	default:
		line.WriteString(msg)
	}

	var long []string // errors and long values go on their own lines
	for _, k := range order {
		if used[k] {
			continue
		}
		used[k] = true
		v := values[k]
		name := strings.TrimPrefix(k, h.group)
		switch {
		case name == "err" || name == "error" || name == "panic" || len(v) > 80 || strings.Contains(v, "\n"):
			if name == "err" {
				name = "error"
			}
			long = append(long, "    "+name+": "+strings.ReplaceAll(strings.TrimRight(v, "\n"), "\n", "\n        "))
		default:
			line.WriteString("  " + k + "=" + prettyValue(v))
		}
	}
	text := strings.Join(append([]string{line.String()}, long...), "\n") + "\n"

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.out, text)
	return err
}
