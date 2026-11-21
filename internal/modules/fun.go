package modules

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"
	"time"

	"argo/internal/core"
	"argo/internal/text"
)

// Lighter commands to break up moderation duty (modules/fun.py).

const (
	maxFunName   = 50
	udCooldown   = 3 * time.Second
	maxDice      = 20
	maxSides     = 1000
	maxFlip      = 100
	udMaxTermLen = 80
)

func registerFun(r *core.Router) {
	r.Command(func(c *core.Ctx) error { return funAction(c, "fun.pats.%02d", 26) }, "pat")
	r.Command(func(c *core.Ctx) error { return funAction(c, "fun.slaps.%02d", 26) }, "slap")
	r.Command(func(c *core.Ctx) error { return funAction(c, "fun.hugs.%02d", 26) }, "hug")
	r.Command(func(c *core.Ctx) error { return funAction(c, "fun.roasts.%02d", 50) }, "roast")
	r.Command(cmdDecide, "decide")
	r.Command(cmdFlip, "flip")
	r.Command(cmdRoll, "roll")
	r.Command(cmdRate, "rate")
	r.Command(cmdUd, "ud")
}

// funPick is the tag number n of a family of tags such as fun.pats.00 ... fun.pats.25.
func funPick(format string, count int) string { return fmt.Sprintf(format, rand.IntN(count)) }

// --- Who is it about ----------------------------------------------------------------

func funActor(c *core.Ctx) string {
	m := c.Msg
	if m.SenderChat != nil {
		t := m.SenderChat.Title
		if t == "" {
			t = c.T("fun.someone")
		}
		return "<b>" + text.Esc(t) + "</b>"
	}
	if m.From != nil {
		return text.UserLink(m.From.Id, core.FullName(m.From))
	}
	return c.T("fun.someone_2")
}

// funTarget is who it is aimed at, and whether that is the bot itself: a real user, given as a mention, @username, id or
// by replying; the sender when nothing is given.
func funTarget(c *core.Ctx) (string, bool, error) {
	replied := core.RepliedMessage(c.Msg)
	if replied == nil && strings.TrimSpace(c.Args) == "" {
		return funActor(c), false, nil
	}
	if replied != nil && replied.SenderChat != nil && replied.From == nil {
		t := replied.SenderChat.Title
		if t == "" {
			t = "someone"
		}
		return "<b>" + text.Esc(t) + "</b>", false, nil
	}
	target, err := c.ResolveTarget(true, true)
	if err != nil {
		return "", false, err
	}
	if target.IsChat() {
		name := target.Title
		if name == "" {
			name = target.Name
		}
		if name == "" {
			name = "someone"
		}
		return "<b>" + text.Esc(name) + "</b>", false, nil
	}
	name := target.Name
	if name == "" {
		name = target.FirstName
	}
	if name == "" && target.Username != "" {
		name = "@" + target.Username
	}
	if name == "" {
		name = strconv.FormatInt(target.ID, 10)
	}
	return text.UserLink(target.ID, name), target.ID == c.Bot.Id, nil
}

func funAction(c *core.Ctx, templates string, count int) error {
	target, isBot, err := funTarget(c)
	if err != nil {
		var te *core.TargetError
		if errors.As(err, &te) {
			c.Say(text.Esc(te.Msg))
			return nil
		}
		return err
	}
	if isBot {
		c.Say(c.T(funPick("fun.bot_replies.%02d", 11)))
		return nil
	}
	msg := c.T(funPick(templates, count), core.V{"a": funActor(c), "t": target, "i": c.T(funPick("fun.slap_items.%02d", 51))})
	_, err = c.ReplyOpts(msg, nil, noPreview)
	return err
}

// --- Decide, flip, roll, rate ---------------------------------------------------------

func cmdDecide(c *core.Ctx) error {
	// Yes, No and Maybe are equally likely, then one of their eight answers.
	families := []struct {
		format string
		first  int
	}{{"fun.answers.yes.%02d", 0}, {"fun.answers.no.%02d", 8}, {"fun.answers.maybe.%02d", 16}}
	f := families[rand.IntN(len(families))]
	c.Say(c.T(fmt.Sprintf(f.format, f.first+rand.IntN(8))))
	return nil
}

var flipFrom = []rune("abcdefghijklmnopqrstuvwxyz0123456789.,!?'_()")
var flipTo = []rune("ɐqɔpǝɟƃɥᴉɾʞlɯuodbɹsʇnʌʍxʎz0ƖᄅƐㄣϛ9ㄥ86˙'¡¿,‾)(")

