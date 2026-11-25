package modules_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"argo/internal/modules"
	. "argo/internal/testutil"
)

var (
	owner = Person{ID: 50, Name: "Olga"} // owner of the chat
	dave  = Person{ID: 60, Name: "Dave"}
)

var allRights = map[string]any{
	"can_manage_chat": true, "can_change_info": true, "can_delete_messages": true, "can_invite_users": true,
	"can_restrict_members": true, "can_pin_messages": true, "can_manage_video_chats": true, "can_manage_topics": true,
	"can_manage_tags": true, "can_promote_members": true,
}

// setup2 is setup plus an owner of the chat, a bot that is an admin with every right, and the group known to getChat.
func setup2(t *testing.T) *Env {
	t.Helper()
	e := setup(t)
	e.Fake.SetMember(chat, owner.ID, owner.Name, "creator", nil)
	// Alice is an admin with every right but the one to add admins.
	rights := map[string]any{}
	for k, v := range allRights {
		rights[k] = v
	}
	delete(rights, "can_promote_members")
	e.Fake.SetMember(chat, alice.ID, alice.Name, "administrator", rights)
	e.Fake.SetMember(chat, BotID, "Argo", "administrator", allRights)
	e.Fake.SetMember(chat, dave.ID, dave.Name, "member", nil)
	e.Fake.SetChat(itoa(chat), map[string]any{"id": chat, "type": "supergroup", "title": "Test group"})
	e.Fake.SetChat(itoa(dave.ID), map[string]any{"id": dave.ID, "type": "private", "first_name": dave.Name})
	return e
}

func sent(e *Env) []string {
	var out []string
	for _, c := range e.Fake.Calls("sendMessage") {
		out = append(out, c.P["text"])
	}
	return out
}

func contains(t *testing.T, got, part string) {
	t.Helper()
	if !strings.Contains(got, part) {
		t.Errorf("\n got: %q\nwant it to contain: %q", got, part)
	}
}

// said is true when one of the bot's messages contains the text.
func said(e *Env, part string) bool {
	for _, s := range sent(e) {
		if strings.Contains(s, part) {
			return true
		}
	}
	return false
}

func answered(t *testing.T, e *Env, part string) {
	t.Helper()
	if !said(e, part) {
		t.Errorf("none of the bot's messages contains %q: %q", part, sent(e))
	}
}

func nothing(t *testing.T, e *Env) {
	t.Helper()
	if s := sent(e); len(s) != 0 {
		t.Errorf("the bot should have stayed silent, it sent %q", s)
	}
}

func private(p Person, text string) Msg { return Msg{Chat: p.ID, Type: "private", From: p, Text: text} }

// --- disabling -------------------------------------------------------------------

func TestDisableCommand(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/disable /ban"})
	want(t, lastText(t, e), "/ban is now switched off for members. Admins can still use it. Its aliases (/dban, /sban) follow.")
	e.Fake.Reset()

	// A member is not answered; an admin still is.
	e.Send(Msg{Chat: chat, From: bob, Text: "/ban", Reply: reply(carol, "x")})
	nothing(t, e)
	e.Send(Msg{Chat: chat, From: alice, Text: "/ban", Reply: reply(carol, "x")})
	if len(e.Fake.Calls("banChatMember")) != 1 {
		t.Fatalf("an admin must still be able to ban: %+v", e.Fake.Calls("banChatMember"))
	}

	e.Send(Msg{Chat: chat, From: alice, Text: "/disable ban"})
	want(t, lastText(t, e), "/ban is already switched off.")
	e.Send(Msg{Chat: chat, From: alice, Text: "/disabled"})
	want(t, lastText(t, e), "Commands switched off for members (admins can still use them): /ban")
	e.Send(Msg{Chat: chat, From: alice, Text: "/enable ban"})
	want(t, lastText(t, e), "/ban is switched on again. Its aliases (/dban, /sban) follow.")
	e.Send(Msg{Chat: chat, From: alice, Text: "/enable ban"})
	want(t, lastText(t, e), "/ban isn't switched off.")
	e.Send(Msg{Chat: chat, From: alice, Text: "/disabled"})
	want(t, lastText(t, e), "Nothing is switched off in this chat.")
}

func TestDisableRules(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/disable disable"})
	want(t, lastText(t, e), "/disable can't be switched off.")
	e.Send(Msg{Chat: chat, From: alice, Text: "/disable nonsense"})
	want(t, lastText(t, e), "I don't have a command called <code>/nonsense</code>.")
	e.Send(Msg{Chat: chat, From: alice, Text: "/disable"})
	contains(t, lastText(t, e), "Use /disable")
	// Members can't change switches.
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: bob, Text: "/disable ban"})
	contains(t, lastText(t, e), "Only chat admins")
}

func TestRestrictModule(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/restrictmodule bans"})
	want(t, lastText(t, e), "The commands of <b>bans</b> are now switched off for members. Admins can still use them.")
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: bob, Text: "/ban", Reply: reply(carol, "x")})
	nothing(t, e)
	e.Send(Msg{Chat: chat, From: alice, Text: "/disabled"})
	want(t, lastText(t, e), "Modules switched off for members (admins can still use them): <code>bans</code>")
	e.Send(Msg{Chat: chat, From: alice, Text: "/unrestrictmodule bans"})
	contains(t, lastText(t, e), "switched on again for members")
	e.Send(Msg{Chat: chat, From: alice, Text: "/restrictmodule nope"})
	contains(t, lastText(t, e), "I don't have a module called")
}

