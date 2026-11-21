package modules_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"argo/internal/core"
	. "argo/internal/testutil"
)

func TestParseZoneAndClock(t *testing.T) {
	for in, want := range map[string]string{"utc": "UTC", "Z": "UTC", "UTC+2": "UTC+02:00", "+5:30": "UTC+05:30", "gmt -03": "UTC-03:00", "Europe/Warsaw": "Europe/Warsaw"} {
		if got, _, ok := core.ParseZone(in); !ok || got != want {
			t.Errorf("zone %q: %q %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "Mars", "UTC+15", "+2:75", "Nowhere/City"} {
		if _, _, ok := core.ParseZone(in); ok {
			t.Errorf("zone %q should be refused", in)
		}
	}
	for in, want := range map[string]int{"23:00": 1380, "7": 420, "07:30": 450, "0": 0} {
		if got, ok := core.ParseClock(in); !ok || got != want {
			t.Errorf("clock %q: %d %v", in, got, ok)
		}
	}
	for _, in := range []string{"24:00", "7:60", "x", ""} {
		if _, ok := core.ParseClock(in); ok {
			t.Errorf("clock %q should be refused", in)
		}
	}
}

func TestIsNight(t *testing.T) {
	s := core.NightSettings{Start: 23 * 60, End: 7 * 60, Zone: "UTC"}
	at := func(h, m int) time.Time { return time.Date(2026, 1, 5, h, m, 0, 0, time.UTC) }
	for _, tc := range []struct {
		h, m int
		want bool
	}{{22, 59, false}, {23, 0, true}, {3, 0, true}, {6, 59, true}, {7, 0, false}, {12, 0, false}} {
		if got := s.IsNight(at(tc.h, tc.m)); got != tc.want {
			t.Errorf("%d:%02d -> %v", tc.h, tc.m, got)
		}
	}
	s = core.NightSettings{Start: 60, End: 120, Zone: "UTC+2"} // 01:00-02:00 at UTC+2 is 23:00-00:00 UTC
	if !s.IsNight(at(23, 30)) || s.IsNight(at(1, 30)) {
		t.Error("zone offset")
	}
	if (core.NightSettings{Start: 5, End: 5, Zone: "UTC"}).IsNight(at(5, 0)) {
		t.Error("equal hours mean no night")
	}
}

func TestNightmodeCommands(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: bob, Text: "/nightmode"})
	contains(t, lastText(t, e), "Only chat admins")
	e.Send(Msg{Chat: chat, From: alice, Text: "/nightmode"})
	got := lastText(t, e)
	contains(t, got, "<b>Enabled:</b> <code>off</code>")
	contains(t, got, "<code>23:00</code> to <code>07:00</code>")
	e.Send(Msg{Chat: chat, From: alice, Text: "/nightmode maybe"})
	contains(t, lastText(t, e), "Use /nightmode")
	e.Send(Msg{Chat: chat, From: alice, Text: "/nighthours 25 7"})
	contains(t, lastText(t, e), "Use /nighthours")
	e.Send(Msg{Chat: chat, From: alice, Text: "/nighthours 8 8"})
	contains(t, lastText(t, e), "Use /nighthours")
	e.Send(Msg{Chat: chat, From: alice, Text: "/nighthours 22:30 to 6"})
	contains(t, lastText(t, e), "22:30")
	e.Send(Msg{Chat: chat, From: alice, Text: "/nighttz Mars"})
	contains(t, lastText(t, e), "Use /nighttz")
	e.Send(Msg{Chat: chat, From: alice, Text: "/nighttz Europe/Warsaw"})
	contains(t, lastText(t, e), "Europe/Warsaw")
	if len(e.Fake.Calls("setChatPermissions")) != 0 {
		t.Fatal("nothing to do while the night is off")
	}
}

func hours(now time.Time, before, after time.Duration) (int, int) {
	a, b := now.Add(-before), now.Add(after)
	return a.Hour()*60 + a.Minute(), b.Hour()*60 + b.Minute()
}

