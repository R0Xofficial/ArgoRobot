package modules_test

import (
	"strings"
	"testing"

	. "argo/internal/testutil"
)

func TestFloodSettingsCommands(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/flood"})
	want(t, lastText(t, e), "<b>Flood control in Test group</b>\n\nFlood control is <b>off</b>.\nAnti-channel: <b>off</b>")
	e.Send(Msg{Chat: chat, From: bob, Text: "/setflood 5"})
	contains(t, lastText(t, e), "Only chat admins")
	e.Send(Msg{Chat: chat, From: alice, Text: "/setflood 1"})
	contains(t, lastText(t, e), "Use /setflood")
	e.Send(Msg{Chat: chat, From: alice, Text: "/setflood 5"})
	contains(t, lastText(t, e), "5")
	e.Send(Msg{Chat: chat, From: alice, Text: "/setfloodtimer 4 10s"})
	contains(t, lastText(t, e), "4")
	e.Send(Msg{Chat: chat, From: alice, Text: "/setfloodtimer 4"})
	contains(t, lastText(t, e), "Use /setfloodtimer")
	e.Send(Msg{Chat: chat, From: alice, Text: "/setfloodmode tmute 2h"})
	contains(t, lastText(t, e), "2 hours")
	e.Send(Msg{Chat: chat, From: alice, Text: "/setfloodmode tmute"})
	contains(t, lastText(t, e), "needs a duration")
	e.Send(Msg{Chat: chat, From: alice, Text: "/setfloodmode ban 2h"})
	contains(t, lastText(t, e), "doesn't take a duration")
	e.Send(Msg{Chat: chat, From: alice, Text: "/clearflood on"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/antichannel on"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/flood"})
	got := lastText(t, e)
	contains(t, got, "Action: <b>")
	contains(t, got, "Delete all flood messages: <b>on</b>")
	contains(t, got, "Anti-channel: <b>on</b>")
	e.Send(Msg{Chat: chat, From: alice, Text: "/setflood off"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/setfloodtimer off"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/flood"})
	contains(t, lastText(t, e), "Flood control is <b>off</b>.")
}

func TestFloodLimit(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/setflood 3"})
	e.Fake.Reset()
	for i := 0; i < 3; i++ {
		e.Send(Msg{Chat: chat, From: bob, Text: "spam"})
	}
	if len(e.Fake.Calls("restrictChatMember")) != 0 {
		t.Fatal("three messages are allowed")
	}
	e.Send(Msg{Chat: chat, From: bob, Text: "spam"})
	if c := e.Fake.Calls("restrictChatMember"); len(c) != 1 || c[0].P["user_id"] != "20" {
		t.Fatalf("restrict: %+v", c)
	}
	if len(e.Fake.Calls("deleteMessages")) != 1 {
		t.Fatalf("the message over the limit is deleted: %+v", e.Fake.Calls("deleteMessages"))
	}
	contains(t, lastText(t, e), "Flood control")
	contains(t, lastText(t, e), "muted")

	// Counting starts again; somebody else in between breaks a run; admins and approved users are exempt.
	e.Fake.Reset()
	for i := 0; i < 5; i++ {
		e.Send(Msg{Chat: chat, From: alice, Text: "hello"})
	}
	e.Send(Msg{Chat: chat, From: carol, Text: "a"})
	e.Send(Msg{Chat: chat, From: carol, Text: "a"})
	e.Send(Msg{Chat: chat, From: dave, Text: "b"})
	e.Send(Msg{Chat: chat, From: carol, Text: "a"})
	e.Send(Msg{Chat: chat, From: carol, Text: "a"})
	if len(e.Fake.Calls("restrictChatMember")) != 0 {
		t.Fatal("nobody should be punished")
	}
}

func TestFloodTimerClearAndAlbum(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/setfloodtimer 3 60s"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/clearflood on"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/setfloodmode ban"})
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: bob, Text: "1"})
	e.Send(Msg{Chat: chat, From: carol, Text: "x"})
	e.Send(Msg{Chat: chat, From: bob, Text: "2"})
	e.Send(Msg{Chat: chat, From: carol, Text: "x"})
	if len(e.Fake.Calls("banChatMember")) != 0 {
		t.Fatal("two messages are fine")
	}
	e.Send(Msg{Chat: chat, From: bob, Text: "3"})
	if len(e.Fake.Calls("banChatMember")) != 1 {
		t.Fatalf("ban: %+v", e.Fake.Calls("banChatMember"))
	}
	d := e.Fake.Calls("deleteMessages")
	if len(d) != 1 || strings.Count(d[0].P["message_ids"], ",") != 2 {
		t.Fatalf("the whole flood goes: %+v", d)
	}

	// An album is one message.
	e.Fake.Reset()
	for i := 0; i < 5; i++ {
		e.Send(Msg{Chat: chat, From: dave, Photo: "p", Extra: map[string]any{"media_group_id": "A"}})
	}
	if len(e.Fake.Calls("banChatMember")) != 0 {
		t.Fatal("an album counts once")
	}
}

func TestFloodFailureAndExemptions(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/setflood 2"})
	if _, err := e.App.Approve(e.Ctx(), chat, carol.ID, "Carol", alice.ID, "Alice"); err != nil {
		t.Fatal(err)
	}
	e.Fake.Reset()
	for i := 0; i < 4; i++ {
		e.Send(Msg{Chat: chat, From: carol, Text: "hi"})
	}
	if len(e.Fake.Calls("restrictChatMember")) != 0 {
		t.Fatal("approved users are exempt")
	}
	e.Fake.Fail("restrictChatMember", 400, "Bad Request: not enough rights to restrict/unrestrict chat member")
	for i := 0; i < 3; i++ {
		e.Send(Msg{Chat: chat, From: bob, Text: "hi"})
	}
	contains(t, lastText(t, e), "is flooding the chat")
}

func TestAntichannel(t *testing.T) {
	e := setup2(t)
	channel := map[string]any{"id": int64(-100900), "type": "channel", "title": "Loud <News>", "username": "loudnews"}
	e.Send(Msg{Chat: chat, From: alice, Text: "/antichannel on"})
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: bob, Text: "post", SenderChat: channel})
	if c := e.Fake.Calls("banChatSenderChat"); len(c) != 1 || c[0].P["sender_chat_id"] != "-100900" {
		t.Fatalf("ban sender chat: %+v", c)
	}
	if len(e.Fake.Calls("deleteMessage")) != 1 {
		t.Fatal("the post should be deleted")
	}
	want(t, lastText(t, e), `<a href="https://t.me/loudnews">Loud &lt;News&gt;</a> has been banned: speaking as a channel isn't allowed here. Talk as yourself.`)

	// The chat itself (an anonymous admin) is never touched.
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: bob, Text: "x", SenderChat: map[string]any{"id": chat, "type": "supergroup", "title": "Test group"}})
	if len(e.Fake.Calls("banChatSenderChat")) != 0 {
		t.Fatal("anonymous admins are fine")
	}
}