func TestDisableModule(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/disablemodule notes"})
	want(t, lastText(t, e), "Only the owner of this chat can change this.")
	e.Send(Msg{Chat: chat, From: owner, Text: "/disablemodule notes"})
	want(t, lastText(t, e), "<b>notes</b> is now off in this chat: its commands and automatic actions stop.")
	e.Send(Msg{Chat: chat, From: owner, Text: "/disablemodule notes"})
	contains(t, lastText(t, e), "already off")

	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: alice, Text: "/save hello world"})
	nothing(t, e) // the module is off: not even an admin is answered
	e.Send(Msg{Chat: chat, From: alice, Text: "/disabled"})
	want(t, lastText(t, e), "Modules turned off for everybody: <code>notes</code>")

	e.Send(Msg{Chat: chat, From: owner, Text: "/disablemodule disabling"})
	want(t, lastText(t, e), "<b>disabling</b> is part of my core and can't be switched off.")
	e.Send(Msg{Chat: chat, From: owner, Text: "/enablemodule notes"})
	want(t, lastText(t, e), "<b>notes</b> is on again in this chat.")
	e.Send(Msg{Chat: chat, From: alice, Text: "/save hello world"})
	contains(t, lastText(t, e), "hello")

	e.Send(Msg{Chat: chat, From: owner, Text: "/disablemodule all"})
	contains(t, lastText(t, e), "modules are now off in this chat.")
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: alice, Text: "/ban", Reply: reply(carol, "x")})
	nothing(t, e)
	e.Send(Msg{Chat: chat, From: owner, Text: "/enablemodule all"})
	want(t, lastText(t, e), "Every module is on again.")
}

// --- connection ---------------------------------------------------------------------

func TestConnection(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/connect"})
	calls := e.Fake.Calls("sendMessage")
	want(t, calls[0].P["text"], "Tap the button to connect your private chat with me to this group. Only admins can.")
	contains(t, calls[0].P["reply_markup"], "https://t.me/ArgoTestBot?start=connect_-100500")
	e.Fake.Reset()

	// The button of the group leads to /start connect_<id> in the private chat.
	e.Send(private(alice, "/start connect_-100500"))
	want(t, lastText(t, e), "You are connected to <b>Test group</b>. Its admin commands you send me here now act on it.\nUse /disconnect to stop.")
	e.Send(private(alice, "/connection"))
	want(t, lastText(t, e), "You are connected to <b>Test group</b> (<code>-100500</code>).")

	// A group command sent in the private chat acts on the group; the answer comes back in the private chat.
	e.Fake.Reset()
	e.Send(private(alice, "/setrules Be nice"))
	var row string
	if err := e.DB.Row(e.Ctx(), "SELECT content FROM rules WHERE chat_id = $1", []any{chat}, &row); err != nil || row != "Be nice" {
		t.Fatalf("rules of the group: %q %v", row, err)
	}
	last := e.Fake.Calls("sendMessage")
	if len(last) == 0 || last[len(last)-1].P["chat_id"] != itoa(alice.ID) {
		t.Fatalf("the answer must go to the private chat: %+v", last)
	}

	// Bob is no admin of the group: he can't connect.
	e.Send(private(bob, "/connect -100500"))
	want(t, lastText(t, e), "Only admins of that group can connect to it.")
	e.Send(private(bob, "/connection"))
	contains(t, lastText(t, e), "You are not connected to a group")

	e.Send(private(alice, "/disconnect"))
	want(t, lastText(t, e), "You are no longer connected to <b>Test group</b>.")
	e.Send(private(alice, "/disconnect"))
	want(t, lastText(t, e), "You are not connected to a group.")

	e.Send(private(alice, "/connect -100500"))
	contains(t, lastText(t, e), "You are connected to <b>Test group</b>")
	e.Send(private(alice, "/connect @nobody_here"))
	contains(t, lastText(t, e), "find that chat")
}

func TestConnectionEndsWhenTurnedOff(t *testing.T) {
	e := setup2(t)
	e.Send(private(alice, "/connect -100500"))
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: owner, Text: "/disablemodule connection"})
	texts := sent(e)
	if len(texts) != 2 {
		t.Fatalf("sent %q", texts)
	}
	contains(t, texts[0], "Your connection to <b>Test group</b> has ended: connections were switched off there.")
	contains(t, texts[1], "\n1 connection to this chat ended.")
	e.Fake.Reset()
	e.Send(private(alice, "/connection"))
	contains(t, lastText(t, e), "not connected")
	e.Send(private(alice, "/connect -100500"))
	want(t, lastText(t, e), "Connections are switched off in that group.")
}

// --- purges ---------------------------------------------------------------------------

func TestDelAndPurge(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/del", Reply: reply(bob, "spam")})
	if n := len(e.Fake.Calls("deleteMessage")); n != 2 {
		t.Fatalf("deleteMessage calls: %d (the message and the command)", n)
	}
	e.Fake.Reset()

	e.Send(Msg{Chat: chat, From: alice, Text: "/purge cleaning up <b>", Reply: reply(bob, "spam")})
	calls := e.Fake.Calls("deleteMessages")
	if len(calls) != 1 || strings.Count(calls[0].P["message_ids"], ",") != 1 {
		t.Fatalf("deleteMessages: %+v", calls)
	}
	want(t, lastText(t, e), "Purge complete: up to 2 messages deleted.\nReason: <code>cleaning up &lt;b&gt;</code>")

	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: alice, Text: "/spurge", Reply: reply(bob, "spam")})
	if len(e.Fake.Calls("deleteMessages")) != 1 {
		t.Fatal("spurge should delete")
	}
	nothing(t, e)

	// Members can't.
	e.Send(Msg{Chat: chat, From: bob, Text: "/purge", Reply: reply(carol, "x")})
	contains(t, lastText(t, e), "Only chat admins")
}

