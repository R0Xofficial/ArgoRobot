package text

import (
	"testing"

	"argo/internal/i18n"
)

func TestEscAndLinks(t *testing.T) {
	if got := Esc(`a<b>&"it's"`); got != `a&lt;b&gt;&amp;"it's"` {
		t.Errorf("Esc: %q", got)
	}
	if got := UserLink(5, "A<B"); got != `<a href="tg://user?id=5">A&lt;B</a>` {
		t.Errorf("UserLink: %q", got)
	}
	if got := UserLink(5, "  "); got != "user[5]" {
		t.Errorf("nameless: %q", got)
	}
	if got := UserRef(5, "Al"); got != `<a href="tg://user?id=5">Al</a> [<code>5</code>]` {
		t.Errorf("UserRef: %q", got)
	}
	if got := StripTags(`<b>a &amp; b</b> <a href="x">c</a>`); got != "a & b c" {
		t.Errorf("StripTags: %q", got)
	}
	if got := Truncate("hello world", 8); got != "hello..." {
		t.Errorf("Truncate: %q", got)
	}
	if got := Truncate("żółć żółć", 6); got != "żół..." {
		t.Errorf("Truncate runes: %q", got)
	}
}

func TestParseSwitch(t *testing.T) {
	for in, want := range map[string]bool{"on": true, " YES ": true, "off": false, "No": false} {
		if got, ok := ParseSwitch(in); !ok || got != want {
			t.Errorf("%q", in)
		}
	}
	if _, ok := ParseSwitch("maybe"); ok {
		t.Error("maybe")
	}
}

func TestDurationText(t *testing.T) {
	if err := i18n.Load("../../locales"); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		l    i18n.Lang
		secs int
		want string
	}{
		{"en", 90, "1 minute 30 seconds"},
		{"en", 2 * 3600, "2 hours"},
		{"pl", 2 * 3600, "2 godziny"},
		{"pl", 5 * 3600, "5 godzin"},
		{"pl", 86400 + 60, "1 dzień 1 minuta"},
	}
	for _, c := range cases {
		if got := DurationText(c.l, c.secs); got != c.want {
			t.Errorf("%s %d: %q, want %q", c.l, c.secs, got, c.want)
		}
	}
}
