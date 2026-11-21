package modules_test

import (
	"strings"
	"testing"

	. "argo/internal/testutil"
)

func TestRulesSetAndShow(t *testing.T) {
	e := setup(t)
	e.Send(Msg{Chat: chat, From: carol, Text: "/rules"})
	if !strings.Contains(lastText(t, e), "no rules yet") {
		t.Fatalf("got %q", lastText(t, e))
	}

	e.Send(Msg{Chat: chat, From: carol, Text: "/setrules be kind"})
	want(t, lastText(t, e), "Only chat admins can use this command.")

	e.Send(Msg{Chat: chat, From: alice, Text: "/setrules <b>Be kind</b> {first}\n[Site](buttonurl://example.com)"})
	if !strings.Contains(lastText(t, e), "rules are saved") {
		t.Fatalf("got %q", lastText(t, e))
	}

	e.Send(Msg{Chat: chat, From: carol, Text: "/rules"})
	calls := e.Fake.Calls("sendMessage")
	last := calls[len(calls)-1]
	if !strings.Contains(last.P["reply_markup"], "t.me/ArgoTestBot?start=rules_-100500") {
		t.Fatalf("no deep link button: %v", last.P)
	}

	e.Send(Msg{Chat: chat, From: carol, Text: "/rules here"})
	calls = e.Fake.Calls("sendMessage")
	last = calls[len(calls)-1]
	if last.P["text"] != "<b>Be kind</b> Carol" || !strings.Contains(last.P["reply_markup"], `"url":"https://example.com"`) {
		t.Errorf("rules here: %v", last.P)
	}

	e.Send(Msg{Chat: chat, From: carol, Text: "/rules noformat"})
	if got := lastText(t, e); got != "&lt;b&gt;Be kind&lt;/b&gt; {first}\n[Site](buttonurl://example.com)" {
		t.Errorf("noformat: %q", got)
	}

	e.Send(Msg{Chat: chat, From: carol, Text: "/rules now"})
	if !strings.Contains(lastText(t, e), "/rules") {
		t.Errorf("got %q", lastText(t, e))
	}

	e.Send(Msg{Chat: chat, From: alice, Text: "/resetrules"})
	want(t, lastText(t, e), "The rules are cleared.")
	e.Send(Msg{Chat: chat, From: alice, Text: "/resetrules"})
	if !strings.Contains(lastText(t, e), "no rules") {
		t.Errorf("got %q", lastText(t, e))
	}
}

func TestRulesDeepLinkInPrivate(t *testing.T) {
	e := setup(t)
	e.Fake.SetChat("-100500", map[string]any{"id": -100500, "type": "supergroup", "title": "Test group"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/setrules Be kind"})
	e.Send(Msg{Chat: carol.ID, Type: "private", From: carol, Text: "/start rules_-100500"})
	want(t, lastText(t, e), "Be kind")

	eve := Person{ID: 77, Name: "Eve"}
	e.Send(Msg{Chat: eve.ID, Type: "private", From: eve, Text: "/start rules_-100500"})
	if !strings.Contains(lastText(t, e), "not in that chat") && !strings.Contains(lastText(t, e), "aren't in") {
		t.Errorf("got %q", lastText(t, e))
	}
}

func TestRulesWithPhoto(t *testing.T) {
	e := setup(t)
	e.Send(Msg{Chat: chat, From: alice, Photo: "FILE1", Text: "/setrules Photo rules"})
	e.Send(Msg{Chat: chat, From: carol, Text: "/rules here"})
	c := e.Fake.Calls("sendPhoto")
	if len(c) != 1 || c[0].P["photo"] != "FILE1" || c[0].P["caption"] != "Photo rules" {
		t.Fatalf("sendPhoto: %+v", c)
	}
}

func TestNotes(t *testing.T) {
	e := setup(t)
	e.Send(Msg{Chat: chat, From: carol, Text: "/notes"})
	if !strings.Contains(lastText(t, e), "none yet") && !strings.Contains(lastText(t, e), "no notes") {
		t.Errorf("got %q", lastText(t, e))
	}
	e.Send(Msg{Chat: chat, From: carol, Text: "/save hi hello"})
	want(t, lastText(t, e), "Only chat admins can use this command.")

	e.Send(Msg{Chat: chat, From: alice, Text: "/save #Hi Hello {first}!"})
	if !strings.Contains(lastText(t, e), "Saved") {
		t.Fatalf("got %q", lastText(t, e))
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/save hi Hello again"})
	if !strings.Contains(lastText(t, e), "pdated") {
		t.Fatalf("got %q", lastText(t, e))
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/save bad!name text"})
	if !strings.Contains(lastText(t, e), "single word") {
		t.Errorf("got %q", lastText(t, e))
	}

	e.Send(Msg{Chat: chat, From: carol, Text: "/get HI"})
	want(t, lastText(t, e), "Hello again")
	e.Send(Msg{Chat: chat, From: carol, Text: "/get nope"})
	if !strings.Contains(lastText(t, e), "no note called") {
		t.Errorf("got %q", lastText(t, e))
	}
	e.Send(Msg{Chat: chat, From: carol, Text: "/notes"})
	if !strings.Contains(lastText(t, e), "<code>hi</code>") {
		t.Errorf("got %q", lastText(t, e))
	}

	// #hi answers once, then the cooldown holds it back.
	n := len(e.Fake.Calls("sendMessage"))
	e.Send(Msg{Chat: chat, From: carol, Text: "#hi there"})
	e.Send(Msg{Chat: chat, From: carol, Text: "#hi again"})
	if got := len(e.Fake.Calls("sendMessage")); got != n+1 {
		t.Errorf("hashtag answers: %d new messages", got-n)
	}

	e.Send(Msg{Chat: chat, From: alice, Text: "/clear hi"})
	if !strings.Contains(lastText(t, e), "emoved") {
		t.Errorf("got %q", lastText(t, e))
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/clear hi"})
	if !strings.Contains(lastText(t, e), "no note called") {
		t.Errorf("got %q", lastText(t, e))
	}
}

func TestSaveFromReplyAndClearAll(t *testing.T) {
	e := setup(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/save quote", Reply: &Msg{Chat: chat, From: bob, Text: "bold bit", Entities: []map[string]any{{"type": "bold", "offset": 0, "length": 4}}}})
	e.Send(Msg{Chat: chat, From: carol, Text: "/get quote"})
	want(t, lastText(t, e), "<b>bold</b> bit")

	e.Fake.SetMember(chat, alice.ID, alice.Name, "creator", nil)
	e.Send(Msg{Chat: chat, From: alice, Text: "/clearall"})
	if !strings.Contains(lastText(t, e), "Delete all <b>1</b>") {
		t.Fatalf("got %q", lastText(t, e))
	}
	e.Press(chat, alice, "notes:clear:yes")
	e.Send(Msg{Chat: chat, From: carol, Text: "/get quote"})
	if !strings.Contains(lastText(t, e), "no note called") {
		t.Errorf("got %q", lastText(t, e))
	}
}