func TestPurgeLimits(t *testing.T) {
	e := setup2(t)
	send := func(back int) {
		e.Raw(map[string]any{"message": map[string]any{
			"message_id": 5000, "date": 1000, "chat": map[string]any{"id": chat, "type": "supergroup", "title": "Test group"},
			"from": alice.JSONForTest(), "text": "/purge",
			"reply_to_message": map[string]any{"message_id": 5000 - back, "date": 1000, "chat": map[string]any{"id": chat, "type": "supergroup"}, "from": bob.JSONForTest(), "text": "x"},
		}})
	}
	send(1500)
	want(t, lastText(t, e), "That is too many messages: I clear at most 1000 at a time. Reply to a newer message.")
	if len(e.Fake.Calls("deleteMessages")) != 0 {
		t.Fatal("nothing should be deleted")
	}
	send(250)
	calls := e.Fake.Calls("deleteMessages")
	if len(calls) != 3 { // 251 messages: 100 + 100 + 51
		t.Fatalf("batches: %d", len(calls))
	}
	contains(t, lastText(t, e), "up to 251 messages deleted.")

	e.Fake.Reset()
	e.Raw(map[string]any{"message": map[string]any{
		"message_id": 5000, "date": 1000, "chat": map[string]any{"id": chat, "type": "supergroup", "title": "Test group"},
		"from": alice.JSONForTest(), "text": "/purge",
		"reply_to_message": map[string]any{"message_id": 5001, "date": 1000, "chat": map[string]any{"id": chat, "type": "supergroup"}, "from": bob.JSONForTest(), "text": "x"},
	}})
	want(t, lastText(t, e), "Reply to a message that came before your command.")

	e.Fake.Reset()
	e.Fake.Fail("deleteMessages", 400, "Bad Request: message can't be deleted")
	send(5)
	contains(t, lastText(t, e), "couldn't delete those messages")
}

// --- admin ----------------------------------------------------------------------------

func TestAdminSwitches(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/staffhelp"})
	contains(t, lastText(t, e), "Help from the bot's staff is <b>on</b> in this chat.")
	e.Send(Msg{Chat: chat, From: alice, Text: "/staffhelp off"})
	want(t, lastText(t, e), "Only the owner of this chat can change this.")
	e.Send(Msg{Chat: chat, From: owner, Text: "/staffhelp off"})
	contains(t, lastText(t, e), "Help from the bot's staff is now <b>off</b>.")
	e.Send(Msg{Chat: chat, From: alice, Text: "/staffhelp"})
	contains(t, lastText(t, e), "<b>off</b> in this chat")
	e.Send(Msg{Chat: chat, From: owner, Text: "/staffhelp maybe"})
	contains(t, lastText(t, e), "Use /staffhelp on or /staffhelp off")

	e.Send(Msg{Chat: chat, From: owner, Text: "/anonadmin on"})
	contains(t, lastText(t, e), "Anonymous admins can now use all commands")
	e.Send(Msg{Chat: chat, From: alice, Text: "/anonadmin"})
	contains(t, lastText(t, e), "<b>on</b>")
	e.Send(Msg{Chat: chat, From: owner, Text: "/adminerror off"})
	contains(t, lastText(t, e), "now <b>off</b>")
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: bob, Text: "/gtitle New"}) // not told it is an admin command any more
	nothing(t, e)
}

