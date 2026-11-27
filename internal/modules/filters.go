package modules

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/core"
	"argo/internal/text"
)

// Saved replies to keywords. Filters ignore case; a saved message can be formatted, address people
// by name, carry buttons and vary at random.

const (
	cbFiltersClear = "filters:clear:" // filters:clear:yes / filters:clear:no
	maxListedFilt  = 100
)

func registerFilters(r *core.Router) {
	r.Command(cmdFilters, "filters")
	r.Command(cmdFilter, "filter")
	r.Command(cmdStop, "stop")
	r.Command(cmdFilterCooldown, "filtercooldown")
	r.Command(cmdStopall, "stopall")
	r.Button(cbFiltersClear, cbStopall)
	// Answering a keyword (or a filter named like a command) never takes a message from anyone else.
	r.Watch(50, "filters", watchFilters)
}

func asFilterError(err error) (string, bool) {
	var fe *core.FilterError
	if errors.As(err, &fe) {
		return fe.Msg, true
	}
	return "", false
}

func codeList(list []string) string {
	parts := make([]string, len(list))
	for i, p := range list {
		parts[i] = "<code>" + text.Esc(p) + "</code>"
	}
	return strings.Join(parts, ", ")
}

func cmdFilters(c *core.Ctx) error {
	if !isGroup(c.Msg.Chat.Type) {
		c.Say(c.T("common.this_command_only_works_in_groups"))
		return nil
	}
	found, err := c.App.FilterPatterns(c.Ctx, c.Msg.Chat.Id)
	if err != nil {
		return err
	}
	name := c.Msg.Chat.Title
	if name == "" {
		name = c.T("common.this_chat")
	}
	title := c.T("filters.filters_in", core.V{"v": text.Esc(name)})
	if len(found) == 0 {
		c.Say(c.T("filters.there_are_none_yet_add_one", core.V{"title": title}))
		return nil
	}
	lines := []string{title, ""}
	for _, p := range found[:min(len(found), maxListedFilt)] {
		lines = append(lines, "• <code>"+text.Esc(p)+"</code>")
	}
	if len(found) > maxListedFilt {
		lines = append(lines, fmt.Sprintf("…and %d more.", len(found)-maxListedFilt))
	}
	c.Say(strings.Join(lines, "\n"))
	return nil
}

func cmdFilter(c *core.Ctx) error {
	if !isGroup(c.Msg.Chat.Type) {
		c.Say(c.T("common.this_command_only_works_in_groups"))
		return nil
	}
	if !adminOnly(c) {
		return nil
	}
	patterns, body, err := core.ParseFilterTriggers(c.L, core.ArgsOf(c.Msg, c.Args))
	if err != nil {
		if msg, ok := asFilterError(err); ok {
			c.Say(c.T("filters.use_filter_lt_keyword_gt_lt", core.V{"exc": text.Esc(msg)}))
			return nil
		}
		return err
	}
	replied := core.RepliedMessage(c.Msg)
	media := core.MediaOf(c.Msg)
	if media == nil {
		media = core.MediaOf(replied)
	}
	if body == "" && replied != nil {
		body = core.TextOf(replied)
	}
	if body == "" && media == nil {
		c.Say(c.T("filters.tell_me_what_to_answer_with"))
		return nil
	}
	if err := c.Validate(body, media != nil); err != nil {
		return sayMarkupError(c, err)
	}
	var mt, mid string
	if media != nil {
		mt, mid = media.Kind, media.FileID
	}
	added, updated, full, err := c.App.SaveFilter(c.Ctx, c.Msg.Chat.Id, patterns, body, mt, mid, c.User.Id)
	if err != nil {
		return err
	}
	var lines []string
	if len(added) > 0 {
		lines = append(lines, c.T("filters.saved_filter_for")+codeList(added)+".")
	}
	if len(updated) > 0 {
		lines = append(lines, c.T("filters.updated_filter_for")+codeList(updated)+".")
	}
	if len(full) > 0 {
		lines = append(lines, c.T("filters.this_chat_is_full_filters_not", core.V{"MAX_FILTERS": core.MaxFilters})+codeList(full)+".")
	}
	c.Say(strings.Join(lines, "\n"))
	return nil
}

func cmdFilterCooldown(c *core.Ctx) error {
	if !isGroup(c.Msg.Chat.Type) {
		c.Say(c.T("common.this_command_only_works_in_groups"))
		return nil
	}
	if !adminOnly(c) {
		return nil
	}
	chatID := c.Msg.Chat.Id
	arg := strings.ToLower(strings.TrimSpace(core.ArgsOf(c.Msg, c.Args)))
	if arg == "" {
		c.Say(c.T("filters.cooldown_is", core.V{"seconds": c.App.FilterCooldown(c.Ctx, chatID), "max": core.MaxFilterCooldown}))
		return nil
	}
	seconds := -1
	if arg != "default" && arg != "reset" {
		n, err := strconv.Atoi(arg)
		if err != nil || n < 0 || n > core.MaxFilterCooldown || strings.HasPrefix(arg, "+") {
			c.Say(c.T("filters.cooldown_usage", core.V{"max": core.MaxFilterCooldown}))
			return nil
		}
		seconds = n
	}
	now, err := c.App.SetFilterCooldown(c.Ctx, chatID, seconds)
	if err != nil {
		return err
	}
	c.Say(c.T("filters.cooldown_set", core.V{"seconds": now}))
	return nil
}

