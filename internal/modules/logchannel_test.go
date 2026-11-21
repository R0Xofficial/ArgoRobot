package modules_test

import (
	"testing"

	. "argo/internal/testutil"
)

func TestLogchannel(t *testing.T) {
	e := setup2(t)
	const logID = -100777
	e.Send(Msg{Chat: chat, From: alice, Text: "/setlog"})
	contains(t, lastText(t, e), "Tell me")
	e.Send(Msg{Chat: chat, From: alice, Text: "/logchannel"})
	contains(t, lastText(t, e), "no log")
	e.Send(Msg{Chat: chat, From: alice, Text: "/unsetlog"})
	contains(t, lastText(t, e), "no log")

	e.Send(Msg{Chat: chat, From: alice, Text: "/setlog -100999"})
	contains(t, lastText(t, e), "find that chat")

	e.Fake.SetChat(itoa(logID), map[string]any{"id": logID, "type": "channel", "title": "Staff log"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/setlog " + itoa(logID)})
	contains(t, lastText(t, e), "admin") // alice is no admin of the log chat yet
	if got, _ := e.App.LogChat(e.Ctx(), chat); got != 0 {
		t.Fatal("log set without admin rights")
	}

	e.Fake.SetMember(logID, alice.ID, alice.Name, "administrator", nil)
	e.Send(Msg{Chat: chat, From: bob, Text: "/setlog " + itoa(logID)})
	if got, _ := e.App.LogChat(e.Ctx(), chat); got != 0 {
		t.Fatal("a member set a log")
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/setlog " + itoa(logID)})
	contains(t, lastText(t, e), "Staff log")
	if got, _ := e.App.LogChat(e.Ctx(), chat); got != logID {
		t.Fatalf("log chat %d", got)
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/logchannel"})
	contains(t, lastText(t, e), "Staff log")

	e.Send(Msg{Chat: chat, From: alice, Text: "/unsetlog"})
	contains(t, lastText(t, e), "no longer")
	if got, _ := e.App.LogChat(e.Ctx(), chat); got != 0 {
		t.Fatal("log not removed")
	}
}