func TestLink(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/link"})
	want(t, lastText(t, e), "Invite link: https://t.me/+exported")

	e.Fake.SetChat(itoa(chat), map[string]any{"id": chat, "type": "supergroup", "title": "Test group", "invite_link": "https://t.me/+primary"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/link"})
	want(t, lastText(t, e), "Invite link: https://t.me/+primary")

	e.Send(Msg{Chat: chat, From: alice, Text: "/link count 5"})
	contains(t, lastText(t, e), "Invite link for 5 people")
	contains(t, lastText(t, e), "https://t.me/+created")
	call := e.Fake.Calls("createChatInviteLink")
	if call[0].P["member_limit"] != "5" || call[0].P["name"] != "By Alice" {
		t.Errorf("create call: %+v", call[0].P)
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/link 1"})
	contains(t, lastText(t, e), "Invite link for 1 person")
	e.Send(Msg{Chat: chat, From: alice, Text: "/link request"})
	contains(t, lastText(t, e), "Invite link that requires approval to join")
	if c := e.Fake.Calls("createChatInviteLink"); c[2].P["creates_join_request"] != "true" {
		t.Errorf("join request: %+v", c[2].P)
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/link banana"})
	contains(t, lastText(t, e), "Use /link")
	e.Send(Msg{Chat: chat, From: alice, Text: "/link 100000"})
	contains(t, lastText(t, e), "Use /link")
}

func TestGtitleAndAdminlist(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/gtitle Brand <new>"})
	if c := e.Fake.Calls("setChatTitle"); len(c) != 1 || c[0].P["title"] != "Brand <new>" {
		t.Fatalf("setChatTitle: %+v", c)
	}
	contains(t, lastText(t, e), "The chat title was changed to <b>Brand &lt;new&gt;</b> by ")
	e.Send(Msg{Chat: chat, From: alice, Text: "/gtitle"})
	contains(t, lastText(t, e), "Give the new title")
	e.Send(Msg{Chat: chat, From: alice, Text: "/gtitle " + strings.Repeat("x", 129)})
	contains(t, lastText(t, e), "at most 128")

	e.Fake.SetMember(chat, alice.ID, alice.Name, "administrator", map[string]any{"custom_title": "Boss"})
	e.Send(Msg{Chat: chat, From: bob, Text: "/adminlist"})
	got := lastText(t, e)
	contains(t, got, "<b>Admins in Test group:</b>")
	contains(t, got, "<b>Owner</b>\n• "+link(owner))
	contains(t, got, "<b>Admins</b>")
	contains(t, got, "• "+link(alice)+" — Boss")
}

func TestCleanBluetext(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/cleanbluetext"})
	contains(t, lastText(t, e), "Commands are <b>kept</b>.")
	e.Send(Msg{Chat: chat, From: alice, Text: "/cleanbluetext on"})
	want(t, lastText(t, e), "Commands will be <b>deleted</b> after they run.")
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: alice, Text: "/adminlist"})
	if len(e.Fake.Calls("deleteMessage")) != 1 {
		t.Fatalf("the command should be deleted: %+v", e.Fake.Calls("deleteMessage"))
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "just talking"})
	if len(e.Fake.Calls("deleteMessage")) != 1 {
		t.Fatal("plain messages must stay")
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/cleanbluetext off"})
	want(t, lastText(t, e), "Commands will be <b>kept</b>.")

	// Without the right to delete, the bot says so.
	e.Fake.SetMember(chat, BotID, "Argo", "administrator", nil)
	e.Send(Msg{Chat: chat, From: alice, Text: "/cleanbluetext on"})
	contains(t, lastText(t, e), "I can't delete messages here yet.")
}

func TestPinUnpin(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/pin", Reply: reply(bob, "hi")})
	c := e.Fake.Calls("pinChatMessage")
	if len(c) != 1 || c[0].P["disable_notification"] != "true" {
		t.Fatalf("pin: %+v", c)
	}
	want(t, lastText(t, e), "The message was pinned by "+link(alice)+".")
	e.Send(Msg{Chat: chat, From: alice, Text: "/pin loud", Reply: reply(bob, "hi")})
	c = e.Fake.Calls("pinChatMessage")
	if c[1].P["disable_notification"] != "" {
		t.Fatalf("loud pin: %+v", c[1].P)
	}
	contains(t, lastText(t, e), "and everyone was notified.")
	e.Send(Msg{Chat: chat, From: alice, Text: "/pin shout", Reply: reply(bob, "hi")})
	contains(t, lastText(t, e), "Use /pin as a reply")
	e.Send(Msg{Chat: chat, From: alice, Text: "/pin"})
	contains(t, lastText(t, e), "Reply to the message")

	e.Send(Msg{Chat: chat, From: alice, Text: "/unpin", Reply: reply(bob, "hi")})
	if u := e.Fake.Calls("unpinChatMessage"); len(u) != 1 || u[0].P["message_id"] == "" {
		t.Fatalf("unpin: %+v", u)
	}
	contains(t, lastText(t, e), "The message was unpinned by")
	e.Send(Msg{Chat: chat, From: alice, Text: "/unpin"})
	contains(t, lastText(t, e), "The most recent pinned message was unpinned by")

	e.Fake.Fail("unpinChatMessage", 400, "Bad Request: message to unpin not found")
	e.Send(Msg{Chat: chat, From: alice, Text: "/unpin"})
	want(t, lastText(t, e), "That message isn't pinned.")
	e.Fake.Fail("pinChatMessage", 400, "Bad Request: not enough rights to pin a message")
	e.Send(Msg{Chat: chat, From: alice, Text: "/pin", Reply: reply(bob, "hi")})
	contains(t, lastText(t, e), "pin")
}

func TestUnpinAll(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/unpinall"})
	contains(t, lastText(t, e), "Unpin <b>every</b> pinned message of this chat?")
	contains(t, e.Fake.Calls("sendMessage")[0].P["reply_markup"], "unpinall:yes")

	e.Press(chat, bob, "unpinall:yes") // not an admin: refused, nothing happens
	if len(e.Fake.Calls("unpinAllChatMessages")) != 0 {
		t.Fatal("a member must not unpin everything")
	}
	e.Press(chat, alice, "unpinall:no")
	if edits := e.Fake.Calls("editMessageText"); len(edits) == 0 || !strings.Contains(edits[len(edits)-1].P["text"], "Cancelled. Nothing was unpinned.") {
		t.Fatalf("edits: %+v", edits)
	}
	e.Press(chat, alice, "unpinall:yes")
	if len(e.Fake.Calls("unpinAllChatMessages")) != 1 {
		t.Fatal("unpinAllChatMessages")
	}
	edits := e.Fake.Calls("editMessageText")
	contains(t, edits[len(edits)-1].P["text"], "Every pinned message was unpinned by ")
}

// --- promote ---------------------------------------------------------------------------

func TestPromoteDemote(t *testing.T) {
	e := setup2(t)
	// The owner of the chat may add admins; Alice (an admin without that right) may not.
	e.Send(Msg{Chat: chat, From: alice, Text: "/promote", Reply: reply(bob, "x")})
	want(t, lastText(t, e), "You need the right to add new admins to use this command.")

	e.Send(Msg{Chat: chat, From: owner, Text: "/promote Chief Boss", Reply: reply(bob, "x")})
	p := e.Fake.Calls("promoteChatMember")
	if len(p) != 1 || p[0].P["user_id"] != "20" || p[0].P["can_delete_messages"] != "true" || p[0].P["can_promote_members"] != "" {
		t.Fatalf("promote: %+v", p)
	}
	if ti := e.Fake.Calls("setChatAdministratorCustomTitle"); len(ti) != 1 || ti[0].P["custom_title"] != "Chief Boss" {
		t.Fatalf("title: %+v", ti)
	}
	want(t, lastText(t, e), link(bob)+" was promoted to admin by "+link(owner)+"\nTitle: <b>Chief Boss</b>")

	e.Send(Msg{Chat: chat, From: owner, Text: "/fullpromote", Reply: reply(carol, "x")})
	p = e.Fake.Calls("promoteChatMember")
	if p[1].P["can_promote_members"] != "true" {
		t.Fatalf("full promote: %+v", p[1].P)
	}
	contains(t, lastText(t, e), "to admin with full rights")

	e.Send(Msg{Chat: chat, From: owner, Text: "/promote " + strings.Repeat("a", 17), Reply: reply(dave, "x")})
	contains(t, lastText(t, e), "at most 16")
	e.Send(Msg{Chat: chat, From: owner, Text: "/promote", Reply: reply(owner, "x")})
	contains(t, lastText(t, e), "owner")

	// A bot that can't add admins can't promote.
	e.Fake.SetMember(chat, BotID, "Argo", "administrator", map[string]any{"can_delete_messages": true})
	e.Send(Msg{Chat: chat, From: owner, Text: "/promote", Reply: reply(dave, "x")})
	want(t, lastText(t, e), "I need the admin right to add admins for that.")
	e.Fake.SetMember(chat, BotID, "Argo", "administrator", allRights)

	e.Fake.SetMember(chat, bob.ID, bob.Name, "administrator", map[string]any{"can_delete_messages": true})
	e.Send(Msg{Chat: chat, From: owner, Text: "/demote", Reply: reply(bob, "x")})
	if len(e.Fake.Calls("promoteChatMember")) != 3 {
		t.Fatal("demote should call promoteChatMember")
	}
	want(t, lastText(t, e), link(bob)+" was demoted by "+link(owner))
	e.Send(Msg{Chat: chat, From: owner, Text: "/demote", Reply: reply(dave, "x")})
	want(t, lastText(t, e), "That user isn't an admin.")
}

func TestStaffPromote(t *testing.T) {
	e := setup2(t)
	if _, err := e.DB.Exec(e.Ctx(), "INSERT INTO roles (user_id, role) VALUES ($1, 2)", sudo.ID); err != nil {
		t.Fatal(err)
	}
	e.Send(Msg{Chat: chat, From: sudo, Text: "/promote", Reply: reply(bob, "x")})
	if len(e.Fake.Calls("promoteChatMember")) != 1 {
		t.Fatal("the bot's staff may promote")
	}
	contains(t, lastText(t, e), "<i>Done by the bot's staff.</i>")
	e.Fake.Reset()
	if err := e.App.SetSetting(e.Ctx(), "staff_help", chat, false); err != nil {
		t.Fatal(err)
	}
	e.Send(Msg{Chat: chat, From: sudo, Text: "/promote", Reply: reply(carol, "x")})
	if len(e.Fake.Calls("promoteChatMember")) != 0 {
		t.Fatal("staff help is off")
	}
}

func TestTitleAndTag(t *testing.T) {
	e := setup2(t)
	e.Fake.SetMember(chat, bob.ID, bob.Name, "administrator", map[string]any{"can_delete_messages": true})
	// An admin sets their own title: /title Boss
	e.Send(Msg{Chat: chat, From: bob, Text: "/title Boss"})
	if c := e.Fake.Calls("setChatAdministratorCustomTitle"); len(c) != 1 || c[0].P["user_id"] != "20" || c[0].P["custom_title"] != "Boss" {
		t.Fatalf("title: %+v", c)
	}
	want(t, lastText(t, e), link(bob)+" was given the title <b>Boss</b> by "+link(bob))
	e.Send(Msg{Chat: chat, From: bob, Text: "/title"})
	contains(t, lastText(t, e), "was cleared")
	// A member can't have a title.
	e.Send(Msg{Chat: chat, From: carol, Text: "/title Hi"})
	contains(t, lastText(t, e), "Only admins can have a title")
	// Someone else's title needs the right to add admins.
	e.Send(Msg{Chat: chat, From: bob, Text: "/title Nope", Reply: reply(alice, "x")})
	contains(t, lastText(t, e), "right to add new admins")
	e.Send(Msg{Chat: chat, From: owner, Text: "/title Deputy", Reply: reply(alice, "x")})
	contains(t, lastText(t, e), "was given the title <b>Deputy</b>")
	e.Fake.Fail("setChatAdministratorCustomTitle", 400, "Bad Request: ADMIN_RANK_EMOJI_NOT_ALLOWED")
	e.Send(Msg{Chat: chat, From: owner, Text: "/title 😀", Reply: reply(alice, "x")})
	contains(t, lastText(t, e), "emoji")

	// Tags belong to members; admins have titles.
	e.Send(Msg{Chat: chat, From: owner, Text: "/tag Vip", Reply: reply(carol, "x")})
	if c := e.Fake.Calls("setChatMemberTag"); len(c) != 1 || c[0].P["tag"] != "Vip" || c[0].P["user_id"] != "30" {
		t.Fatalf("tag: %+v", c)
	}
	want(t, lastText(t, e), link(carol)+" was given the tag <b>Vip</b> by "+link(owner))
	e.Send(Msg{Chat: chat, From: owner, Text: "/tag Vip", Reply: reply(alice, "x")})
	want(t, lastText(t, e), "Admins have a title instead. Use /title.")
	e.Send(Msg{Chat: chat, From: carol, Text: "/tag Mine"}) // the chat does not let members edit tags
	contains(t, lastText(t, e), "doesn't let members")
	e.Send(Msg{Chat: chat, From: carol, Text: "/tag Mine", Reply: reply(bob, "x")})
	contains(t, lastText(t, e), "right to manage")
}

// --- root -----------------------------------------------------------------------------------

func TestPing(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: bob, Text: "/ping"})
	want(t, lastText(t, e), "<b>Pong!</b>")
	ed := e.Fake.Calls("editMessageText")
	if len(ed) != 1 || !strings.HasPrefix(ed[0].P["text"], "<b>Pong!</b>\nLatency: <code>") {
		t.Fatalf("edit: %+v", ed)
	}
}

