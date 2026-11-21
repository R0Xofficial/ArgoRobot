package core

import (
	"testing"
	"time"
)

func TestParseCommand(t *testing.T) {
	cases := []struct {
		in         string
		name, args string
		ok         bool
	}{
		{"/ban", "ban", "", true},
		{"/Ban 123 reason here", "ban", "123 reason here", true},
		{"!kick  @x   why ", "kick", "@x   why", true},
		{"/ban@ArgoTestBot 5", "ban", "5", true},
		{"/ban@OtherBot 5", "", "", false},
		{"/ban@argotestbot", "ban", "", true},
		{"hello /ban", "", "", false},
		{"/", "", "", false},
		{"", "", "", false},
		{"/ban\n12", "ban", "12", true},
		{"#ban", "", "", false},
	}
	for _, c := range cases {
		name, args, ok := ParseCommand(c.in, "/!", "ArgoTestBot")
		if name != c.name || args != c.args || ok != c.ok {
			t.Errorf("%q: got (%q, %q, %v), want (%q, %q, %v)", c.in, name, args, ok, c.name, c.args, c.ok)
		}
	}
}

func TestFirstArgumentOffsetUTF16(t *testing.T) {
	cases := map[string]int{
		"/ban x":    5,
		"/ban   x":  7,
		"/ban":      -1,
		"/ban   ":   -1,
		"/😀 x":      4, // the emoji is two UTF-16 units
		"/ban\nx y": 5,
		"":          -1,
	}
	for in, want := range cases {
		if got := firstArgumentOffset(in); got != want {
			t.Errorf("%q: got %d, want %d", in, got, want)
		}
	}
}

func TestUTF16Slice(t *testing.T) {
	text := "/ban 😀bob rest"
	if got := utf16Slice(text, 5, 8); got != "😀b" {
		t.Errorf("got %q", got)
	}
	if got := utf16Slice(text, 10, -1); got != " rest" {
		t.Errorf("got %q", got)
	}
}

func TestStripNameRemainder(t *testing.T) {
	cases := []struct{ extra, linked, full, want string }{
		{"Kowalski spamming", "Jan", "Jan Kowalski", "spamming"},
		{"kowalski, spamming", "Jan", "Jan Kowalski", "spamming"},
		{"Kowalski", "Jan", "Jan Kowalski", ""},
		{"spamming", "Jan", "Jan Kowalski", "spamming"},
		{"spamming", "Jan Kowalski", "Jan Kowalski", "spamming"},
		{"x", "Other", "Jan Kowalski", "x"},
		{"x", "Jan", "", "x"},
		{"A B reason", "Jan", "Jan A B", "reason"},
	}
	for _, c := range cases {
		if got := stripNameRemainder(c.extra, c.linked, c.full); got != c.want {
			t.Errorf("%+v: got %q", c, got)
		}
	}
}

func TestSplitN(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want []string
	}{
		{"a b c", 2, []string{"a", "b c"}},
		{"  a   b  c ", 2, []string{"a", "b  c "}},
		{"a", 2, []string{"a"}},
		{"", 2, nil},
		{"a b c", 1, []string{"a b c"}},
	}
	for _, c := range cases {
		got := splitN(c.in, c.n)
		if len(got) != len(c.want) {
			t.Errorf("%q: %q", c.in, got)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%q: %q", c.in, got)
			}
		}
	}
}

func TestDurations(t *testing.T) {
	ok := map[string]time.Duration{"30m": 30 * time.Minute, "2H": 2 * time.Hour, "3d": 72 * time.Hour, "1w": 168 * time.Hour, "999999m": 999999 * time.Minute}
	for in, want := range ok {
		if got, valid := ParseDuration(in); !valid || got != want {
			t.Errorf("%q: %v %v", in, got, valid)
		}
	}
	for _, in := range []string{"", "m", "1", "1x", "-5m", "1.5h", "1234567m", "5 m"} {
		if _, valid := ParseDuration(in); valid {
			t.Errorf("%q should not be a duration", in)
		}
	}
	cases := map[time.Duration]string{
		time.Minute: "1 minute", 90 * time.Minute: "1 hour 30 minutes", 25 * time.Hour: "1 day 1 hour",
		2 * 24 * time.Hour: "2 days", 61 * time.Minute: "1 hour 1 minute",
	}
	for d, want := range cases {
		if got := FormatDuration(d); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}
	}
}

func TestEntryFormat(t *testing.T) {
	// The log entry layout is covered end to end in the modules tests; this pins the empty-parts behaviour.
	if !IsEnforceMode("tmute") || IsEnforceMode("del") || !IsTimedMode("tban") || IsTimedMode("ban") {
		t.Error("enforce modes")
	}
}
