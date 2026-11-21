package modules_test

import (
	"strings"
	"testing"

	. "argo/internal/testutil"
)

func TestMuteAndUnmute(t *testing.T) {
	e := setup(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/mute 20 flooding"})
	c := e.Fake.Calls("restrictChatMember")
	if len(c) != 1 || c[0].P["user_id"] != "20" || c[0].P["permissions"] != "{}" {
		t.Fatalf("restrictChatMember: %+v", c)
	}
	want(t, lastText(t, e), link(bob)+" was muted by "+link(alice)+"\nReason: flooding")
	if kb := e.Fake.Calls("sendMessage")[0].P["reply_markup"]; !strings.Contains(kb, `"callback_data":"undo:mute:20"`) {
		t.Errorf("undo button missing: %s", kb)
	}

	// Not muted yet according to Telegram: unmute refuses.
	e.Send(Msg{Chat: chat, From: alice, Text: "/unmute 20"})
	if !strings.Contains(lastText(t, e), "isn't muted") {
		t.Errorf("got %q", lastText(t, e))
	}

	e.Fake.SetMember(chat, bob.ID, bob.Name, "restricted", map[string]any{"can_send_messages": false})
	e.Fake.SetChat("-100500", map[string]any{"id": -100500, "type": "supergroup", "permissions": map[string]any{"can_send_messages": true, "can_send_photos": true}})
	e.Send(Msg{Chat: chat, From: alice, Text: "/unmute 20"})
	c = e.Fake.Calls("restrictChatMember")
	if len(c) != 2 || !strings.Contains(c[1].P["permissions"], `"can_send_photos":true`) {
		t.Fatalf("unmute did not restore the chat's permissions: %+v", c)
	}
	want(t, lastText(t, e), link(bob)+" was unmuted by "+link(alice))
}

func TestTmuteAndSilent(t *testing.T) {
	e := setup(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/tmute 20 2h spam"})
	c := e.Fake.Calls("restrictChatMember")
	if len(c) != 1 || c[0].P["until_date"] == "" {
		t.Fatalf("restrictChatMember: %+v", c)
	}
	if !strings.Contains(lastText(t, e), "for 2 hours") {
		t.Errorf("got %q", lastText(t, e))
	}
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: alice, Text: "/smute 20"})
	if len(e.Fake.Calls("sendMessage")) != 0 || len(e.Fake.Calls("deleteMessage")) != 1 {
		t.Fatalf("a silent mute must only delete the command: %+v", e.Fake.Calls("deleteMessage"))
	}
}

func TestUnmuteButton(t *testing.T) {
	e := setup(t)
	e.Fake.SetMember(chat, bob.ID, bob.Name, "restricted", map[string]any{"can_send_messages": false})
	e.Press(chat, alice, "undo:mute:20")
	if len(e.Fake.Calls("restrictChatMember")) != 1 || len(e.Fake.Calls("editMessageText")) != 1 {
		t.Fatalf("the button did not unmute: %v", e.Fake.Calls("restrictChatMember"))
	}
}
