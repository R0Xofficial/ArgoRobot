package markup

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"

	"argo/internal/i18n"
)

type entity struct {
	Type     string  `json:"type"`
	Offset   int     `json:"offset"`
	Length   int     `json:"length"`
	URL      *string `json:"url"`
	Language *string `json:"language"`
}

type golden struct {
	Kind     string            `json:"kind"`
	In       string            `json:"in"`
	Out      json.RawMessage   `json:"out"`
	Values   map[string]string `json:"values"`
	HTML     string            `json:"html"`
	Buttons  [][][]string      `json:"buttons"`
	Options  []string          `json:"options"`
	Error    string            `json:"error"`
	Raises   bool              `json:"raises"`
	Text     string            `json:"text"`
	Entities []entity          `json:"entities"`
	Literal  bool              `json:"literal"`
	Skip     int               `json:"skip"`
}

// The golden file holds what the Python version (core/markup.py) produces; it was made once from the Python version.
func TestSameAsPython(t *testing.T) {
	if err := i18n.Load("../../locales"); err != nil {
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
	l := i18n.English
	bad := 0
	fail := func(format string, args ...any) {
		bad++
		if bad <= 25 {
			t.Errorf(format, args...)
		}
	}
	for _, c := range cases {
		switch c.Kind {
		case "to_html":
			got := ToHTML(c.In)
			if c.Raises {
				continue // Python fails on these; Go only has to cope
			}
			var want string
			_ = json.Unmarshal(c.Out, &want)
			if got != want {
				fail("to_html(%q)\n got: %q\nwant: %q", c.In, got, want)
			}
		case "parse":
			p, err := Parse(l, c.In, c.Values)
			if c.Raises {
				continue
			}
			var me *Error
			switch {
			case c.Error != "":
				if !errors.As(err, &me) || me.Msg != c.Error {
					fail("parse(%q, %v) error\n got: %v\nwant: %q", c.In, c.Values, err, c.Error)
				}
			case err != nil:
				fail("parse(%q, %v) failed: %v", c.In, c.Values, err)
			default:
				var buttons [][][]string
				for _, row := range p.Buttons {
					var r [][]string
					for _, b := range row {
						r = append(r, []string{b.Text, b.URL})
					}
					buttons = append(buttons, r)
				}
				var options []string
				for _, o := range Options {
					if p.Options[o] {
						options = append(options, o)
					}
				}
				wantOpts := append([]string(nil), c.Options...)
				// Python sorts the names alphabetically.
				sortStrings(options)
				if p.HTML != c.HTML || !reflect.DeepEqual(buttons, nilIfEmpty(c.Buttons)) || !reflect.DeepEqual(emptyToNil(options), emptyToNil(wantOpts)) {
					fail("parse(%q, %v)\n got: %q %v %v\nwant: %q %v %v", c.In, c.Values, p.HTML, buttons, options, c.HTML, c.Buttons, c.Options)
				}
			}
		case "check":
			err := Check(l, c.In)
			if c.Raises {
				continue
			}
			var me *Error
			switch {
			case c.Error != "":
				if !errors.As(err, &me) || me.Msg != c.Error {
					fail("check(%q)\n got: %v\nwant: %q", c.In, err, c.Error)
				}
			case err != nil:
				fail("check(%q) failed: %v", c.In, err)
			}
		case "variants":
			var want []string
			_ = json.Unmarshal(c.Out, &want)
			if got := Variants(c.In); !reflect.DeepEqual(got, want) {
				fail("variants(%q) got %q want %q", c.In, got, want)
			}
		case "from_entities":
			var es []Entity
			for _, e := range c.Entities {
				x := Entity{Type: e.Type, Offset: e.Offset, Length: e.Length}
				if e.URL != nil {
					x.URL = *e.URL
				}
				if e.Language != nil {
					x.Language = *e.Language
				}
				es = append(es, x)
			}
			var want string
			_ = json.Unmarshal(c.Out, &want)
			if got := FromEntities(c.Text, es, c.Literal, c.Skip); got != want {
				fail("from_entities(%q, %+v, literal=%v, skip=%d)\n got: %q\nwant: %q", c.Text, c.Entities, c.Literal, c.Skip, got, want)
			}
		}
	}
	if bad > 0 {
		t.Fatalf("%d of %d cases differ from Python", bad, len(cases))
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func nilIfEmpty(b [][][]string) [][][]string {
	if len(b) == 0 {
		return nil
	}
	return b
}

func emptyToNil(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}
