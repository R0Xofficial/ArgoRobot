package modules

import (
	"fmt"
	"strings"

	"argo/internal/core"
	"argo/internal/i18n"
	"argo/internal/text"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

// Language of the bot's texts (modules/language.py). The languages are the files in locales/.

const (
	cbLang      = "lang:"
	langRight   = "can_change_info"
	langColumns = 2
)

func registerLanguage(r *core.Router) {
	r.Command(cmdLanguage, "setlang")
	r.Button(cbLang, cbLanguage)
}

func langKeyboard(current string) *gotgbot.InlineKeyboardMarkup {
	var rows [][]gotgbot.InlineKeyboardButton
	var row []gotgbot.InlineKeyboardButton
	for _, code := range i18n.Codes() {
		label := i18n.Label(code)
		if code == current {
			label = "✓ " + label
		}
		row = append(row, core.Button(label, cbLang+code))
		if len(row) == langColumns {
			rows, row = append(rows, row), nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	return core.Keyboard(rows...)
}

func langStatus(l i18n.Lang, code string) string {
	var listing []string
	for _, known := range i18n.Codes() {
		listing = append(listing, fmt.Sprintf("%s (<code>%s</code>)", text.Esc(i18n.Label(known)), known))
	}
	return l.T("language.the_language_of_this_chat_is", core.V{
		"language": text.Esc(i18n.Label(code)), "listing": strings.Join(listing, "\n"),
	})
}

func cmdLanguage(c *core.Ctx) error {
	chatID := c.Msg.Chat.Id
	if c.Msg.Chat.Type != "private" && c.BeginChatCommand(core.AuthorizeOpts{Right: langRight}) == nil {
		return nil
	}
	arg := strings.ToLower(strings.TrimSpace(c.Args))
	if arg != "" {
		if !i18n.Known(arg) {
			var codes []string
			for _, code := range i18n.Codes() {
				codes = append(codes, "<code>"+code+"</code>")
			}
			c.Say(c.T("language.i_don_t_speak_that_use", core.V{"codes": strings.Join(codes, ", ")}))
			return nil
		}
		if err := c.App.SetChatLanguage(c.Ctx, chatID, arg); err != nil {
			return err
		}
		c.L = i18n.Lang(arg) // the answer is already in the new language
		_, err := c.ReplyText(c.T("language.done_the_language_of_this_chat", core.V{"language": text.Esc(i18n.Label(arg))}))
		return err
	}
	current := string(c.L)
	_, err := c.Reply(langStatus(c.L, current), langKeyboard(current))
	return err
}

func cbLanguage(c *core.Ctx) error {
	code := strings.TrimPrefix(c.Cb.Data, cbLang)
	if c.Msg == nil || !i18n.Known(code) {
		c.Answer(c.T("common.this_button_is_no_longer_valid"), true)
		return nil
	}
	if c.Msg.Chat.Type != "private" {
		if _, ok := c.AuthorizeCallback(langRight); !ok {
			return nil
		}
	}
	if err := c.App.SetChatLanguage(c.Ctx, c.Msg.Chat.Id, code); err != nil {
		return err
	}
	c.L = i18n.Lang(code)
	c.Answer("", false)
	return c.Edit(langStatus(c.L, code), langKeyboard(code))
}