func TestRootIsEnglishOnlyAndForStaff(t *testing.T) {
	e := setup2(t)
	if _, err := e.DB.Exec(e.Ctx(), "INSERT INTO chat_settings (chat_id, language) VALUES ($1, 'pl')", chat); err != nil {
		t.Fatal(err)
	}
	e.Send(Msg{Chat: chat, From: bob, Text: "/modules"}) // nobody but Developers answers
	nothing(t, e)
	e.Send(Msg{Chat: chat, From: root, Text: "/module"})
	want(t, lastText(t, e), "Usage: /module &lt;enable|disable&gt; &lt;name&gt;")
}

func TestModulesCommand(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: root, Text: "/modules"})
	got := lastText(t, e)
	contains(t, got, "<b>Modules</b>")
	contains(t, got, "<code>root</code> - <b>enabled</b> <i>(protected)</i>")
	contains(t, got, "<i>Description: Chat rules</i>")

	e.Send(Msg{Chat: chat, From: root, Text: "/module disable rules"})
	want(t, lastText(t, e), "Module <code>rules</code> disabled.")
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: alice, Text: "/setrules x"})
	nothing(t, e)
	e.Send(Msg{Chat: chat, From: root, Text: "/module enable RULES"})
	contains(t, lastText(t, e), "Module <code>rules</code>")
	e.Send(Msg{Chat: chat, From: alice, Text: "/setrules x"})
	contains(t, lastText(t, e), "rules")
	e.Send(Msg{Chat: chat, From: root, Text: "/module disable root"})
	want(t, lastText(t, e), "Module <code>root</code> is protected and can't be disabled.")
	e.Send(Msg{Chat: chat, From: root, Text: "/module disable nope"})
	want(t, lastText(t, e), "Module <code>nope</code> not found.")
}