// flipText is the text upside down and back to front; unknown characters stay as they are.
func flipText(s string) string {
	table := map[rune]rune{}
	for i, r := range flipFrom {
		table[r] = flipTo[i]
	}
	runes := []rune(s)
	out := make([]rune, 0, len(runes))
	for i := len(runes) - 1; i >= 0; i-- {
		r := runes[i]
		if t, ok := table[r]; ok {
			out = append(out, t)
		} else if t, ok := table[[]rune(strings.ToLower(string(r)))[0]]; ok {
			out = append(out, t)
		} else {
			out = append(out, r)
		}
	}
	return string(out)
}

func cmdFlip(c *core.Ctx) error {
	s := strings.Join(strings.Fields(c.Args), " ")
	if s == "" {
		side := c.T("fun.heads")
		if rand.IntN(2) == 1 {
			side = c.T("fun.tails")
		}
		c.Say("<b>" + side + "</b>")
		return nil
	}
	r := []rune(s)
	if len(r) > maxFlip {
		r = r[:maxFlip]
	}
	c.Say("(╯°□°)╯︵ " + text.Esc(flipText(string(r))))
	return nil
}

var diceRE = regexp.MustCompile(`(?i)^(\d{0,2})d(\d{1,4})$`)

func cmdRoll(c *core.Ctx) error {
	arg := strings.TrimSpace(c.Args)
	if arg == "" {
		arg = "1d6"
	}
	m := diceRE.FindStringSubmatch(arg)
	count, sides := 0, 0
	if m != nil {
		count = 1
		if m[1] != "" {
			count, _ = strconv.Atoi(m[1])
		}
		sides, _ = strconv.Atoi(m[2])
	}
	if m == nil || count < 1 || count > maxDice || sides < 2 || sides > maxSides {
		c.Say(c.T("fun.use_roll_ndm_like_roll_2d6", core.V{"MAX_DICE": maxDice, "MAX_SIDES": maxSides}))
		return nil
	}
	rolls := make([]string, count)
	total := 0
	for i := range rolls {
		n := 1 + rand.IntN(sides)
		total += n
		rolls[i] = strconv.Itoa(n)
	}
	lines := []string{c.T("fun.roll_d", core.V{"count": count, "sides": sides}), c.T("fun.rolls", core.V{"join": strings.Join(rolls, ", ")})}
	if count > 1 {
		lines = append(lines, fmt.Sprintf("• <b>Total:</b> <code>%d</code>", total))
	}
	c.Say(strings.Join(lines, "\n"))
	return nil
}

// funRate is a grade from 0 to 10 that only depends on the words, so it never changes its mind.
func funRate(thing string) int {
	sum := sha256.Sum256([]byte(strings.Join(strings.Fields(strings.ToLower(thing)), " ")))
	return int(sum[0]) % 11
}

func cmdRate(c *core.Ctx) error {
	thing := strings.Join(strings.Fields(c.Args), " ")
	if r := []rune(thing); len(r) > maxFunName {
		thing = string(r[:maxFunName])
	}
	if replied := core.RepliedMessage(c.Msg); thing == "" && replied != nil && replied.From != nil {
		thing = core.FullName(replied.From)
	}
	if thing == "" {
		c.Say(c.T("fun.use_rate_lt_thing_gt_or"))
		return nil
	}
	grade := funRate(thing)
	c.Say(fmt.Sprintf("<b>%s</b>: <code>%d/10</code>\n%s", text.Esc(thing), grade, c.T(fmt.Sprintf("fun.verdicts.%02d", grade/2))))
	return nil
}

// --- Urban Dictionary --------------------------------------------------------------------

func cmdUd(c *core.Ctx) error {
	term := strings.Join(strings.Fields(c.Args), " ")
	if term == "" {
		c.Say(c.T("fun.use_ud_lt_term_gt"))
		return nil
	}
	if len([]rune(term)) > udMaxTermLen {
		c.Say(c.T("fun.that_is_too_long_for_a"))
		return nil
	}
	if c.User != nil && !c.App.Cooldown(fmt.Sprintf("ud:%d", c.User.Id), udCooldown) {
		c.Say(c.T("common.slow_down_a_little_and_try"))
		return nil
	}
	out, err := c.App.Urban(c.Ctx, c.L, term)
	if err != nil {
		var we *core.WebError
		if !errors.As(err, &we) {
			return err
		}
		out = we.Msg
	}
	_, err = c.ReplyOpts(out, nil, noPreview)
	return err
}
