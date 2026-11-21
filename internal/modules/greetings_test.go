package modules_test

import (
	"strings"
	"testing"
	"time"

	"argo/internal/core"
	. "argo/internal/testutil"
)

func leaveMsg(who Person, by Person) Msg {
	return Msg{Chat: chat, From: by, Extra: map[string]any{"left_chat_member": user(who)}}
}

func memberUpdate(e *Env, p Person, from Person, oldStatus, newStatus string) {
	e.Raw(map[string]any{"chat_member": map[string]any{
		"chat": map[string]any{"id": chat, "type": "supergroup", "title": "Test group"}, "from": user(from), "date": 1000,
		"old_chat_member": map[string]any{"status": oldStatus, "user": user(p)},
		"new_chat_member": map[string]any{"status": newStatus, "user": user(p)},
	}})
}

func TestGreetingCommands(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: bob, Text: "/welcome"})
	contains(t, lastText(t, e), "Only chat admins")
	e.Send(Msg{Chat: chat, From: alice, Text: "/welcome"})
	got := sent(e)
	contains(t, got[len(got)-2], "<b>Welcome</b>")
	contains(t, got[len(got)-2], "<b>Enabled:</b> <code>Yes</code>")
	contains(t, got[len(got)-1], "Welcome to Test group")
	e.Send(Msg{Chat: chat, From: alice, Text: "/welcome maybe"})
	contains(t, lastText(t, e), "Use /welcome")
	e.Send(Msg{Chat: chat, From: alice, Text: "/welcome on"})
	contains(t, lastText(t, e), "already")
	e.Send(Msg{Chat: chat, From: alice, Text: "/welcome off"})
	contains(t, lastText(t, e), "now")
	e.Send(Msg{Chat: chat, From: alice, Text: "/welcome off"})
	contains(t, lastText(t, e), "already")
	e.Send(Msg{Chat: chat, From: alice, Text: "/setwelcome"})
	contains(t, lastText(t, e), "Tell me what")
	e.Send(Msg{Chat: chat, From: alice, Text: "/setwelcome Hi {first}, this is *{chatname}*"})
	contains(t, lastText(t, e), "saved")
	e.Send(Msg{Chat: chat, From: alice, Text: "/welcome noformat"})
	got = sent(e)
	contains(t, got[len(got)-2], "<b>Message:</b> <code>Custom</code>")
	contains(t, got[len(got)-1], "{first}")
	e.Send(Msg{Chat: chat, From: alice, Text: "/resetwelcome"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/welcome"})
	got = sent(e)
	contains(t, got[len(got)-2], "<b>Message:</b> <code>Default</code>")
	for _, c := range []string{"welcomemute", "cleanwelcome", "cleanservice"} {
		e.Send(Msg{Chat: chat, From: alice, Text: "/" + c + " maybe"})
		contains(t, lastText(t, e), "Use /"+c)
		e.Send(Msg{Chat: chat, From: alice, Text: "/" + c + " on"})
		e.Send(Msg{Chat: chat, From: alice, Text: "/" + c + " on"})
		contains(t, lastText(t, e), "already")
		e.Send(Msg{Chat: chat, From: alice, Text: "/" + c})
		contains(t, lastText(t, e), "/"+c)
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/goodbye off"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/setgoodbye Bye {first}"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/resetgoodbye"})
}

func TestWelcomeOnJoin(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/setwelcome Hi {first}, welcome to {chatname}"})
	e.Fake.Reset()
	e.Send(joinMsg(carol))
	contains(t, lastText(t, e), "Hi Carol, welcome to Test group")
	// more than five are not all greeted
	e.Fake.Reset()
	var many []Person
	for i := int64(0); i < 7; i++ {
		many = append(many, Person{ID: 700 + i, Name: "M" + itoa(i)})
	}
	e.Send(joinMsg(many...))
	if n := len(e.Fake.Calls("sendMessage")); n != maxGreetedForTest {
		t.Fatalf("greeted %d", n)
	}
	// welcome off: nobody is greeted
	e.Send(Msg{Chat: chat, From: alice, Text: "/welcome off"})
	e.Fake.Reset()
	e.Send(joinMsg(carol))
	if len(e.Fake.Calls("sendMessage")) != 0 {
		t.Fatal("welcome is off")
	}
	// bots are not greeted
	e.Send(Msg{Chat: chat, From: alice, Text: "/welcome on"})
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: alice, Extra: map[string]any{"new_chat_members": []map[string]any{{"id": 900, "is_bot": true, "first_name": "Bot"}}}})
	if len(e.Fake.Calls("sendMessage")) != 0 {
		t.Fatal("bots are not greeted")
	}
}

