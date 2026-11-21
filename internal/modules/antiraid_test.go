package modules_test

import (
	"testing"
	"time"

	"argo/internal/core"

	. "argo/internal/testutil"
)

func joinMsg(who ...Person) Msg {
	var members []map[string]any
	for _, p := range who {
		members = append(members, map[string]any{"id": p.ID, "is_bot": false, "first_name": p.Name})
	}
	return Msg{Chat: chat, From: who[0], Extra: map[string]any{"new_chat_members": members}}
}

func newcomer(id int64) Person { return Person{ID: id, Name: "New" + itoa(id)} }

func TestAntiraidCommands(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: bob, Text: "/antiraid"})
	contains(t, lastText(t, e), "Only chat admins")
	e.Send(Msg{Chat: chat, From: alice, Text: "/antiraid"})
	contains(t, lastText(t, e), "<b>Enabled:</b>")
	e.Send(Msg{Chat: chat, From: alice, Text: "/antiraid maybe"})
	contains(t, lastText(t, e), "Use /antiraid")
	e.Send(Msg{Chat: chat, From: alice, Text: "/raidlimit 2 30s"})
	contains(t, lastText(t, e), "Use /raidlimit")
	e.Send(Msg{Chat: chat, From: alice, Text: "/raidlimit 5 5s"})
	contains(t, lastText(t, e), "Use /raidlimit")
	e.Send(Msg{Chat: chat, From: alice, Text: "/raidlimit 5 30s"})
	contains(t, lastText(t, e), "5")
	e.Send(Msg{Chat: chat, From: alice, Text: "/raidtime 30s"})
	contains(t, lastText(t, e), "Use /raidtime")
	e.Send(Msg{Chat: chat, From: alice, Text: "/raidtime 2h"})
	contains(t, lastText(t, e), "2 hours")
	e.Send(Msg{Chat: chat, From: alice, Text: "/raidmode shout"})
	contains(t, lastText(t, e), "Use /raidmode")
	e.Send(Msg{Chat: chat, From: alice, Text: "/raidmode ban"})
	contains(t, lastText(t, e), "banned")
	e.Send(Msg{Chat: chat, From: alice, Text: "/antiraid on"})
	got := lastText(t, e)
	contains(t, got, "<code>on</code>")
	contains(t, got, "5")
	e.Send(Msg{Chat: chat, From: alice, Text: "/antiraid off"})
	contains(t, lastText(t, e), "<code>off</code>")
}

func TestAntiraidDetectsAndDeals(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/raidlimit 3 60s"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/antiraid on"})
	e.Fake.Reset()
	// off the radar: two joins, the same person twice counts once
	e.Send(joinMsg(newcomer(101)))
	e.Send(joinMsg(newcomer(101)))
	e.Send(joinMsg(newcomer(102)))
	if len(e.Fake.Calls("restrictChatMember")) != 0 {
		t.Fatal("not a raid yet")
	}
	e.Send(joinMsg(newcomer(103)))
	calls := e.Fake.Calls("restrictChatMember")
	if len(calls) != 3 {
		t.Fatalf("the three who made the raid are muted: %+v", calls)
	}
	contains(t, lastText(t, e), "Raid detected")
	if !e.App.RaidWasRemoved(chat, 103) {
		t.Fatal("the others can tell")
	}
	// during the raid every newcomer is dealt with, quietly
	e.Fake.Reset()
	e.Send(joinMsg(newcomer(104)))
	if c := e.Fake.Calls("restrictChatMember"); len(c) != 1 || c[0].P["user_id"] != "104" {
		t.Fatalf("raid newcomer: %+v", c)
	}
	if len(e.Fake.Calls("sendMessage")) != 0 {
		t.Fatal("only the start is announced")
	}
	// admins, approved people and bots are not touched; ending the antiraid ends the raid
	e.Fake.Reset()
	e.Send(joinMsg(alice))
	e.Send(Msg{Chat: chat, From: bob, Extra: map[string]any{"new_chat_members": []map[string]any{{"id": 900, "is_bot": true, "first_name": "Bot"}}}})
	if len(e.Fake.Calls("restrictChatMember")) != 0 {
		t.Fatal("exempt joins")
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/antiraid off"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/antiraid on"})
	e.Fake.Reset()
	e.Send(joinMsg(newcomer(105)))
	if len(e.Fake.Calls("restrictChatMember")) != 0 {
		t.Fatal("the raid is over")
	}
}

func TestAntiraidKickMode(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/raidlimit 3 60s"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/raidmode kick"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/antiraid on"})
	e.Fake.Reset()
	e.Send(joinMsg(newcomer(201), newcomer(202), newcomer(203)))
	if n := len(e.Fake.Calls("banChatMember")); n != 3 {
		t.Fatalf("three kicked: %d", n)
	}
	if n := len(e.Fake.Calls("unbanChatMember")); n != 3 {
		t.Fatalf("and let back in: %d", n)
	}
}

func TestAntiraidMemberUpdateAndFailure(t *testing.T) {
	core.MemberWait = 0 // the greetings module waits for a service message that will not come
	t.Cleanup(func() { core.MemberWait = 3 * time.Second })
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/raidlimit 3 60s"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/antiraid on"})
	e.Fake.Reset()
	update := func(p Person) {
		e.Raw(map[string]any{"chat_member": map[string]any{
			"chat": map[string]any{"id": chat, "type": "supergroup", "title": "Test group"}, "from": user(p), "date": 1000,
			"old_chat_member": map[string]any{"status": "left", "user": user(p)},
			"new_chat_member": map[string]any{"status": "member", "user": user(p)},
		}})
	}
	update(newcomer(301))
	update(newcomer(302))
	if len(e.Fake.Calls("restrictChatMember")) != 0 {
		t.Fatal("two joins")
	}
	e.Fake.Fail("restrictChatMember", 400, "Bad Request: not enough rights to restrict/unrestrict chat member")
	update(newcomer(303))
	if !said(e, "Antiraid is on, but I can") {
		t.Fatalf("complained: %q", sent(e))
	}
}

func user(p Person) map[string]any {
	return map[string]any{"id": p.ID, "is_bot": false, "first_name": p.Name}
}
