package modules

import (
	"errors"
	"fmt"
	"strings"

	"argo/internal/blacklist"
	"argo/internal/core"
	"argo/internal/text"
)

// Warn filters: a warn filter is a blacklist trigger whose action is "warn". The blacklist already
// matches every message, warns the sender and counts toward the limit.

func registerWarnFilters(r *core.Router) {
	r.Command(cmdAddwarn, "addwarn")
	r.Command(cmdNowarn, "nowarn", "stopwarn")
	r.Command(cmdWarnlist, "warnlist")
}

// splitKeyword: 'spam why' -> ('spam', 'why'); '"free money" why' -> ('free money', 'why').
func splitKeyword(s string) (string, string) {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > 0 && strings.ContainsRune(quotes, r[0]) {
		for i := 1; i < len(r); i++ {
			if strings.ContainsRune(quotes, r[i]) {
				return strings.TrimSpace(string(r[1:i])), strings.TrimSpace(string(r[i+1:]))
			}
		}
		return strings.TrimSpace(string(r[1:])), ""
	}
	keyword, rest, _ := strings.Cut(s, " ")
	return keyword, strings.TrimSpace(rest)
}

func warnTriggers(c *core.Ctx, chatID int64) ([]*blacklist.Trigger, error) {
	all, err := c.App.Triggers(c.Ctx, chatID)
	var out []*blacklist.Trigger
	for _, t := range all {
		if t.Action == "warn" {
			out = append(out, t)
		}
	}
	return out, err
}

func cmdAddwarn(c *core.Ctx) error {
	actor := c.BeginChatCommand(core.AuthorizeOpts{})
	if actor == nil {
		return nil
	}
	keyword, reason := splitKeyword(c.Args)
	if replied := core.RepliedMessage(c.Msg); reason == "" && replied != nil {
		reason = blacklist.CollapseSpace(messageText(replied))
	}
	if keyword == "" {
		c.Say(c.T("warns.give_a_word_or_a_phrase"))
		return nil
	}
	pattern, err := blacklist.Check(c.L, keyword)
	var isNew, full bool
	if err == nil {
		isNew, full, err = c.App.AddTrigger(c.Ctx, c.L, c.Msg.Chat.Id, pattern, "warn", 0, text.Truncate(reason, 200), actor.ID)
	}
	if err != nil {
		var te *blacklist.Error
		if errors.As(err, &te) {
			c.Say(text.Esc(te.Msg))
			return nil
		}
		return err
	}
	if full {
		c.Say(c.T("common.the_blacklist_is_full_triggers", core.V{"MAX_TRIGGERS": blacklist.MaxTriggers}))
		return nil
	}
	opt := c.T("warns.updated")
	if isNew {
		opt = c.T("warns.added")
	}
	c.Say(c.T("warns.anyone_who_writes_gets_a_warn", core.V{
		"pattern": text.Esc(pattern), "opt": opt, "by": c.By(actor), "staff_note": c.StaffNote(actor),
	}))
	return nil
}

func cmdNowarn(c *core.Ctx) error {
	actor := c.BeginChatCommand(core.AuthorizeOpts{})
	if actor == nil {
		return nil
	}
	keyword, _ := splitKeyword(c.Args)
	if keyword == "" {
		c.Say(c.T("warns.give_the_word_or_phrase_of"))
		return nil
	}
	pattern, err := blacklist.Check(c.L, keyword)
	if err != nil {
		var te *blacklist.Error
		if errors.As(err, &te) {
			c.Say(text.Esc(te.Msg))
			return nil
		}
		return err
	}
	chatID := c.Msg.Chat.Id
	found, err := warnTriggers(c, chatID)
	if err != nil {
		return err
	}
	exists := false
	for _, t := range found {
		if t.Pattern == pattern {
			exists = true
		}
	}
	removed := false
	if exists {
		if removed, err = c.App.RemoveTrigger(c.Ctx, chatID, pattern); err != nil {
			return err
		}
	}
	if !removed {
		c.Say(c.T("warns.there_is_no_warn_filter_like"))
		return nil
	}
	c.Say(c.T("warns.the_warn_filter_was_removed", core.V{"pattern": text.Esc(pattern), "by": c.By(actor), "staff_note": c.StaffNote(actor)}))
	return nil
}

func cmdWarnlist(c *core.Ctx) error {
	if c.BeginChatCommand(core.AuthorizeOpts{}) == nil {
		return nil
	}
	found, err := warnTriggers(c, c.Msg.Chat.Id)
	if err != nil {
		return err
	}
	if len(found) == 0 {
		c.Say(c.T("warns.there_are_no_warn_filters_in"))
		return nil
	}
	lines := []string{c.T("warns.warn_filters")}
	for i, t := range found {
		if i >= 80 {
			break
		}
		line := "• <code>" + text.Esc(t.Pattern) + "</code>"
		if t.Reason != "" {
			line += " - " + text.Esc(text.Truncate(t.Reason, 80))
		}
		lines = append(lines, line)
	}
	if len(found) > 80 {
		lines = append(lines, fmt.Sprintf("…and %d more.", len(found)-80))
	}
	c.Say(strings.Join(lines, "\n"))
	return nil
}
