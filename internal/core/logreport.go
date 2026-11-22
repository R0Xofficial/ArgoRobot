package core

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

// Scheduled log report for the developer log. Everything the bot writes to its log (INFO and
// above) is also kept in memory; every LOG_REPORT_HOURS hours, and once more when the bot stops, the collected lines
// are sent to the developer log as one .txt file. A report that could not be delivered is kept and sent with the next
// one. The buffer is limited: when it overflows the oldest entries are dropped and the report says how many.

const (
	logMaxEntries = 20000
	logMaxChars   = 4000 // a single entry is cut to this
)

// LogEntry is one line of the log: time, level, the part of the bot that wrote it and what happened.
type LogEntry struct {
	When  time.Time
	Level slog.Level
	Name  string
	Text  string
}

// LogBuffer is a slog handler that passes records on to another handler and keeps the ones of INFO and above.
type LogBuffer struct {
	inner slog.Handler
	state *logState
	attrs []slog.Attr
	group string
}

type logState struct {
	mu      sync.Mutex
	entries []LogEntry
	dropped int
}

// NewLogBuffer wraps a handler so that what it logs is also kept for the report.
func NewLogBuffer(inner slog.Handler) *LogBuffer {
	return &LogBuffer{inner: inner, state: &logState{}}
}

func (b *LogBuffer) Enabled(ctx context.Context, l slog.Level) bool { return b.inner.Enabled(ctx, l) }

func (b *LogBuffer) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *b
	c.inner = b.inner.WithAttrs(attrs)
	c.attrs = append(append([]slog.Attr{}, b.attrs...), attrs...)
	return &c
}

func (b *LogBuffer) WithGroup(name string) slog.Handler {
	c := *b
	c.inner = b.inner.WithGroup(name)
	if name != "" {
		c.group += name + "."
	}
	return &c
}

func logValue(v string) string {
	if v == "" || strings.ContainsAny(v, " \t\n\"=") {
		return strconv.Quote(v)
	}
	return v
}

func (b *LogBuffer) Handle(ctx context.Context, r slog.Record) error {
	err := b.inner.Handle(ctx, r)
	if r.Level < slog.LevelInfo {
		return err
	}
	var sb strings.Builder
	sb.WriteString(r.Message)
	add := func(a slog.Attr) {
		sb.WriteString(" " + b.group + a.Key + "=" + logValue(a.Value.Resolve().String()))
	}
	for _, a := range b.attrs {
		add(a)
	}
	r.Attrs(func(a slog.Attr) bool { add(a); return true })
	text := CleanLogText(sb.String())
	if len(text) > logMaxChars {
		text = text[:logMaxChars] + " ... (cut)"
	}
	name := "bot"
	if r.Message == "command" {
		name = "usage"
	}
	when := r.Time
	if when.IsZero() {
		when = time.Now()
	}
	s := b.state
	s.mu.Lock()
	if len(s.entries) >= logMaxEntries {
		s.entries = s.entries[1:]
		s.dropped++
	}
	s.entries = append(s.entries, LogEntry{when.UTC(), r.Level, name, text})
	s.mu.Unlock()
	return err
}

// Take hands over everything collected so far and starts empty.
func (b *LogBuffer) Take() ([]LogEntry, int) {
	s := b.state
	s.mu.Lock()
	defer s.mu.Unlock()
	batch, dropped := s.entries, s.dropped
	s.entries, s.dropped = nil, 0
	return batch, dropped
}

// PutBack returns a batch that could not be sent, in front of what came in meanwhile.
func (b *LogBuffer) PutBack(batch []LogEntry, dropped int) {
	s := b.state
	s.mu.Lock()
	defer s.mu.Unlock()
	all := append(append([]LogEntry{}, batch...), s.entries...)
	overflow := 0
	if len(all) > logMaxEntries {
		overflow = len(all) - logMaxEntries
		all = all[overflow:] // the oldest fall out first
	}
	s.entries = all
	s.dropped += dropped + overflow
}

