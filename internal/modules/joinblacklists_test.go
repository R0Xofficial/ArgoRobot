package modules_test

import (
	"testing"

	. "argo/internal/testutil"
)

func joinOf(from Person, who ...Person) Msg {
	var members []map[string]any
	for _, p := range who {
		m := map[string]any{"id": p.ID, "is_bot": false, "first_name": p.Name}
		if p.Username != "" {
			m["username"] = p.Username
		}
		members = append(members, m)
	}
	return Msg{Chat: chat, From: from, Extra: map[string]any{"new_chat_members": members}}
}

func TestJoinBlacklistCommands(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: bob, Text: "/blacklistname spam"})
	if len(e.Fake.Calls("banChatMember")) != 0 {
		t.Fatal("unexpected")
	}
	if got, _ := e.App.JoinPatterns(e.Ctx(), chat); len(got) != 0 {
		t.Fatalf("a member added a trigger: %v", got)
	}
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: alice, Text: "/blacklistname"})
	contains(t, lastText(t, e), "one per line")
	e.Send(Msg{Chat: chat, From: alice, Text: "/joinblacklists"})
	contains(t, lastText(t, e), "There are none yet")
	e.Send(Msg{Chat: chat, From: alice, Text: "/blacklistname Buy Crypto\nbuy crypto\n*****a*b*c*d"})
	got := lastText(t, e)
	contains(t, got, "Added 1 name trigger")
	contains(t, got, "<code>buy crypto</code>")
	contains(t, got, "Skipped")
	e.Send(Msg{Chat: chat, From: alice, Text: "/blacklistusername @Scammer"})
	contains(t, lastText(t, e), "<code>scammer</code>")
	e.Send(Msg{Chat: chat, From: alice, Text: "/joinblacklist"})
	got = lastText(t, e)
	contains(t, got, "<b>Names</b>")
	contains(t, got, "<b>Usernames</b>")
	e.Send(Msg{Chat: chat, From: alice, Text: "/unblacklistname buy crypto\nnothing"})
	got = lastText(t, e)
	contains(t, got, "Removed 1 name trigger")
	contains(t, got, "Not in the name list")
	for i := 0; i < 10; i++ {
		e.Send(Msg{Chat: chat, From: alice, Text: "/blacklistbio word" + itoa(int64(i))})
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/blacklistbio one more"})
	contains(t, lastText(t, e), "full")
}

func TestJoinBlacklistEnforcement(t *testing.T) {
	e := setup2(t)
	for _, c := range []string{"/blacklistname free crypto", "/blacklistusername spam*", "/blacklistbio earn money", "/blacklistchannel promo"} {
		e.Send(Msg{Chat: chat, From: alice, Text: c})
	}
	e.Fake.Reset()

	// An innocent newcomer is not touched.
	good := Person{ID: 700, Name: "Nice Person"}
	e.Fake.SetChat("700", map[string]any{"id": 700, "type": "private", "first_name": "Nice Person", "bio": "I like cats"})
	e.Send(joinOf(good, good))
	if len(e.Fake.Calls("banChatMember")) != 0 {
		t.Fatal("innocent person banned")
	}

	// By name (whole words, any case), by username, by bio, by personal channel.
	cases := []struct {
		p    Person
		chat map[string]any
	}{
		{Person{ID: 701, Name: "FREE Crypto Bot"}, nil},
		{Person{ID: 702, Name: "Zed", Username: "SpamKing"}, nil},
		{Person{ID: 703, Name: "Zed"}, map[string]any{"bio": "Easy to Earn Money here"}},
		{Person{ID: 704, Name: "Zed"}, map[string]any{"personal_chat": map[string]any{"id": -100500, "type": "channel", "title": "Big Promo"}}},
	}
	for _, c := range cases {
		e.Fake.Reset()
		obj := map[string]any{"id": c.p.ID, "type": "private", "first_name": c.p.Name}
		for k, v := range c.chat {
			obj[k] = v
		}
		e.Fake.SetChat(itoa(c.p.ID), obj)
		e.Send(joinOf(c.p, c.p))
		b := e.Fake.Calls("banChatMember")
		if len(b) != 1 || b[0].P["user_id"] != itoa(c.p.ID) {
			t.Fatalf("%+v not banned: %+v", c.p, b)
		}
		if deleted(e) != 1 {
			t.Fatalf("join message of %d not deleted", c.p.ID)
		}
	}

	// Exempt: added by a chat admin, approved, chat admin, bot staff.
	e.Fake.Reset()
	added := Person{ID: 710, Name: "free crypto"}
	e.Send(joinOf(alice, added))
	if len(e.Fake.Calls("banChatMember")) != 0 {
		t.Fatal("people added by an admin are exempt")
	}
	e.Fake.SetMember(chat, 711, "free crypto", "administrator", nil)
	e.Send(joinOf(Person{ID: 711, Name: "free crypto"}, Person{ID: 711, Name: "free crypto"}))
	if len(e.Fake.Calls("banChatMember")) != 0 {
		t.Fatal("chat admins are exempt")
	}
	// A failed profile lookup is no reason to ban.
	e.Send(joinOf(Person{ID: 720, Name: "Quiet"}, Person{ID: 720, Name: "Quiet"}))
	if len(e.Fake.Calls("banChatMember")) != 0 {
		t.Fatal("unreadable profile banned")
	}
}