func TestNightmodeClosesAndOpens(t *testing.T) {
	e := setup2(t)
	e.Fake.SetChat("-100500", map[string]any{"id": -100500, "type": "supergroup", "title": "Test group",
		"permissions": map[string]any{"can_send_messages": true, "can_send_photos": true, "can_send_polls": false, "can_invite_users": true, "can_pin_messages": true}})
	start, end := hours(time.Now().UTC(), 2*time.Hour, 2*time.Hour)
	e.Send(Msg{Chat: chat, From: alice, Text: fmt.Sprintf("/nighthours %02d:%02d %02d:%02d", start/60, start%60, end/60, end%60)})
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: alice, Text: "/nightmode on"})
	calls := e.Fake.Calls("setChatPermissions")
	if len(calls) != 1 {
		t.Fatalf("closed once: %+v", e.Fake.Calls(""))
	}
	var closed map[string]any
	if err := json.Unmarshal([]byte(calls[0].P["permissions"]), &closed); err != nil {
		t.Fatal(err)
	}
	if closed["can_send_messages"] == true || closed["can_send_photos"] == true || closed["can_invite_users"] != true {
		t.Fatalf("send rights off, the rest kept: %v", closed)
	}
	if calls[0].P["use_independent_chat_permissions"] != "true" {
		t.Fatalf("independent: %v", calls[0].P)
	}
	contains(t, lastText(t, e), "<b>Chat right now:</b> <code>closed</code>")
	if !said(e, "Night mode is on") {
		t.Errorf("announced: %q", sent(e))
	}
	s, _ := e.App.NightSettings(e.Ctx(), -100500)
	if !s.Active || !s.Saved["can_send_messages"] || !s.Saved["can_invite_users"] {
		t.Fatalf("saved: %+v", s)
	}

	// the sweep does nothing while it is night; then the night is moved away and the sweep opens the chat
	e.Fake.Reset()
	if err := e.App.SweepNight(e.Ctx(), e.Bot); err != nil || len(e.Fake.Calls("setChatPermissions")) != 0 {
		t.Fatalf("sweep during the night: %v", err)
	}
	later, laterEnd := hours(time.Now().UTC(), -3*time.Hour, 4*time.Hour)
	if err := e.App.UpdateNight(e.Ctx(), -100500, map[string]any{"start": later, "end": laterEnd}); err != nil {
		t.Fatal(err)
	}
	if err := e.App.SweepNight(e.Ctx(), e.Bot); err != nil {
		t.Fatal(err)
	}
	calls = e.Fake.Calls("setChatPermissions")
	if len(calls) != 1 {
		t.Fatalf("opened: %+v", e.Fake.Calls(""))
	}
	var opened map[string]any
	_ = json.Unmarshal([]byte(calls[0].P["permissions"]), &opened)
	if opened["can_send_messages"] != true || opened["can_send_photos"] != true || opened["can_invite_users"] != true {
		t.Fatalf("restored: %v", opened)
	}
	s, _ = e.App.NightSettings(e.Ctx(), -100500)
	if s.Active || s.Saved != nil {
		t.Fatalf("state cleared: %+v", s)
	}
	if !said(e, "over") {
		t.Errorf("announced the morning: %q", sent(e))
	}
}

func TestNightmodeRefused(t *testing.T) {
	e := setup2(t)
	e.Fake.SetChat("-100500", map[string]any{"id": -100500, "type": "supergroup", "title": "Test group", "permissions": map[string]any{"can_send_messages": true}})
	start, end := hours(time.Now().UTC(), time.Hour, time.Hour)
	e.Send(Msg{Chat: chat, From: alice, Text: fmt.Sprintf("/nighthours %02d:%02d %02d:%02d", start/60, start%60, end/60, end%60)})
	e.Fake.Fail("setChatPermissions", 400, "Bad Request: not enough rights")
	e.Send(Msg{Chat: chat, From: alice, Text: "/nightmode on"})
	contains(t, lastText(t, e), "not enough rights")
	s, _ := e.App.NightSettings(e.Ctx(), -100500)
	if s.Active {
		t.Fatal("a failed closing leaves nothing behind")
	}
}
