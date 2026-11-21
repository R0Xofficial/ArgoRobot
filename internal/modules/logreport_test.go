package modules_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"argo/internal/core"

	. "argo/internal/testutil"
)

func TestLogReport(t *testing.T) {
	e := setup2(t)
	var sink strings.Builder
	buf := core.NewLogBuffer(slog.NewTextHandler(&sink, &slog.HandlerOptions{Level: slog.LevelDebug}))
	log := slog.New(buf)
	log.Debug("hidden")
	log.Info("command", "command", "/ban", "user", 1, "note", "two words")
	log.Warn("careful")
	log.Error("broken\nline two")
	if !strings.Contains(sink.String(), "hidden") {
		t.Error("the inner handler did not get the debug line")
	}

	// Nothing buffered or no buffer: nothing is sent.
	if !e.App.SendLogReport(context.Background()) || len(e.Fake.Calls("sendDocument")) != 0 {
		t.Fatal("sent without a buffer")
	}
	e.App.LogBuffer = buf
	if !e.App.SendLogReport(context.Background()) {
		t.Fatal("failed")
	}
	docs := e.Fake.Calls("sendDocument")
	if len(docs) != 1 {
		t.Fatalf("%d documents", len(docs))
	}
	contains(t, docs[0].P["caption"], "<b>Entries:</b> 3 (1 warning, 1 error)")
	contains(t, docs[0].P["chat_id"], "1")
	file := docs[0].P["_file_document"]
	for _, part := range []string{"Argo log report (all times are UTC)", "usage", `note="two words"`, "WARNING", "    line two"} {
		contains(t, file, part)
	}
	if strings.Contains(file, "hidden") {
		t.Error("debug line in the report")
	}

	// Empty again; a report that cannot be delivered is kept for the next one.
	e.Fake.Reset()
	log.Info("later")
	e.Fake.Fail("sendDocument", 500, "boom")
	if e.App.SendLogReport(context.Background()) {
		t.Fatal("a failed sending reported success")
	}
	log.Info("even later")
	e.Fake.Reset()
	e.App.SendLogReport(context.Background())
	docs = e.Fake.Calls("sendDocument")
	if len(docs) != 1 {
		t.Fatal("kept report not sent")
	}
	contains(t, docs[0].P["caption"], "<b>Entries:</b> 2")
}

func TestRenderLogReportDropped(t *testing.T) {
	at := time.Date(2026, 11, 11, 14, 0, 3, 0, time.UTC)
	cap, name, data := core.RenderLogReport([]core.LogEntry{{When: at, Level: slog.LevelInfo, Name: "bot", Text: "x"}}, 7)
	contains(t, cap, "7 older entries were dropped")
	want(t, name, "argo-log_20261111-1400_20261111-1400.txt")
	contains(t, string(data), "--- 2026-11-11 ---")
	contains(t, string(data), "14:00:03  INFO     bot        x")
}

func TestAdminRefresh(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: bob, Text: "hello"})
	e.App.WaitAdminRefresh()
	if n := len(e.Fake.Calls("getChatAdministrators")); n != 1 {
		t.Fatalf("%d refreshes", n)
	}
	// Not again for hours.
	e.Send(Msg{Chat: chat, From: bob, Text: "again"})
	e.App.WaitAdminRefresh()
	if n := len(e.Fake.Calls("getChatAdministrators")); n != 1 {
		t.Fatalf("%d refreshes", n)
	}
	row, err := e.App.UserByID(context.Background(), owner.ID)
	if err != nil || row == nil {
		t.Fatalf("the admin was not recorded: %v %v", row, err)
	}
}
