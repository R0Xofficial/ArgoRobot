package modules_test

import (
	"testing"

	. "argo/internal/testutil"
)

func serviceMsg(extra map[string]any) Msg {
	return Msg{Chat: chat, From: bob, Text: "", Extra: extra}
}

var serviceKinds = map[string]map[string]any{
	"join":      {"new_chat_members": []map[string]any{{"id": 4242, "is_bot": false, "first_name": "New"}}},
	"leave":     {"left_chat_member": map[string]any{"id": 4243, "is_bot": false, "first_name": "Gone"}},
	"pin":       {"pinned_message": map[string]any{"message_id": 5, "date": 1, "chat": map[string]any{"id": chat, "type": "supergroup"}}},
	"photo":     {"delete_chat_photo": true},
	"title":     {"new_chat_title": "A new name"},
	"videochat": {"video_chat_started": map[string]any{}},
	"other":     {"message_auto_delete_timer_changed": map[string]any{"message_auto_delete_time": 60}},
}

// deletedKinds sends one service message of every kind and returns the kinds that were deleted.
func deletedKinds(e *Env) map[string]bool {
	got := map[string]bool{}
	for kind, extra := range serviceKinds {
		e.Fake.Reset()
		e.Send(serviceMsg(extra))
		got[kind] = deleted(e) > 0
	}
	return got
}

func TestCleanServiceKinds(t *testing.T) {
	e := setup2(t)

	// Nothing is deleted until a chat asks for it.
	for kind, gone := range deletedKinds(e) {
		if gone {
			t.Errorf("%s: deleted without being asked", kind)
		}
	}

	e.Send(Msg{Chat: chat, From: bob, Text: "/cleanservice pin"})
	contains(t, lastText(t, e), "Only chat admins")
	e.Send(Msg{Chat: chat, From: alice, Text: "/cleanservice pin, photo"})
	contains(t, lastText(t, e), "<b>photo, pin</b>")
	got := deletedKinds(e)
	for kind, gone := range got {
		if want := kind == "pin" || kind == "photo"; gone != want {
			t.Errorf("%s: deleted=%v, want %v", kind, gone, want)
		}
	}

	e.Send(Msg{Chat: chat, From: alice, Text: "/cleanservice"})
	contains(t, lastText(t, e), "<b>photo, pin</b>")
	e.Send(Msg{Chat: chat, From: alice, Text: "/cleanservice pin"})
	contains(t, lastText(t, e), "already")

	e.Send(Msg{Chat: chat, From: alice, Text: "/keepservice pin"})
	contains(t, lastText(t, e), "<b>photo</b>")
	if got := deletedKinds(e); got["pin"] || !got["photo"] {
		t.Errorf("after /keepservice pin: %v", got)
	}

	e.Send(Msg{Chat: chat, From: alice, Text: "/cleanservice videochat"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/nocleanservice videochat"})
	contains(t, lastText(t, e), "<b>photo</b>")

	e.Send(Msg{Chat: chat, From: alice, Text: "/cleanservice nonsense"})
	contains(t, lastText(t, e), "nonsense")
	contains(t, lastText(t, e), "/cleanservicetypes")

	e.Send(Msg{Chat: chat, From: alice, Text: "/cleanservicetypes"})
	for _, part := range []string{"<code>all</code>", "<code>join</code>", "<code>leave</code>", "<code>other</code>", "<code>photo</code>", "<code>pin</code>", "<code>title</code>", "<code>videochat</code>"} {
		contains(t, lastText(t, e), part)
	}

	// "on" and "all" are every kind, "off" and "keepservice all" none.
	e.Send(Msg{Chat: chat, From: alice, Text: "/cleanservice on"})
	contains(t, lastText(t, e), "<b>all</b>")
	for kind, gone := range deletedKinds(e) {
		if !gone {
			t.Errorf("%s: kept with /cleanservice on", kind)
		}
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/cleanservice off"})
	contains(t, lastText(t, e), "kept")
	e.Send(Msg{Chat: chat, From: alice, Text: "/cleanservice all"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/keepservice all"})
	contains(t, lastText(t, e), "kept")
	for kind, gone := range deletedKinds(e) {
		if gone {
			t.Errorf("%s: deleted after /keepservice all", kind)
		}
	}
}

// The old on/off switch meant "joined" and "left"; a chat that had it on keeps that until it chooses kinds.
func TestCleanServiceOldSwitch(t *testing.T) {
	e := setup2(t)
	if err := e.App.UpdateGreetings(e.Ctx(), chat, map[string]any{"clean_service": true}); err != nil {
		t.Fatal(err)
	}
	for kind, gone := range deletedKinds(e) {
		if want := kind == "join" || kind == "leave"; gone != want {
			t.Errorf("%s: deleted=%v, want %v", kind, gone, want)
		}
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/cleanservice"})
	contains(t, lastText(t, e), "<b>join, leave</b>")
	e.Send(Msg{Chat: chat, From: alice, Text: "/keepservice join"})
	contains(t, lastText(t, e), "<b>leave</b>")
	e.Send(Msg{Chat: chat, From: alice, Text: "/keepservice leave"})
	// Switched off for good: the old flag must not bring join and leave back.
	for kind, gone := range deletedKinds(e) {
		if gone {
			t.Errorf("%s: deleted after everything was kept", kind)
		}
	}
}

func TestServiceKindOfMessages(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/cleanservice all"})
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: bob, Text: "just a message"})
	if deleted(e) != 0 {
		t.Fatal("an ordinary message is never deleted")
	}
}
