package i18n

import (
	"encoding/json"
	"os"
	"testing"
)

type golden struct {
	Lang      string         `json:"lang"`
	Tag       string         `json:"tag"`
	Vals      map[string]any `json:"vals"`
	Plural    *int           `json:"plural"`
	PluralTag *int           `json:"plural_tag"`
	Out       string         `json:"out"`
}

// The golden file holds what the Python version (core/i18n.py) renders for every tag of every language, with
// made-up values. The Go port must write exactly the same texts.
func TestSameTextsAsPython(t *testing.T) {
	if err := Load("../../locales"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []golden
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	bad := 0
	for _, c := range cases {
		l := Lang(c.Lang)
		var got string
		switch {
		case c.Plural != nil:
			got = l.Plural(*c.Plural, "one", "few", "many")
		case c.PluralTag != nil:
			got = l.PluralTag(c.Tag, *c.PluralTag)
		case len(c.Vals) > 0:
			got = l.T(c.Tag, V(c.Vals))
		default:
			got = l.T(c.Tag)
		}
		if got != c.Out {
			if bad++; bad <= 15 {
				t.Errorf("%s %s:\n got: %q\nwant: %q", c.Lang, c.Tag, got, c.Out)
			}
		}
	}
	t.Logf("%d texts compared, %d differ", len(cases), bad)
	if bad > 15 {
		t.Errorf("... and %d more", bad-15)
	}
}

func TestLanguagesFound(t *testing.T) {
	if err := Load("../../locales"); err != nil {
		t.Fatal(err)
	}
	codes := Codes()
	if len(codes) < 4 || codes[0] != "en" {
		t.Fatalf("codes: %v", codes)
	}
	if Label("pl") == "pl" || !Known("en-pt") || !Known("pl-sl") || Known("xx") {
		t.Errorf("labels/known: %q", Label("pl"))
	}
	if got := Lang("xx").T("bans.was_unbanned", V{"who": "A", "by": "", "staff_note": ""}); got != "A was unbanned" {
		t.Errorf("an unknown language speaks English: %q", got)
	}
}
