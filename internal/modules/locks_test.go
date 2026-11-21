package modules_test

import (
	"strings"
	"testing"

	. "argo/internal/testutil"
)

func deleted(e *Env) int {
	return len(e.Fake.Calls("deleteMessage")) + len(e.Fake.Calls("deleteMessages"))
}

func ents(typ string, off, n int) []map[string]any {
	return []map[string]any{{"type": typ, "offset": off, "length": n}}
}

func TestLockCommands(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: bob, Text: "/lock url"})
	contains(t, lastText(t, e), "Only chat admins")
	e.Send(Msg{Chat: chat, From: alice, Text: "/lock"})
	contains(t, lastText(t, e), "Use /lock")
	e.Send(Msg{Chat: chat, From: alice, Text: "/lock url nonsense"})
	contains(t, lastText(t, e), "nonsense")
	e.Send(Msg{Chat: chat, From: alice, Text: "/lock url, sticker"})
	contains(t, lastText(t, e), "<code>url</code>, <code>sticker</code>")
	e.Send(Msg{Chat: chat, From: alice, Text: "/locks"})
	got := lastText(t, e)
	contains(t, got, "• <code>sticker</code>")
	contains(t, got, "• <code>url</code>")
	e.Send(Msg{Chat: chat, From: alice, Text: "/unlock sticker"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/locks"})
	if g := lastText(t, e); len(g) == 0 || g == got {
		t.Fatal("unlock changes the list")
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/locktypes"})
	contains(t, lastText(t, e), "<code>invitelink</code>*")
	e.Send(Msg{Chat: chat, From: alice, Text: "/lockmode"})
	contains(t, lastText(t, e), "Use /lockmode")
	e.Send(Msg{Chat: chat, From: alice, Text: "/lockmode tmute 1h"})
	contains(t, lastText(t, e), "1 hour")
	e.Send(Msg{Chat: chat, From: alice, Text: "/lockmode bogus"})
	contains(t, lastText(t, e), "Use /lockmode")
	e.Send(Msg{Chat: chat, From: alice, Text: "/lockwarns on"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/lockwarns"})
	contains(t, lastText(t, e), "on")
	e.Send(Msg{Chat: chat, From: alice, Text: "/lockwarns maybe"})
	contains(t, lastText(t, e), "Use /lockwarns")
}

func TestLockDeletesAndExempts(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/lock url photo text"})
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: bob, Text: "hello"})
	if deleted(e) != 1 {
		t.Fatalf("text is locked: %d", deleted(e))
	}
	e.Send(Msg{Chat: chat, From: bob, Photo: "p"})
	if deleted(e) != 2 {
		t.Fatalf("photo is locked: %d", deleted(e))
	}
	// admins are exempt
	e.Send(Msg{Chat: chat, From: alice, Text: "hello"})
	if deleted(e) != 2 {
		t.Fatal("admins are exempt")
	}
	// approved users are exempt
	e.Send(Msg{Chat: chat, From: alice, Text: "/approve", Reply: &Msg{Chat: chat, From: carol, Text: "x"}})
	n := deleted(e)
	e.Send(Msg{Chat: chat, From: carol, Text: "hi"})
	if deleted(e) != n {
		t.Fatal("approved users are exempt")
	}
	if len(e.Fake.Calls("restrictChatMember")) != 0 {
		t.Fatal("only deleting by default")
	}
}

func TestLockUrlAllowlist(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/lock url"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/allowlist https://www.Example.com/path?x=1 @Some_Name $btc /Start 12345 t.me/addstickers/Pack"})
	contains(t, lastText(t, e), "Allowlisted <b>6</b> new items")
	e.Send(Msg{Chat: chat, From: alice, Text: "/allowlist"})
	got := lastText(t, e)
	for _, w := range []string{"example.com/path", "@some_name", "$BTC", "/start", "12345", "stickerpack:pack"} {
		contains(t, got, w)
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/allowlist nodots"})
	contains(t, lastText(t, e), "nodots")
	e.Send(Msg{Chat: chat, From: alice, Text: "/allowlist example.org"})
	e.Fake.Reset()

	link := func(s string) {
		e.Send(Msg{Chat: chat, From: bob, Text: s, Entities: ents("url", 0, len(s))})
	}
	link("example.org/a")
	link("sub.example.org")
	link("http://www.example.org")
	if deleted(e) != 0 {
		t.Fatalf("allowlisted site: %d", deleted(e))
	}
	link("evil.com")
	link("example.org.evil.com")
	if deleted(e) != 2 {
		t.Fatalf("other sites are locked: %d", deleted(e))
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/rmallowlist example.org"})
	contains(t, lastText(t, e), "Removed <b>1</b> item")
	e.Send(Msg{Chat: chat, From: alice, Text: "/rmallowlist example.org"})
	contains(t, lastText(t, e), "None of those")
	link("example.org")
	if deleted(e) != 3 {
		t.Fatal("removed from the allowlist")
	}
}

func TestLockClearAllowlist(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/rmallowlistall"})
	contains(t, lastText(t, e), "Only the owner")
	e.Send(Msg{Chat: chat, From: owner, Text: "/rmallowlistall"})
	contains(t, lastText(t, e), "empty")
	e.Send(Msg{Chat: chat, From: alice, Text: "/allowlist a.com b.com"})
	e.Send(Msg{Chat: chat, From: owner, Text: "/rmallowlistall"})
	contains(t, lastText(t, e), "2")
	e.Fake.Reset()
	e.Press(chat, alice, "locks:clear:yes")
	if len(e.Fake.Calls("editMessageText")) != 0 {
		t.Fatal("only the owner presses it")
	}
	e.Press(chat, owner, "locks:clear:yes")
	if c := e.Fake.Calls("editMessageText"); len(c) != 1 || !strings.Contains(c[0].P["text"], "2") {
		t.Fatalf("edit: %+v", c)
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/allowlist"})
	contains(t, lastText(t, e), "empty")
}

func TestLockWarnAndModes(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/lock sticker"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/lockwarns on"})
	e.Fake.Reset()
	sticker := map[string]any{"sticker": map[string]any{"file_id": "s", "file_unique_id": "u", "type": "regular", "width": 1, "height": 1, "is_animated": false, "is_video": false, "set_name": "mypack"}}
	e.Send(Msg{Chat: chat, From: bob, Extra: sticker})
	if deleted(e) != 1 {
		t.Fatal("sticker deleted")
	}
	contains(t, lastText(t, e), "warned")
	// a second one within seconds earns no second warning
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: bob, Extra: sticker})
	if deleted(e) != 1 || len(e.Fake.Calls("sendMessage")) != 0 {
		t.Fatal("warn cooldown")
	}
	// the pack on the allowlist stays
	e.Send(Msg{Chat: chat, From: alice, Text: "/allowlist t.me/addstickers/mypack"})
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: dave, Extra: sticker})
	if deleted(e) != 0 {
		t.Fatal("allowlisted pack")
	}
	// mute mode
	e.Send(Msg{Chat: chat, From: alice, Text: "/unlock sticker"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/lock voice"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/lockmode mute"})
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: carol, Extra: map[string]any{"voice": map[string]any{"file_id": "v", "file_unique_id": "v", "duration": 1}}})
	if c := e.Fake.Calls("restrictChatMember"); len(c) != 1 || c[0].P["user_id"] != "30" {
		t.Fatalf("mute: %+v", c)
	}
}