func cmdStop(c *core.Ctx) error {
	if !isGroup(c.Msg.Chat.Type) {
		c.Say(c.T("common.this_command_only_works_in_groups"))
		return nil
	}
	if !adminOnly(c) {
		return nil
	}
	patterns, _, err := core.ParseFilterTriggers(c.L, c.Args)
	if err != nil {
		if msg, ok := asFilterError(err); ok {
			c.Say(c.T("filters.use_stop_lt_keyword_gt", core.V{"exc": text.Esc(msg)}))
			return nil
		}
		return err
	}
	var removed, missing []string
	for _, p := range patterns {
		ok, err := c.App.RemoveFilter(c.Ctx, c.Msg.Chat.Id, p)
		if err != nil {
			return err
		}
		if ok {
			removed = append(removed, p)
		} else {
			missing = append(missing, p)
		}
	}
	var lines []string
	if len(removed) > 0 {
		lines = append(lines, c.T("filters.removed_the_filter_for")+codeList(removed)+".")
	}
	if len(missing) > 0 {
		lines = append(lines, c.T("filters.there_is_no_filter_for")+codeList(missing)+".")
	}
	c.Say(strings.Join(lines, "\n"))
	return nil
}

func cmdStopall(c *core.Ctx) error {
	if !c.RequireChatOwner() {
		return nil
	}
	found, err := c.App.FilterPatterns(c.Ctx, c.Msg.Chat.Id)
	if err != nil {
		return err
	}
	if len(found) == 0 {
		c.Say(c.T("filters.there_are_no_filters_in_this"))
		return nil
	}
	_, err = c.Reply(c.T("filters.remove_all_filters_of_this_chat", core.V{"count": len(found)}), core.Keyboard(
		[]gotgbot.InlineKeyboardButton{core.Button(c.T("common.remove_all"), cbFiltersClear+"yes"), core.Button(c.T("common.cancel"), cbFiltersClear+"no")}))
	return err
}

func cbStopall(c *core.Ctx) error {
	if c.Msg == nil {
		c.Answer(c.T("common.this_button_is_no_longer_valid"), true)
		return nil
	}
	if c.MemberStatus(c.Msg.Chat.Id, c.User.Id) != "creator" {
		c.Answer(c.T("common.only_the_owner_of_this_chat"), true)
		return nil
	}
	if c.Cb.Data == cbFiltersClear+"yes" {
		removed, err := c.App.ClearFilters(c.Ctx, c.Msg.Chat.Id)
		if err != nil {
			return err
		}
		c.Answer("", false)
		return c.Edit(c.T("filters.done_filters_removed", core.V{"removed": removed}), nil)
	}
	c.Answer("", false)
	return c.Edit(c.T("filters.cancelled_the_filters_are_unchanged"), nil)
}

// --- Answering ------------------------------------------------------------------------------

// commandWord is the command as typed (without "@bot") when the text starts with a real command of the bot, else "".
// Such a message is matched by its command word only, so a filter named "/ban" answers /ban (the command still runs),
// while a filter named "bot" is not answered by "/filter bot hi". Anything that starts with "/" but is not a command
// of the bot (a joke command like "/gbam") is just text.
func commandWord(c *core.Ctx, s string) string {
	word := s
	if i := strings.IndexFunc(s, unicode.IsSpace); i >= 0 {
		word = s[:i]
	}
	r := []rune(word)
	if len(r) < 2 || !strings.ContainsRune(c.App.Cfg.CommandPrefix, r[0]) || !unicode.IsLetter(r[1]) {
		return ""
	}
	word, _, _ = strings.Cut(word, "@")
	if _, _, ok := c.App.CommandOf(word); ok {
		return word
	}
	return ""
}

func watchFilters(c *core.Ctx) (bool, error) {
	m := c.Msg
	if m == nil || c.Cb != nil || c.Edited || c.User == nil || c.User.IsBot || !isGroup(m.Chat.Type) {
		return false, nil
	}
	body := m.Text
	if body == "" {
		body = m.Caption
	}
	if body == "" {
		return false, nil
	}
	if word := commandWord(c, body); word != "" {
		body = word
	}
	pattern := c.App.FilterMatch(c.Ctx, m.Chat.Id, body)
	if pattern == "" || !c.App.Cooldown(fmt.Sprintf("filter:%d/%s", m.Chat.Id, pattern), time.Duration(c.App.FilterCooldown(c.Ctx, m.Chat.Id))*time.Second) {
		return false, nil
	}
	row, err := c.App.GetFilter(c.Ctx, m.Chat.Id, pattern)
	if err != nil || row == nil {
		return false, err
	}
	c.SendSaved(c.User, row.Content, row.MediaType, row.MediaID, core.SendOpts{Label: c.T("filters.filter", core.V{"filter_pattern": pattern})})
	return false, nil // answering a keyword never takes a message from anyone else
}