func levelName(l slog.Level) string {
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

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// RenderLogReport is the caption (HTML), the file name and the content for a batch of entries. The batch must not be
// empty.
func RenderLogReport(batch []LogEntry, dropped int) (string, string, []byte) {
	first, last := batch[0].When, batch[len(batch)-1].When
	warnings, errs := 0, 0
	for _, e := range batch {
		if e.Level == slog.LevelWarn {
			warnings++
		} else if e.Level >= slog.LevelError {
			errs++
		}
	}
	summary := strconv.Itoa(len(batch))
	var parts []string
	if warnings > 0 {
		parts = append(parts, plural(warnings, "warning"))
	}
	if errs > 0 {
		parts = append(parts, plural(errs, "error"))
	}
	if len(parts) > 0 {
		summary += " (" + strings.Join(parts, ", ") + ")"
	}
	caption := "<b>Log report</b>\n<b>From:</b> " + Timestamp(first) + "\n<b>To:</b> " + Timestamp(last) + "\n<b>Entries:</b> " + summary
	if dropped > 0 {
		caption += fmt.Sprintf("\n<i>%d older entries were dropped to save memory.</i>", dropped)
	}

	const layout = "2006-01-02 15:04:05"
	lines := []string{
		"Argo log report (all times are UTC)",
		"From: " + first.Format(layout),
		"To:   " + last.Format(layout),
		"Entries: " + summary,
	}
	if dropped > 0 {
		lines = append(lines, fmt.Sprintf("%d older entries were dropped to save memory.", dropped))
	}
	lines = append(lines, "")
	day := ""
	for _, e := range batch {
		if d := e.When.Format("2006-01-02"); d != day {
			day = d
			lines = append(lines, "--- "+day+" ---")
		}
		text, rest, _ := strings.Cut(e.Text, "\n")
		lines = append(lines, fmt.Sprintf("%s  %-8s %-10s %s", e.When.Format("15:04:05"), levelName(e.Level), e.Name, text))
		if rest != "" {
			for _, extra := range strings.Split(rest, "\n") {
				lines = append(lines, "    "+extra)
			}
		}
	}
	name := "argo-log_" + first.Format("20060102-1504") + "_" + last.Format("20060102-1504") + ".txt"
	return caption, name, []byte(strings.Join(lines, "\n") + "\n")
}

// DevFile sends a text file with a caption to the developer log. It never fails: false says it did not arrive.
func (a *App) DevFile(ctx context.Context, name string, data []byte, caption string) bool {
	chat, thread := a.Cfg.DevLogChat, a.Cfg.DevLogThread
	if chat == 0 {
		chat, thread = a.Cfg.OwnerID, 0
	}
	_, err := a.Bot.SendDocumentWithContext(ctx, chat, gotgbot.InputFileByReader(name, bytes.NewReader(data)),
		&gotgbot.SendDocumentOpts{Caption: caption, ParseMode: parseHTML, MessageThreadId: thread})
	if err != nil {
		slog.Warn("Could not send the developer log file", "chat", chat, "err", err)
		return false
	}
	return true
}

// SendLogReport sends what has been collected to the developer log. Nothing collected: nothing is sent (true). When
// the sending fails the entries are kept for the next report.
func (a *App) SendLogReport(ctx context.Context) bool {
	b := a.LogBuffer
	if b == nil {
		return true
	}
	batch, dropped := b.Take()
	if len(batch) == 0 {
		return true
	}
	caption, name, data := RenderLogReport(batch, dropped)
	if a.DevFile(ctx, name, data, caption) {
		return true
	}
	b.PutBack(batch, dropped)
	return false
}

// RunLogReport sends a report every `hours` hours until ctx ends. hours <= 0 or no buffer: it does nothing.
func (a *App) RunLogReport(ctx context.Context, hours float64) {
	if hours <= 0 || a.LogBuffer == nil {
		return
	}
	every := time.Duration(hours * float64(time.Hour))
	slog.Info("Log report", "every_hours", strconv.FormatFloat(hours, 'g', -1, 64))
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sendCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			a.SendLogReport(sendCtx)
			cancel()
		}
	}
}