func TestModuleSwitchSurvivesRestart(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: root, Text: "/module disable notes"})
	var off bool
	if err := e.DB.Row(e.Ctx(), "SELECT NOT enabled FROM modules WHERE name = 'notes'", nil, &off); err != nil || !off {
		t.Fatalf("stored: %v %v", off, err)
	}
	if err := e.App.LoadModuleState(e.Ctx()); err != nil {
		t.Fatal(err)
	}
	if m := e.App.Module("notes"); m == nil || m.Enabled() {
		t.Fatal("notes should stay disabled after loading the state again")
	}
}

func TestModuleDisableConnectionEndsThem(t *testing.T) {
	e := setup2(t)
	e.Send(private(alice, "/connect -100500"))
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: root, Text: "/module disable connection"})
	texts := sent(e)
	if len(texts) != 2 {
		t.Fatalf("sent: %q", texts)
	}
	contains(t, texts[0], "Your connection to <b>Test group</b> has ended")
	want(t, texts[1], "Module <code>connection</code> disabled.\n1 connection ended.")
}

func TestStats(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: bob, Text: "hello"})
	e.Send(Msg{Chat: chat, From: root, Text: "/stats"})
	got := lastText(t, e)
	contains(t, got, "<b>Bot Statistics:</b>")
	contains(t, got, "• <b>Chats:</b> <code>1</code> (groups: <code>1</code>, channels: <code>0</code>)")
	contains(t, got, "• <b>Global Bans:</b> <code>0</code>")
	contains(t, got, "• <b>Staff:</b> <code>1</code> (including the owner)")
}

func TestStatus(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: root, Text: "/status"})
	got := lastText(t, e)
	contains(t, got, "<b>Bot Status:</b>")
	contains(t, got, "• <b>Telegram:</b> <code>")
	contains(t, got, "• <b>Database:</b> <code>")
	contains(t, got, "• <b>Go:</b> <code>go")
	contains(t, got, "• <b>MTProto:</b> <code>gotd/td ")
	contains(t, got, "(layer ")
}

func TestLeave(t *testing.T) {
	e := setup2(t)
	e.Send(private(root, "/leave"))
	want(t, lastText(t, e), "I can only leave groups and channels.")
	e.Send(Msg{Chat: chat, From: bob, Text: "hi"}) // the chat is recorded
	e.Send(Msg{Chat: chat, From: root, Text: "/leave"})
	want(t, lastText(t, e), "Farewell! My duties here are finished. 🫡")
	if len(e.Fake.Calls("leaveChat")) != 1 {
		t.Fatal("leaveChat")
	}
	var n int
	if err := e.DB.Row(e.Ctx(), "SELECT count(*) FROM chats WHERE chat_id = $1", []any{chat}, &n); err != nil || n != 0 {
		t.Fatalf("the chat should be forgotten: %d %v", n, err)
	}
}

func TestCleanChats(t *testing.T) {
	e := setup2(t)
	gone := int64(-100999)
	e.Send(Msg{Chat: chat, From: bob, Text: "hi"})
	e.Send(Msg{Chat: gone, From: bob, Text: "hi"})
	e.Fake.Reset()
	e.Fake.Fail("getChatMember", 403, "Forbidden: bot was kicked from the supergroup chat") // the first stored chat in order
	e.Send(Msg{Chat: chat, From: root, Text: "/cleanchats"})
	got := e.Fake.Calls("editMessageText")
	if len(got) == 0 {
		t.Fatalf("no report: %q", sent(e))
	}
	report := got[len(got)-1].P["text"]
	contains(t, report, "<b>Chat cleanup finished</b>")
	contains(t, report, "Checked: <code>2</code>")
	contains(t, report, "Removed: <code>1</code>")
}

func TestSpeedtest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/__down"):
			var n int
			fmt.Sscan(r.URL.Query().Get("bytes"), &n)
			w.Write(make([]byte, n))
		case strings.HasSuffix(r.URL.Path, "/__up"):
			buf := make([]byte, 1<<16)
			for {
				if _, err := r.Body.Read(buf); err != nil {
					break
				}
			}
		}
	}))
	defer srv.Close()
	old := modules.SpeedtestBase
	modules.SpeedtestBase = srv.URL
	defer func() { modules.SpeedtestBase = old }()

	e := setup2(t)
	e.Send(Msg{Chat: chat, From: root, Text: "/speedtest"})
	want(t, lastText(t, e), "Running a speedtest, this takes a few seconds...")
	ed := e.Fake.Calls("editMessageText")
	if len(ed) != 1 || !strings.HasPrefix(ed[0].P["text"], "<b>Speedtest</b>\n\n• <b>Ping:</b> <code>") {
		t.Fatalf("edit: %+v", ed)
	}

	modules.SpeedtestBase = "http://127.0.0.1:1"
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: root, Text: "/speedtest"})
	ed = e.Fake.Calls("editMessageText")
	if len(ed) != 1 || !strings.HasPrefix(ed[0].P["text"], "<b>Speedtest failed:</b>") {
		t.Fatalf("failure: %+v", ed)
	}
}

