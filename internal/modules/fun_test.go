package modules_test

import (
	"context"
	"strings"
	"testing"

	"argo/internal/core"
	"argo/internal/i18n"

	. "argo/internal/testutil"
)

func TestFunActions(t *testing.T) {
	e := setup2(t)
	for _, cmd := range []string{"/pat", "/slap", "/hug", "/roast"} {
		e.Fake.Reset()
		e.Send(Msg{Chat: chat, From: bob, Text: cmd + " @carol"})
		if len(e.Fake.Calls("sendMessage")) != 1 {
			t.Fatalf("%s: %q", cmd, sent(e))
		}
		if strings.Contains(lastText(t, e), "{") {
			t.Errorf("%s left a placeholder: %q", cmd, lastText(t, e))
		}
	}
	// Without a target the sender is the target; the bot answers for itself.
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: bob, Text: "/hug"})
	contains(t, lastText(t, e), "tg://user?id=20")
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: bob, Text: "/slap " + itoa(BotID)})
	if strings.Contains(lastText(t, e), "slaps") && strings.Contains(lastText(t, e), "tg://user?id="+itoa(BotID)) {
		t.Errorf("the bot should not be slapped: %q", lastText(t, e))
	}
}

func TestFunSmall(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: bob, Text: "/decide"})
	if lastText(t, e) == "" {
		t.Error("empty answer")
	}
	e.Send(Msg{Chat: chat, From: bob, Text: "/flip Hello"})
	want(t, lastText(t, e), "(╯°□°)╯︵ ollǝɥ")
	e.Send(Msg{Chat: chat, From: bob, Text: "/flip"})
	if l := lastText(t, e); !strings.HasPrefix(l, "<b>") {
		t.Errorf("%q", l)
	}
	e.Send(Msg{Chat: chat, From: bob, Text: "/roll 3d6"})
	contains(t, lastText(t, e), "Total")
	e.Send(Msg{Chat: chat, From: bob, Text: "/roll 99d6"})
	contains(t, lastText(t, e), "20")
	e.Send(Msg{Chat: chat, From: bob, Text: "/roll abc"})
	contains(t, lastText(t, e), "roll")

	e.Send(Msg{Chat: chat, From: bob, Text: "/rate pizza"})
	first := lastText(t, e)
	e.Send(Msg{Chat: chat, From: bob, Text: "/rate  PIZZA "})
	contains(t, lastText(t, e), first[strings.Index(first, ":"):])
	contains(t, first, "/10")
	e.Send(Msg{Chat: chat, From: bob, Text: "/rate"})
	contains(t, lastText(t, e), "/rate")
}

func TestUd(t *testing.T) {
	e := setup2(t)
	old := core.GetJSON
	defer func() { core.GetJSON = old }()
	core.GetJSON = func(_ context.Context, l i18n.Lang, url string, params map[string]string) (any, error) {
		return map[string]any{"list": []any{map[string]any{"word": "w", "definition": "a [thing]", "example": "ex", "thumbs_up": 3.0, "thumbs_down": 1.0}}}, nil
	}
	e.Send(Msg{Chat: chat, From: bob, Text: "/ud word"})
	contains(t, lastText(t, e), "thing")
	n := len(sent(e))
	e.Send(Msg{Chat: chat, From: bob, Text: "/ud word"})
	if len(sent(e)) != n+1 {
		t.Error("the cooldown reply is missing")
	}
	e.Send(Msg{Chat: chat, From: carol, Text: "/ud"})
	contains(t, lastText(t, e), "/ud")
}
