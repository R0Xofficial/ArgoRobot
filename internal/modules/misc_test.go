package modules_test

import (
	"context"
	"strings"
	"testing"

	"argo/internal/core"
	"argo/internal/i18n"

	. "argo/internal/testutil"
)

func TestStartAndHelp(t *testing.T) {
	e := setup2(t)
	e.Send(private(bob, "/start"))
	contains(t, lastText(t, e), "Argo")
	e.Send(Msg{Chat: chat, From: bob, Text: "/start"})
	contains(t, lastText(t, e), "Argo")

	// In a group /help is a button to the private chat; a topic goes with it.
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: bob, Text: "/help notes"})
	if got := e.Fake.Calls("sendMessage"); len(got) != 1 || !strings.Contains(got[0].P["reply_markup"], "start=help_notes") {
		t.Fatalf("%+v", got)
	}
	e.Send(Msg{Chat: chat, From: bob, Text: "/help nonsense"})
	contains(t, lastText(t, e), "no help page")

	e.Send(private(bob, "/help"))
	got := e.Fake.Calls("sendMessage")
	last := got[len(got)-1].P["reply_markup"]
	for _, want := range []string{"help:module:notes", "help:module:filters", "help:module:locks", "start:home"} {
		if !strings.Contains(last, want) {
			t.Errorf("home keyboard lacks %s: %s", want, last)
		}
	}
	// Commands only for staff stay hidden from a member.
	if strings.Contains(last, "help:module:gbans") && !strings.Contains(last, "gbans") {
		t.Fatal("unreachable")
	}
	e.Send(private(bob, "/help save")) // by the name of a command
	contains(t, lastText(t, e), "<b>Notes</b>")
	e.Send(private(bob, "/start help_locks"))
	contains(t, lastText(t, e), "<b>Locks</b>")

	// Buttons.
	e.PressOn(bob.ID, "private", int(e.Fake.LastID()), bob, "help:module:notes")
	if len(e.Fake.Calls("editMessageText")) == 0 {
		t.Fatal("the page was not shown")
	}
	e.Fake.Reset()
	e.PressOn(bob.ID, "private", 5, bob, "help:page:locks:0:0")
	if c := e.Fake.Calls("editMessageText"); len(c) != 1 || !strings.Contains(c[0].P["text"], "Lockable items") {
		t.Fatalf("lock types page: %+v", c)
	}
	e.Fake.Reset()
	e.PressOn(bob.ID, "private", 5, bob, "help:module:nonexistent")
	if len(e.Fake.Calls("answerCallbackQuery")) != 1 || len(e.Fake.Calls("editMessageText")) != 0 {
		t.Fatal("an unknown category must only be answered")
	}
}

func TestHelpHidesStaffCommands(t *testing.T) {
	e := setup2(t)
	if err := e.App.SetRole(e.Ctx(), sudo.ID, core.Sudo, 0, sudo.Name, ""); err != nil {
		t.Fatal(err)
	}
	e.Send(private(bob, "/help gbans"))
	text := lastText(t, e)
	contains(t, text, "/gstat")
	if strings.Contains(text, "/gban <user>") {
		t.Fatal("a staff command is shown to a member")
	}
	e.Send(private(sudo, "/help gbans"))
	text = lastText(t, e)
	contains(t, text, "/gban &lt;user&gt;")
	if strings.Contains(text, "and up)") || strings.Contains(text, "<i>(") {
		t.Fatalf("a command is followed by who may use it: %s", text)
	}
	if strings.Contains(text, "/gexport") {
		t.Fatal("Owner-only command shown to sudo")
	}
}

