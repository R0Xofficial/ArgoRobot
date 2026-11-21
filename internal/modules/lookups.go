package modules

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"argo/internal/core"
)

// /tr, /dict, /time, /weather, /cash.

const lookupCooldown = 3 * time.Second // between lookups of one person, to be kind to the free services

func registerLookups(r *core.Router) {
	r.Command(cmdTr, "tr")
	r.Command(cmdDict, "dict")
	r.Command(cmdTime, "time")
	r.Command(cmdWeather, "weather")
	r.Command(cmdCash, "cash")
}

// lookup runs one lookup and sends its text, or the reason it failed.
func lookup(c *core.Ctx, work func() (string, error)) error {
	if c.User != nil && !c.App.Cooldown(fmt.Sprintf("lookup:%d", c.User.Id), lookupCooldown) {
		c.Say(c.T("common.slow_down_a_little_and_try"))
		return nil
	}
	out, err := work()
	if err != nil {
		var we *core.WebError
		if !errors.As(err, &we) {
			return err
		}
		out = we.Msg // always a ready-made sentence, already safe for HTML
	}
	_, err = c.ReplyOpts(out, nil, noPreview)
	return err
}

func cmdTr(c *core.Ctx) error {
	words := strings.Fields(c.Args)
	target, rest := "en", c.Args
	if len(words) > 0 && core.IsLookupLanguage(words[0]) {
		target = strings.ToLower(words[0])
		rest = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(c.Args), words[0]))
	}
	body := strings.TrimSpace(rest)
	if body == "" {
		if replied := core.RepliedMessage(c.Msg); replied != nil {
			body = strings.TrimSpace(core.TextOf(replied))
		}
	}
	if body == "" {
		c.Say(c.T("misc.use_tr_language_lt_text_gt"))
		return nil
	}
	return lookup(c, func() (string, error) { return c.App.Translate(c.Ctx, c.L, body, target) })
}

func cmdDict(c *core.Ctx) error {
	word := strings.TrimSpace(c.Args)
	if word == "" {
		c.Say(c.T("misc.use_dict_lt_word_gt"))
		return nil
	}
	if len([]rune(word)) > 60 {
		c.Say(c.T("misc.that_is_too_long_for_a"))
		return nil
	}
	return lookup(c, func() (string, error) { return c.App.Dictionary(c.Ctx, c.L, word) })
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func cmdTime(c *core.Ctx) error {
	q := strings.TrimSpace(c.Args)
	if q == "" {
		c.Say(c.T("misc.use_time_lt_place_or_timezone"))
		return nil
	}
	return lookup(c, func() (string, error) { return c.App.TimeIn(c.Ctx, c.L, clip(q, 100)) })
}

func cmdWeather(c *core.Ctx) error {
	q := strings.TrimSpace(c.Args)
	if q == "" {
		c.Say(c.T("misc.use_weather_lt_place_gt_like"))
		return nil
	}
	return lookup(c, func() (string, error) { return c.App.Weather(c.Ctx, c.L, clip(q, 100)) })
}

func allLetters(s string) bool {
	for _, r := range s {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return s != ""
}

func cmdCash(c *core.Ctx) error {
	usage := c.T("misc.use_cash_lt_amount_gt_lt")
	words := strings.Fields(c.Args)
	if len(words) == 2 && allLetters(words[0]) && allLetters(words[1]) {
		words = append([]string{"1"}, words...) // /cash usd pln
	}
	if len(words) != 3 {
		c.Say(usage)
		return nil
	}
	amount, ok := core.ParseAmount(words[0])
	source, target := words[1], words[2]
	if !ok || len([]rune(source)) != 3 || len([]rune(target)) != 3 || !allLetters(source) || !allLetters(target) {
		c.Say(usage)
		return nil
	}
	return lookup(c, func() (string, error) { return c.App.Currency(c.Ctx, c.L, amount, source, target) })
}