const maxGreetedForTest = 5

func TestWelcomeCleaning(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/cleanwelcome on"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/cleanservice on"})
	e.Fake.Reset()
	e.Send(joinMsg(carol))
	if n := len(e.Fake.Calls("deleteMessage")) + len(e.Fake.Calls("deleteMessages")); n != 1 {
		t.Fatalf("the joined message goes: %d", n)
	}
	s, _ := e.App.Greetings(e.Ctx(), chat)
	if s.LastWelcomeID == nil {
		t.Fatal("the welcome is remembered")
	}
	first := *s.LastWelcomeID
	e.Fake.Reset()
	e.Send(joinMsg(dave))
	var gone []string
	for _, c := range e.Fake.Calls("deleteMessage") {
		gone = append(gone, c.P["message_id"])
	}
	found := false
	for _, g := range gone {
		found = found || g == itoa(first)
	}
	if !found {
		t.Fatalf("the previous welcome %d goes: %v", first, gone)
	}
}

func TestWelcomeMute(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/welcomemute on"})
	e.Fake.SetMember(chat, carol.ID, carol.Name, "member", nil)
	e.Fake.Reset()
	e.Send(joinMsg(carol))
	calls := e.Fake.Calls("restrictChatMember")
	if len(calls) != 1 || calls[0].P["user_id"] != "30" || calls[0].P["use_independent_chat_permissions"] != "true" {
		t.Fatalf("restrict: %+v", calls)
	}
	contains(t, calls[0].P["permissions"], `"can_send_messages":true`)
	if strings.Contains(calls[0].P["permissions"], "can_send_photos") {
		t.Fatalf("no media: %s", calls[0].P["permissions"])
	}
	// somebody who is restricted already, or an admin, is left alone
	e.Fake.SetMember(chat, bob.ID, bob.Name, "restricted", map[string]any{"can_send_messages": false})
	e.Fake.Reset()
	e.Send(joinMsg(bob))
	e.Send(joinMsg(alice))
	if len(e.Fake.Calls("restrictChatMember")) != 0 {
		t.Fatal("left alone")
	}
}

func TestGoodbyeAndStaff(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/setgoodbye Bye {first}"})
	e.Fake.Reset()
	e.Send(leaveMsg(carol, carol))
	contains(t, lastText(t, e), "Bye Carol")
	e.Fake.Reset()
	e.Send(leaveMsg(carol, alice)) // removed by an admin: no goodbye
	if len(e.Fake.Calls("sendMessage")) != 0 {
		t.Fatal("no goodbye for a removal")
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/goodbye off"})
	e.Fake.Reset()
	e.Send(leaveMsg(carol, carol))
	if len(e.Fake.Calls("sendMessage")) != 0 {
		t.Fatal("goodbye is off")
	}
	// the owner of the bot is welcomed and seen off with the message of the role, whatever the chat says
	boss := Person{ID: OwnerID, Name: "Boss"}
	e.Send(Msg{Chat: chat, From: alice, Text: "/welcome off"})
	e.Fake.Reset()
	e.Send(joinMsg(boss))
	contains(t, lastText(t, e), "My owner")
	e.Fake.Reset()
	e.Send(leaveMsg(boss, boss))
	contains(t, lastText(t, e), "My owner Boss has left")
}

func TestMemberUpdatesGreet(t *testing.T) {
	core.MemberWait = 0
	t.Cleanup(func() { core.MemberWait = 3 * time.Second })
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/setwelcome Hello {first}"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/setgoodbye See you {first}"})
	e.Fake.Reset()
	// a join only the update tells about
	memberUpdate(e, carol, carol, "left", "member")
	contains(t, lastText(t, e), "Hello Carol")
	// the service message came first: the update does nothing
	e.Fake.Reset()
	e.Send(joinMsg(dave))
	n := len(e.Fake.Calls("sendMessage"))
	memberUpdate(e, dave, dave, "left", "member")
	if len(e.Fake.Calls("sendMessage")) != n {
		t.Fatal("greeted twice")
	}
	// leaving
	e.Fake.Reset()
	memberUpdate(e, carol, carol, "member", "left")
	contains(t, lastText(t, e), "See you Carol")
	// a ban is no goodbye
	e.Fake.Reset()
	memberUpdate(e, bob, alice, "member", "kicked")
	if len(e.Fake.Calls("sendMessage")) != 0 {
		t.Fatal("no goodbye for a ban")
	}
}