func TestIDAndInfo(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: bob, Text: "/id"})
	got := lastText(t, e)
	contains(t, got, "<b>Your ID:</b> <code>20</code>")
	contains(t, got, "<b>Chat ID:</b>")
	e.Send(Msg{Chat: chat, From: bob, Text: "/id", Reply: &Msg{Chat: chat, From: carol, Text: "x"}})
	contains(t, lastText(t, e), "<b>User ID:</b> <code>30</code>")

	e.Send(Msg{Chat: chat, From: alice, Text: "/info", Reply: &Msg{Chat: chat, From: dave, Text: "x"}})
	got = lastText(t, e)
	contains(t, got, "<b>User Info</b>")
	contains(t, got, "<code>Member</code>")
	contains(t, got, "Globally Banned:</b> <code>No</code>")
	if strings.Count(got, "Globally Banned") != 1 {
		t.Fatalf("a flag is listed twice: %s", got)
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/approve", Reply: &Msg{Chat: chat, From: dave, Text: "x"}})
	e.Send(Msg{Chat: chat, From: alice, Text: "/info", Reply: &Msg{Chat: chat, From: dave, Text: "x"}})
	contains(t, lastText(t, e), "<code>Member (Approved)</code>")

	// An admin has no flags to list, the owner of the bot has a role.
	e.Send(Msg{Chat: chat, From: bob, Text: "/info", Reply: &Msg{Chat: chat, From: alice, Text: "x"}})
	got = lastText(t, e)
	contains(t, got, "<code>Admin</code>")
	if strings.Contains(got, "Globally Banned") {
		t.Fatal("flags shown for an admin")
	}
}

func TestInfoGbanned(t *testing.T) {
	e := gbanSetup(t)
	if _, err := e.App.AddGban(e.Ctx(), carol.ID, carol.Name, "spam", sudo.ID, sudo.Name, sudo.ID, sudo.Name); err != nil {
		t.Fatal(err)
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/info", Reply: &Msg{Chat: chat, From: carol, Text: "x"}})
	contains(t, lastText(t, e), "Globally Banned:</b> <code>Yes</code>")
	e.Fake.SetChat("-100999", map[string]any{"id": -100999, "type": "channel", "title": "News", "username": "newsch"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/info -100999"})
	got := lastText(t, e)
	contains(t, got, "<b>Chat Info</b>")
	contains(t, got, "https://t.me/newsch")
	e.Send(Msg{Chat: chat, From: alice, Text: "/info 777777"})
	contains(t, lastText(t, e), "I don't know")
}

func TestLookups(t *testing.T) {
	e := setup2(t)
	old := core.GetJSON
	defer func() { core.GetJSON = old }()
	core.GetJSON = func(_ context.Context, l i18n.Lang, url string, params map[string]string) (any, error) {
		switch {
		case strings.Contains(url, "translate"):
			return []any{[]any{[]any{"Dzień dobry", "Good morning"}}, nil, "en"}, nil
		case strings.Contains(url, "dictionaryapi"):
			return []any{map[string]any{"word": "cat", "phonetic": "/kæt/", "meanings": []any{map[string]any{"partOfSpeech": "noun",
				"definitions": []any{map[string]any{"definition": "A small animal.", "example": "The cat sat."}}}}}}, nil
		case strings.Contains(url, "geocoding"):
			return map[string]any{"results": []any{map[string]any{"name": "Warsaw", "admin1": "Mazovia", "country": "Poland", "country_code": "PL", "latitude": 52.2, "longitude": 21.0, "timezone": "Europe/Warsaw"}}}, nil
		case strings.Contains(url, "forecast"):
			return map[string]any{"current": map[string]any{"temperature_2m": 10.0, "apparent_temperature": 8.0, "relative_humidity_2m": 70.0,
				"precipitation": 0.2, "weather_code": 3.0, "wind_speed_10m": 16.0, "wind_direction_10m": 90.0}}, nil
		case strings.Contains(url, "er-api"):
			return map[string]any{"result": "success", "rates": map[string]any{"PLN": 4.0}, "time_last_update_utc": "Sat, 21 Nov 2026 00:00:01 +0000"}, nil
		}
		return nil, &core.WebError{Msg: "down"}
	}
	say := func(p Person, text string) string {
		e.Send(Msg{Chat: chat, From: p, Text: text})
		return lastText(t, e)
	}
	contains(t, say(bob, "/tr pl Good morning"), "Dzień dobry")
	contains(t, say(carol, "/dict cat"), "A small animal.")
	got := say(dave, "/weather Warsaw")
	contains(t, got, "Warsaw, Mazovia, Poland")
	contains(t, got, "10.0°C / 50.0°F")
	contains(t, got, "16 km/h / 10 mph from E")
	contains(t, say(alice, "/time Europe/Warsaw"), "<b>Time in Europe/Warsaw</b>")
	contains(t, say(owner, "/time Warsaw"), "Warsaw, Mazovia, Poland")
	got = say(carol, "/cash 12,5 usd pln") // carol asked a moment ago
	contains(t, got, "Slow down")
	got = say(sudo, "/cash 12,5 usd pln")
	contains(t, got, "<code>50.00 PLN</code>")
}