func TestLockKinds(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/lock cyrillic cjk rtl zalgo email command forward cashtag"})
	e.Fake.Reset()
	cases := []struct {
		m    Msg
		want bool
	}{
		{Msg{Chat: chat, From: bob, Text: "привет"}, true},
		{Msg{Chat: chat, From: bob, Text: "你好"}, true},
		{Msg{Chat: chat, From: bob, Text: "שלום"}, true},
		{Msg{Chat: chat, From: bob, Text: "à́̂̃b"}, true},
		{Msg{Chat: chat, From: bob, Text: "à́b"}, false},
		{Msg{Chat: chat, From: bob, Text: "plain words"}, false},
		{Msg{Chat: chat, From: bob, Text: "a@b.co", Entities: ents("email", 0, 6)}, true},
		{Msg{Chat: chat, From: bob, Text: "/rules", Entities: ents("bot_command", 0, 6)}, true},
		{Msg{Chat: chat, From: bob, Text: "x $BTC", Entities: ents("cashtag", 2, 4)}, true},
		{Msg{Chat: chat, From: bob, Text: "fwd", Extra: map[string]any{"forward_origin": map[string]any{"type": "hidden_user", "date": 1, "sender_user_name": "X"}}}, true},
		{Msg{Chat: chat, From: bob, Text: "fwd", Extra: map[string]any{"is_automatic_forward": true, "forward_origin": map[string]any{"type": "hidden_user", "date": 1, "sender_user_name": "X"}}}, false},
	}
	for i, tc := range cases {
		before := deleted(e)
		e.Send(tc.m)
		if got := deleted(e) > before; got != tc.want {
			t.Errorf("case %d (%q): deleted=%v want %v", i, tc.m.Text, got, tc.want)
		}
	}
	// edited messages are checked too
	before := deleted(e)
	e.Send(Msg{Chat: chat, From: bob, Text: "привет", Edited: true})
	if deleted(e) != before+1 {
		t.Fatal("edited messages count")
	}
}

func TestLockChannelAndBots(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/lock anonchannel bot"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/lockwarns on"})
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: bob, Text: "post", SenderChat: map[string]any{"id": -1001, "type": "channel", "title": "Ch"}})
	if deleted(e) != 1 || len(e.Fake.Calls("sendMessage")) != 0 {
		t.Fatal("a channel is only deleted")
	}
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: bob, Text: "", Extra: map[string]any{"new_chat_members": []map[string]any{{"id": 777, "is_bot": true, "first_name": "Spam"}}}})
	if c := e.Fake.Calls("banChatMember"); len(c) != 1 || c[0].P["user_id"] != "777" {
		t.Fatalf("ban: %+v", e.Fake.Calls("banChatMember"))
	}
	if len(e.Fake.Calls("unbanChatMember")) != 1 {
		t.Fatal("unban follows")
	}
	contains(t, lastText(t, e), "Spam")
	if deleted(e) != 0 {
		t.Fatal("the joined message stays unless the chat cleans service messages")
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/cleanservice on"})
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: bob, Text: "", Extra: map[string]any{"new_chat_members": []map[string]any{{"id": 779, "is_bot": true, "first_name": "Spam2"}}}})
	if deleted(e) != 1 {
		t.Fatal("this handler takes the message away from the greetings one, so it cleans it")
	}
	// an admin adding a bot is fine
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: alice, Text: "", Extra: map[string]any{"new_chat_members": []map[string]any{{"id": 778, "is_bot": true, "first_name": "Good"}}}})
	if len(e.Fake.Calls("banChatMember")) != 0 {
		t.Fatal("admins may add bots")
	}
}
