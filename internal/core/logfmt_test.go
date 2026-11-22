package core

import (
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func logLines(t *testing.T, level slog.Level, fn func(*slog.Logger)) string {
	t.Helper()
	var sink strings.Builder
	l := slog.New(NewPrettyHandler(&sink, level))
	fn(l)
	return sink.String()
}

func TestPrettyLogCommand(t *testing.T) {
	got := logLines(t, slog.LevelInfo, func(l *slog.Logger) {
		l.Info("command", "module", "modules.gbans", "command", "/fban", "user", int64(7410298411), "username", "ItsR0Xx", "chat", int64(-1002345803559), "type", "supergroup")
		l.Info("command", "command", "/start", "user", int64(5), "username", "", "chat", int64(5), "type", "private")
		l.Info("callback", "module", "modules.misc", "data", "help:module:gbans", "user", int64(8491934017), "username", "R3QU3R")
	})
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines: %q", got)
	}
	for i, want := range []string{
		" | INFO     | modules.gbans | command=/fban user=7410298411 (@ItsR0Xx) chat=-1002345803559 [supergroup]",
		" | INFO     | core.logfmt_test | command=/start user=5 chat=5 [private]",
		" | INFO     | modules.misc | callback=help:module:gbans user=8491934017 (@R3QU3R)",
	} {
		if !strings.HasSuffix(lines[i], want) {
			t.Errorf("line %d: %q, want it to end with %q", i, lines[i], want)
		}
		if len(lines[i]) < 19 || lines[i][4] != '-' || lines[i][10] != ' ' {
			t.Errorf("line %d has no time: %q", i, lines[i])
		}
	}
}

func TestPrettyLogErrorsAndTokens(t *testing.T) {
	err := errors.New(`failed to execute POST request to getUpdates: Post "https://api.telegram.org/bot123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw/getUpdates": context deadline exceeded`)
	got := logLines(t, slog.LevelInfo, func(l *slog.Logger) {
		l.With("component", "updater").Error("failed to get updates; sleeping 1s", "error", err)
		l.Warn("Could not ban", "user", 5, "chat", -100, "err", errors.New("line one\nline two"))
		l.Debug("hidden")
	})
	if strings.Contains(got, "AAHdq") || strings.Contains(got, "123456789") || strings.Contains(got, "api.telegram.org") {
		t.Fatalf("the token is in the log: %s", got)
	}
	for _, want := range []string{
		" | ERROR    | updater | failed to get updates; sleeping 1s error=\"failed to execute POST request to getUpdates: Post \\\"getUpdates\\\": context deadline exceeded\"\n",
		" | WARNING  | core.logfmt_test | Could not ban user=5 chat=-100\n    error: line one\n        line two\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "hidden") {
		t.Error("a debug line was written at the info level")
	}
}

func TestCleanLogText(t *testing.T) {
	for in, want := range map[string]string{
		"nothing to see": "nothing to see",
		"token 123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw leaked":                                  "token <token> leaked",
		"https://api.telegram.org/file/bot123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw/photos/a.jpg": "photos/a.jpg",
	} {
		if got := CleanLogText(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestLogBufferHidesTheToken(t *testing.T) {
	var sink strings.Builder
	buf := NewLogBuffer(NewPrettyHandler(&sink, slog.LevelInfo))
	slog.New(buf).Warn("failed", "err", "Post https://api.telegram.org/bot123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw/getMe: boom")
	batch, _ := buf.Take()
	if len(batch) != 1 || strings.Contains(batch[0].Text, "AAHdq") || !strings.Contains(batch[0].Text, "getMe") {
		t.Fatalf("%+v", batch)
	}
	_, _, data := BuildErrorReport("x", errors.New("Get https://api.telegram.org/bot123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw/getMe: boom"), nil, nil, time.Now())
	if strings.Contains(string(data), "AAHdq") {
		t.Fatal("the token is in the error report file")
	}
}