func TestStaffRoles(t *testing.T) {
	e := setup2(t)
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: root, Text: "/adddev", Reply: reply(dave, "x")})
	answered(t, e, link(dave)+" has been promoted to <b>Developer</b>.")
	toldDave := false
	for _, c := range e.Fake.Calls("sendMessage") {
		if c.P["chat_id"] == itoa(dave.ID) && strings.Contains(c.P["text"], "You have been promoted to") && strings.Contains(c.P["text"], "Developer") &&
			strings.Contains(c.P["text"], "Timestamp:") && strings.Contains(c.P["text"], "Admin:") {
			toldDave = true
		}
	}
	if !toldDave {
		t.Fatal("the promoted person is told in a private message")
	}
	if r := e.App.GetRole(e.Ctx(), dave.ID); r.String() != "Developer" {
		t.Fatalf("role: %v", r)
	}
	e.Send(Msg{Chat: chat, From: root, Text: "/adddev", Reply: reply(dave, "x")})
	want(t, lastText(t, e), link(dave)+" is already Developer.")
	e.Send(Msg{Chat: chat, From: root, Text: "/addsudo", Reply: reply(dave, "x")})
	want(t, lastText(t, e), link(dave)+" is already Developer. Remove that role first.")

	// Developers manage Sudo and Support, not Developers.
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: dave, Text: "/addsudo", Reply: reply(bob, "x")})
	answered(t, e, link(bob)+" has been promoted to <b>Sudo</b>.")
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: dave, Text: "/addsupport", Reply: reply(carol, "x")})
	answered(t, e, "<b>Support</b>")
	e.Send(Msg{Chat: chat, From: dave, Text: "/adddev", Reply: reply(alice, "x")})
	want(t, lastText(t, e), "You don't have permission to manage Developers.")
	e.Send(Msg{Chat: chat, From: dave, Text: "/addsudo", Reply: reply(dave, "x")})
	want(t, lastText(t, e), "You can't change your own role.")
	e.Send(Msg{Chat: chat, From: dave, Text: "/addsudo", Reply: reply(root, "x")})
	want(t, lastText(t, e), "The owner's role can't be changed.")
	e.Send(Msg{Chat: chat, From: dave, Text: "/rmsupport", Reply: reply(bob, "x")})
	want(t, lastText(t, e), link(bob)+" is not Support.")
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: dave, Text: "/rmsudo", Reply: reply(bob, "x")})
	answered(t, e, link(bob)+" has been removed from <b>Sudo</b>.")
	if r := e.App.GetRole(e.Ctx(), bob.ID); r != 0 {
		t.Fatalf("role after removal: %v", r)
	}
	// Support (carol) may look but not change.
	e.Send(Msg{Chat: chat, From: carol, Text: "/addsupport", Reply: reply(bob, "x")})
	want(t, lastText(t, e), "You don't have permission to manage Supports.")
	// Everyone else is not answered at all.
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: alice, Text: "/addsupport", Reply: reply(bob, "x")})
	nothing(t, e)
}

func TestStaffRoleLog(t *testing.T) {
	e := setup2(t)
	e.Fake.SetChat("-100777", map[string]any{"id": logCh, "type": "supergroup"})
	e.App.Cfg.LogChatID = logCh
	e.Send(Msg{Chat: chat, From: root, Text: "/adddev", Reply: reply(dave, "x")})
	var found bool
	for _, c := range e.Fake.Calls("sendMessage") {
		if c.P["chat_id"] == "-100777" && strings.Contains(c.P["text"], "#DEVELOPER") && strings.Contains(c.P["text"], dave.Name) {
			found = true
		}
	}
	if !found {
		t.Fatalf("the role log is missing: %q", sent(e))
	}
}

func TestStaffList(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: root, Text: "/addsudo", Reply: reply(dave, "x")})
	e.Send(Msg{Chat: chat, From: root, Text: "/staff"})
	want(t, lastText(t, e), "<b>Staff</b>\n\nChoose a role to see who holds it.")
	markup := e.Fake.Calls("sendMessage")[len(e.Fake.Calls("sendMessage"))-1].P["reply_markup"]
	for _, data := range []string{"staff:role:4", "staff:role:3", "staff:role:2", "staff:role:1"} {
		contains(t, markup, data)
	}
	e.Press(chat, root, "staff:role:2")
	ed := e.Fake.Calls("editMessageText")
	got := ed[len(ed)-1].P["text"]
	contains(t, got, "<b>Sudos</b> (1)")
	contains(t, got, "1. "+link(dave))
	contains(t, got, "Added: ")
	contains(t, got, "By: "+link(root))
	e.Press(chat, root, "staff:role:3")
	ed = e.Fake.Calls("editMessageText")
	want(t, ed[len(ed)-1].P["text"], "<b>Developers</b>\n\nNobody holds this role yet.")
	e.Press(chat, root, "staff:role:4")
	ed = e.Fake.Calls("editMessageText")
	contains(t, ed[len(ed)-1].P["text"], "<b>Owner</b> (1)")
	e.Press(chat, root, "staff:home")
	ed = e.Fake.Calls("editMessageText")
	contains(t, ed[len(ed)-1].P["text"], "Choose a role")
	n := len(e.Fake.Calls("editMessageText"))
	e.Press(chat, bob, "staff:role:2") // not staff: nothing
	if len(e.Fake.Calls("editMessageText")) != n {
		t.Fatal("a regular user must not see the staff")
	}
	e.Press(chat, root, "staff:role:9")
	if a := e.Fake.Calls("answerCallbackQuery"); !strings.Contains(a[len(a)-1].P["text"], "Unknown role") {
		t.Fatalf("answer: %+v", a[len(a)-1].P)
	}
}

