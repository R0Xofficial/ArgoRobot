package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"argo/internal/testutil"
)

// Starts the real program (config, database, polling) against a fake Telegram, sends it a command through
// getUpdates and checks the answer, then stops it with SIGTERM and checks the "Bot stopped." notice.
func TestSmokeStartAnswerStop(t *testing.T) {
	fake := testutil.NewFake()
	defer fake.Close()
	fake.SetMember(-100500, 10, "Alice", "administrator", map[string]any{"can_restrict_members": true})
	fake.SetChat("20", map[string]any{"id": 20, "type": "private", "first_name": "Bob"})
	fake.SetMember(-100500, 20, "Bob", "member", nil)

	url := testutil.TestDatabaseURL()
	env := map[string]string{
		"BOT_TOKEN": "123:TEST", "OWNER_ID": "1", "DATABASE_URL": url, "TELEGRAM_API_URL": fake.Srv.URL,
		"LOCALES_DIR": "../../locales", "LOG_LEVEL": "WARNING",
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	// A clean database: the program creates the schema itself.
	cleanDB(t, url)

	fake.QueueUpdate(`{"update_id": 1, "message": {"message_id": 7, "date": 1000,
		"chat": {"id": -100500, "type": "supergroup", "title": "G"}, "from": {"id": 10, "is_bot": false, "first_name": "Alice"},
		"text": "/ban 20 spam"}}`)

	done := make(chan error, 1)
	go func() { done <- run() }()

	deadline := time.After(20 * time.Second)
	for {
		if len(fake.Calls("banChatMember")) == 1 {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("the program stopped early: %v", err)
		case <-deadline:
			t.Fatalf("no ban within 20s; calls: %+v", fake.Calls(""))
		case <-time.After(50 * time.Millisecond):
		}
	}
	if len(fake.Calls("deleteWebhook")) != 1 || fake.Calls("deleteWebhook")[0].P["drop_pending_updates"] != "true" {
		t.Errorf("deleteWebhook: %+v", fake.Calls("deleteWebhook"))
	}
	// getUpdates must ask for the update types that carry users.
	if g := fake.Calls("getUpdates"); len(g) == 0 || g[0].P["allowed_updates"] == "" {
		t.Errorf("getUpdates: %+v", g)
	}

	_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the program did not stop")
	}

	sent := fake.Calls("sendMessage")
	if len(sent) < 3 {
		t.Fatalf("expected start notice, answer and stop notice, got %d messages", len(sent))
	}
	first, last := sent[0].P["text"], sent[len(sent)-1].P["text"]
	if !strings.HasPrefix(first, "Bot started (@ArgoTestBot). Modules enabled: ") || last != "Bot stopped." {
		t.Errorf("notices: %q ... %q", first, last)
	}
	if sent[0].P["chat_id"] != "1" {
		t.Errorf("the notice goes to the owner: %v", sent[0].P)
	}
	fmt.Println("answer:", sent[1].P["text"])
}
