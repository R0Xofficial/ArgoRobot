package blacklist

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"argo/internal/i18n"
)

type sticker struct {
	Emoji   *string `json:"emoji"`
	SetName *string `json:"set_name"`
}

type hit [3]json.RawMessage

type golden struct {
	Kind       string     `json:"kind"`
	In         string     `json:"in"`
	Normalized string     `json:"normalized"`
	Stored     string     `json:"stored"`
	Error      string     `json:"error"`
	Pattern    string     `json:"pattern"`
	Hits       [][3]any   `json:"hits"`
	Pairs      [][]string `json:"pairs"`
}

// The golden file holds the expected decisions for a large set of cases.
func TestSameAsGolden(t *testing.T) {
	if err := i18n.Load("../../locales"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Cases    []golden   `json:"cases"`
		Texts    []string   `json:"texts"`
		Stickers []*sticker `json:"stickers"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	l := i18n.English
	bad, checks := 0, 0
	fail := func(format string, args ...any) {
		bad++
		if bad <= 30 {
			t.Errorf(format, args...)
		}
	}
	for _, c := range file.Cases {
		switch c.Kind {
		case "check":
			if got := Normalize(c.In); got != c.Normalized {
				fail("Normalize(%q) = %q, want %q", c.In, got, c.Normalized)
			}
			stored, err := Check(l, c.In)
			var e *Error
			switch {
			case c.Error != "":
				if !errors.As(err, &e) || e.Msg != c.Error {
					fail("Check(%q): got %q / %v, want error %q", c.In, stored, err, c.Error)
				}
			case err != nil || stored != c.Stored:
				fail("Check(%q): got %q / %v, want %q", c.In, stored, err, c.Stored)
			}
		case "match":
			trig, err := Build(l, c.Pattern, "", 0, "")
			if err != nil {
				fail("Build(%q): %v", c.Pattern, err)
				continue
			}
			for _, h := range c.Hits {
				ti, si, want := int(h[0].(float64)), int(h[1].(float64)), h[2].(bool)
				var st *Sticker
				if s := file.Stickers[si]; s != nil {
					st = &Sticker{}
					if s.Emoji != nil {
						st.Emoji = *s.Emoji
					}
					if s.SetName != nil {
						st.Pack = *s.SetName
					}
				}
				checks++
				if got := Find([]*Trigger{trig}, file.Texts[ti], st, nil) != nil; got != want {
					short := file.Texts[ti]
					if len(short) > 60 {
						short = short[:60] + "..."
					}
					fail("pattern %q text %q sticker %d: got %v, want %v", c.Pattern, short, si, got, want)
				}
			}
		case "escape":
			for _, p := range c.Pairs {
				if got := EscapeWildcards(p[0]); got != p[1] {
					fail("EscapeWildcards(%q) = %q, want %q", p[0], got, p[1])
				}
			}
		}
	}
	if bad > 0 {
		t.Fatalf("%d differences from the golden file (%d match checks)", bad, checks)
	}
	t.Logf("%d match checks agree with the golden file", checks)
}

func TestWildcardsCannotBeSlow(t *testing.T) {
	// The worst allowed shape on the longest allowed text must still answer at once.
	tr, err := Build(i18n.English, "a**a**a*a*b", "", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	text := make([]rune, MaxMatchedText)
	for i := range text {
		text[i] = 'a'
	}
	if tr.Matches(text, nil) {
		t.Fatal("matched, but there is no b")
	}
}