func TestWhitelist(t *testing.T) {
	e := setup2(t)
	e.Fake.SetChat("-100777", map[string]any{"id": logCh, "type": "supergroup"})
	e.App.Cfg.LogChatID = logCh
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: root, Text: "/whitelist", Reply: reply(bob, "x")})
	answered(t, e, link(bob)+" has been whitelisted.")
	e.Send(Msg{Chat: chat, From: root, Text: "/whitelist", Reply: reply(bob, "x")})
	want(t, lastText(t, e), link(bob)+" is already whitelisted.")
	if ok, _ := e.App.IsProtected(e.Ctx(), bob.ID); !ok {
		t.Fatal("bob should be protected")
	}
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: root, Text: "/unwhitelist", Reply: reply(bob, "x")})
	answered(t, e, link(bob)+" has been removed from the whitelist.")
	e.Send(Msg{Chat: chat, From: root, Text: "/unwhitelist", Reply: reply(bob, "x")})
	want(t, lastText(t, e), link(bob)+" is not whitelisted.")
	e.Send(Msg{Chat: chat, From: root, Text: "/whitelist", Reply: reply(root, "x")})
	want(t, lastText(t, e), "You can't change your own whitelist status.")
	e.Send(Msg{Chat: chat, From: root, Text: "/whitelist 777000"})
	contains(t, lastText(t, e), "is immune to all bot actions")
	// Support is below Sudo: not answered.
	if _, err := e.DB.Exec(e.Ctx(), "INSERT INTO roles (user_id, role) VALUES ($1, 1)", dave.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.App.SetRole(e.Ctx(), dave.ID, 1, 0, "", ""); err != nil {
		t.Fatal(err)
	}
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: dave, Text: "/whitelist", Reply: reply(bob, "x")})
	nothing(t, e)
}

func TestPinned(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: dave, Text: "/pinned"})
	want(t, lastText(t, e), "There is no pinned message in this chat.")
	e.Fake.SetChat(itoa(chat), map[string]any{"id": chat, "type": "supergroup", "title": "Test group",
		"pinned_message": map[string]any{"message_id": 55, "date": 1, "chat": map[string]any{"id": chat, "type": "supergroup", "title": "Test group"}}})
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: dave, Text: "/pinned"})
	c := e.Fake.Calls("sendMessage")
	if len(c) != 1 || !strings.Contains(c[0].P["reply_parameters"], `"message_id":55`) || !strings.Contains(c[0].P["reply_markup"], "/55") {
		t.Fatalf("/pinned should answer to the pinned message with a link: %+v", c)
	}
}

func TestPermapin(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/permapin"})
	contains(t, lastText(t, e), "Use /permapin")
	e.Send(Msg{Chat: dave.ID, Type: "private", From: dave, Text: "/permapin hi"})
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: dave, Text: "/permapin hello"})
	if len(e.Fake.Calls("pinChatMessage")) != 0 {
		t.Fatal("a member can't use /permapin")
	}
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: alice, Text: "/permapin <b>Rules</b> be nice"})
	s := e.Fake.Calls("sendMessage")
	if len(s) != 1 || !strings.Contains(s[0].P["text"], "<b>Rules</b> be nice") || s[0].P["reply_parameters"] != "" {
		t.Fatalf("the message is sent on its own: %+v", s)
	}
	if p := e.Fake.Calls("pinChatMessage"); len(p) != 1 || p[0].P["disable_notification"] != "true" {
		t.Fatalf("pinned quietly: %+v", p)
	}
	// A replied-to message works too.
	e.Send(Msg{Chat: chat, From: alice, Text: "/permapin", Reply: reply(bob, "the rules")})
	if s = e.Fake.Calls("sendMessage"); len(s) != 2 || !strings.Contains(s[1].P["text"], "the rules") {
		t.Fatalf("replied text: %+v", s)
	}
}

func TestLinkedChannelSwitches(t *testing.T) {
	e := setup2(t)
	for _, cmd := range []string{"antichannelpin", "cleanlinked"} {
		e.Send(Msg{Chat: chat, From: alice, Text: "/" + cmd})
		contains(t, lastText(t, e), "currently <b>off</b>")
		e.Send(Msg{Chat: chat, From: alice, Text: "/" + cmd + " maybe"})
		contains(t, lastText(t, e), "Use /"+cmd+" on")
		e.Send(Msg{Chat: chat, From: alice, Text: "/" + cmd + " on"})
		e.Send(Msg{Chat: chat, From: alice, Text: "/" + cmd})
		contains(t, lastText(t, e), "currently <b>on</b>")
	}
	e.Send(Msg{Chat: dave.ID, Type: "private", From: dave, Text: "/cleanlinked on"})
	contains(t, lastText(t, e), "supergroup")

	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: bob, Text: "post of the channel", Extra: map[string]any{"is_automatic_forward": true}})
	if len(e.Fake.Calls("deleteMessage")) != 1 {
		t.Fatalf("the forwarded post should be deleted: %+v", e.Fake.Calls("deleteMessage"))
	}
	e.Send(Msg{Chat: chat, From: bob, Text: "a normal message"})
	if len(e.Fake.Calls("deleteMessage")) != 1 {
		t.Fatal("ordinary messages stay")
	}
	e.Send(Msg{Chat: chat, From: bob, Extra: map[string]any{"pinned_message": map[string]any{
		"message_id": 9, "date": 1, "is_automatic_forward": true, "chat": map[string]any{"id": chat, "type": "supergroup"}}}})
	if u := e.Fake.Calls("unpinChatMessage"); len(u) != 1 || u[0].P["message_id"] != "9" {
		t.Fatalf("the automatic pin should be undone: %+v", u)
	}
	e.Send(Msg{Chat: chat, From: bob, Extra: map[string]any{"pinned_message": map[string]any{
		"message_id": 10, "date": 1, "chat": map[string]any{"id": chat, "type": "supergroup"}}}})
	if len(e.Fake.Calls("unpinChatMessage")) != 1 {
		t.Fatal("a pin by a person stays")
	}
}
