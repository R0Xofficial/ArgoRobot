package modules_test

import (
	"strings"
	"testing"

	"argo/internal/core"
	"argo/internal/i18n"

	. "argo/internal/testutil"
)

func TestFilterParsing(t *testing.T) {
	l := i18n.English
	cases := []struct {
		in       string
		patterns []string
		rest     string
	}{
		{"bot Hello there", []string{"bot"}, "Hello there"},
		{`"a long   Sentence" Hi`, []string{"a long sentence"}, "Hi"},
		{`(bad, "Not Good", prefix:stay) Be nice`, []string{"bad", "not good", "prefix:stay"}, "Be nice"},
		{"exact:Ping pong", []string{"exact:ping"}, "pong"},
	}
	for _, c := range cases {
		got, rest, err := core.ParseFilterTriggers(l, c.in)
		if err != nil || strings.Join(got, "|") != strings.Join(c.patterns, "|") || rest != c.rest {
			t.Errorf("%q: %q %q %v", c.in, got, rest, err)
		}
	}
	for _, bad := range []string{"", `"open`, "(a, b", "()", `(two words) x`} {
		if _, _, err := core.ParseFilterTriggers(l, bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestFilters(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: bob, Text: "/filter hi Hello"})
	if n, _ := e.App.FilterPatterns(e.Ctx(), chat); len(n) != 0 {
		t.Fatal("a member saved a filter")
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/filter"})
	contains(t, lastText(t, e), "keyword")
	e.Send(Msg{Chat: chat, From: alice, Text: "/filter (Hello, \"good day\") Greetings, {first}!"})
	contains(t, lastText(t, e), "<code>hello</code>")
	e.Send(Msg{Chat: chat, From: alice, Text: "/filter exact:ping Pong"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/filter hello Updated"})
	contains(t, lastText(t, e), "Updated filter for")
	e.Send(Msg{Chat: chat, From: alice, Text: "/filters"})
	contains(t, lastText(t, e), "<code>exact:ping</code>")

	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: bob, Text: "well, HELLO everyone"})
	if got := lastText(t, e); got != "Updated" {
		t.Fatalf("answer %q", got)
	}
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: bob, Text: "hello again"}) // the cooldown
	if len(e.Fake.Calls("sendMessage")) != 0 {
		t.Fatal("answered twice in a row")
	}
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: bob, Text: "shell"}) // not a whole word
	e.Send(Msg{Chat: chat, From: bob, Text: "ping pong"})
	e.Send(Msg{Chat: chat, From: bob, Text: "/hello"})
	if len(e.Fake.Calls("sendMessage")) != 0 {
		t.Fatalf("should not match: %v", sent(e))
	}
	e.Send(Msg{Chat: chat, From: bob, Text: "PING"})
	want(t, lastText(t, e), "Pong")

	e.Send(Msg{Chat: chat, From: alice, Text: "/stop (exact:ping, nothing)"})
	got := lastText(t, e)
	contains(t, got, "<code>nothing</code>")
	contains(t, got, "<code>exact:ping</code>")
	e.Send(Msg{Chat: chat, From: alice, Text: "/stopall"})
	if len(e.Fake.Calls("sendMessage")) == 0 {
		t.Fatal("no answer")
	}
	e.Send(Msg{Chat: chat, From: owner, Text: "/stopall"})
	contains(t, lastText(t, e), "Remove all")
	e.Press(chat, owner, "filters:clear:yes")
	if n, _ := e.App.FilterPatterns(e.Ctx(), chat); len(n) != 0 {
		t.Fatalf("not cleared: %v", n)
	}
}

func TestAfk(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: bob, Text: "/afk gone fishing"})
	contains(t, lastText(t, e), "gone fishing")
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: carol, Text: "where is @bobby_x?", Entities: []map[string]any{{"type": "mention", "offset": 9, "length": 8}}})
	contains(t, lastText(t, e), "gone fishing")
	e.Send(Msg{Chat: chat, From: carol, Text: "hey again", Reply: &Msg{Chat: chat, From: bob, Text: "x"}}) // within the cooldown
	if n := len(e.Fake.Calls("sendMessage")); n != 1 {
		t.Fatalf("announced %d times", n)
	}
	e.Send(Msg{Chat: chat, From: bob, Text: "I'm back"})
	contains(t, lastText(t, e), "no longer <b>AFK</b>")
	if e.App.AfkGet(e.Ctx(), bob.ID) != nil {
		t.Fatal("still away")
	}
	e.Send(Msg{Chat: chat, From: carol, Text: "BRB lunch"})
	contains(t, lastText(t, e), "lunch")
	if e.App.AfkGet(e.Ctx(), carol.ID) == nil {
		t.Fatal("brb did not work")
	}
}

func TestReporting(t *testing.T) {
	e := setup2(t)
	e.Fake.SetMember(chat, carol.ID, carol.Name, "member", nil)
	e.Send(Msg{Chat: chat, From: bob, Text: "/report spam"})
	contains(t, lastText(t, e), "Reply to the message")
	e.Fake.Reset()
	spam := &Msg{Chat: chat, From: carol, Text: "buy things now"}
	e.Send(Msg{Chat: chat, From: bob, Text: "/report ads", Reply: spam})
	if !said(e, "to the admins.") {
		t.Fatalf("no notice: %v", sent(e))
	}
	var alerts int
	for _, c := range e.Fake.Calls("sendMessage") {
		if c.P["chat_id"] == itoa(alice.ID) && strings.Contains(c.P["text"], "buy things now") && strings.Contains(c.P["text"], "ads") {
			alerts++
		}
	}
	if alerts != 1 {
		t.Fatalf("admin alerts: %d in %v", alerts, sent(e))
	}
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: bob, Text: "/report again", Reply: spam})
	contains(t, lastText(t, e), "just sent a report")

	// Admins and the bot cannot be reported, and an @admin without a reply reports the message itself.
	e.Send(Msg{Chat: chat, From: dave, Text: "/report", Reply: &Msg{Chat: chat, From: alice, Text: "x"}})
	contains(t, lastText(t, e), "can't report an admin")
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: carol, Text: "help @admins please"})
	if !said(e, "to the admins.") {
		t.Fatalf("@admin did nothing: %v", sent(e))
	}

	// Turning it off.
	e.Send(Msg{Chat: chat, From: alice, Text: "/reports off"})
	contains(t, lastText(t, e), "off")
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: dave, Text: "@admin look", Reply: spam})
	if len(e.Fake.Calls("sendMessage")) != 0 {
		t.Fatal("reported while off")
	}
	e.Send(Msg{Chat: dave.ID, Type: "private", From: alice, Text: "/reports off"})
	if on, _ := e.App.ReportAlertsOn(e.Ctx(), alice.ID); on {
		t.Fatal("alerts still on")
	}
}
