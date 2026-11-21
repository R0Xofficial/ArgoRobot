package modules

import (
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/core"
	"argo/internal/markup"
	"argo/internal/text"
)

// Chat rules: text with optional media, kept as the admin wrote it and
// turned into a message only when it is sent.

const rightChangeInfo = "can_change_info"

var deepLinkRE = regexp.MustCompile(`(?i)^/start(?:@\w+)?\s+rules_(-?\d+)\s*$`)

func registerRules(r *core.Router) {
	r.Command(cmdSetrules, "setrules")
	r.Command(cmdResetrules, "resetrules")
	r.Command(cmdRules, "rules")
	// Before the module that answers a plain /start, so the rules link reaches us first.
	r.Watch(-10, "rules link", watchRulesLink)
}

func isGroup(t string) bool { return t == "group" || t == "supergroup" }

type savedRow struct {
	Content   string
	MediaType string
	MediaID   string
}

func (s *savedRow) empty() bool { return s.Content == "" && s.MediaType == "" }

func getRules(c *core.Ctx, chatID int64) (*savedRow, error) {
	var content string
	var mt, mid *string
	found, err := c.App.DB.Found(c.Ctx, `SELECT content, media_type, media_file_id FROM rules WHERE chat_id = $1`,
		[]any{chatID}, &content, &mt, &mid)
	if err != nil || !found {
		return nil, err
	}
	return &savedRow{content, deref(mt), deref(mid)}, nil
}

// adminOnly is the chat's own business: the people of the chat with the right to change its info (the bot's staff
// may help, as with every command about the chat itself).
func adminOnly(c *core.Ctx) bool {
	return c.BeginChatCommand(core.AuthorizeOpts{Right: rightChangeInfo}) != nil
}

func cmdSetrules(c *core.Ctx) error {
	if !isGroup(c.Msg.Chat.Type) {
		c.Say(c.T("common.this_command_only_works_in_groups"))
		return nil
	}
	if !adminOnly(c) {
		return nil
	}
	body := core.ArgsOf(c.Msg, c.Args)
	replied := core.RepliedMessage(c.Msg)
	media := core.MediaOf(c.Msg)
	if media == nil {
		media = core.MediaOf(replied) // media sent with the command, else the replied-to message's
	}
	if body == "" && replied != nil {
		body = core.TextOf(replied)
	}
	if body == "" && media == nil {
		c.Say(c.T("rules.tell_me_what_the_rules_are"))
		return nil
	}
	if err := c.Validate(body, media != nil); err != nil {
		return sayMarkupError(c, err)
	}
	var mt, mid any
	if media != nil {
		mt, mid = media.Kind, media.FileID
	}
	if _, err := c.App.DB.Exec(c.Ctx, `INSERT INTO rules (chat_id, content, media_type, media_file_id) VALUES ($1, $2, $3, $4)
ON CONFLICT (chat_id) DO UPDATE SET content = EXCLUDED.content, media_type = EXCLUDED.media_type, media_file_id = EXCLUDED.media_file_id`,
		c.Msg.Chat.Id, body, mt, mid); err != nil {
		return err
	}
	c.Say(c.T("rules.the_rules_are_saved_use_rules"))
	return nil
}

// sayMarkupError answers a text that can't be sent; any other error is a real failure.
func sayMarkupError(c *core.Ctx, err error) error {
	var me *markup.Error
	if errors.As(err, &me) {
		c.Say(text.Esc(me.Msg))
		return nil
	}
	return err
}

func cmdResetrules(c *core.Ctx) error {
	if !isGroup(c.Msg.Chat.Type) {
		c.Say(c.T("common.this_command_only_works_in_groups"))
		return nil
	}
	if !adminOnly(c) {
		return nil
	}
	n, err := c.App.DB.Exec(c.Ctx, `DELETE FROM rules WHERE chat_id = $1`, c.Msg.Chat.Id)
	if err != nil {
		return err
	}
	if n > 0 {
		c.Say(c.T("rules.the_rules_are_cleared"))
	} else {
		c.Say(c.T("rules.this_chat_has_no_rules_to"))
	}
	return nil
}

// sendRules sends the rules of chat where the current message was sent. False when the chat has none.
func sendRules(c *core.Ctx, chat *gotgbot.Chat, raw bool) (bool, error) {
	row, err := getRules(c, chat.Id)
	if err != nil || row == nil || row.empty() {
		return false, err
	}
	c.SendSaved(c.User, row.Content, row.MediaType, row.MediaID, core.SendOpts{Raw: raw, Label: c.T("rules.rules"), Chat: chat})
	return true, nil
}

func cmdRules(c *core.Ctx) error {
	if !isGroup(c.Msg.Chat.Type) {
		c.Say(c.T("common.this_command_only_works_in_groups"))
		return nil
	}
	argument := strings.ToLower(strings.TrimSpace(c.Args))
	if argument != "" && argument != "here" && argument != "noformat" {
		c.Say(c.T("rules.use_rules_rules_here_or_rules"))
		return nil
	}
	row, err := getRules(c, c.Msg.Chat.Id)
	if err != nil {
		return err
	}
	if row == nil || row.empty() {
		c.Say(c.T("rules.this_chat_has_no_rules_yet"))
		return nil
	}
	if argument != "" {
		_, err := sendRules(c, &c.Msg.Chat, argument == "noformat")
		return err
	}
	url := "https://t.me/" + c.Bot.Username + "?start=rules_" + strconv.FormatInt(c.Msg.Chat.Id, 10)
	_, err = c.Reply(c.T("rules.click_the_button_below_to_read"), core.Keyboard([]gotgbot.InlineKeyboardButton{
		{Text: c.T("rules.rules_2"), Url: url},
	}))
	return err
}

// watchRulesLink answers a private /start that carries the rules link made by /rules.
func watchRulesLink(c *core.Ctx) (bool, error) {
	if c.Edited || c.Msg.Chat.Type != "private" || c.User == nil || c.Msg.Text == "" {
		return false, nil
	}
	m := deepLinkRE.FindStringSubmatch(c.Msg.Text)
	if m == nil {
		return false, nil
	}
	rulesChat, _ := strconv.ParseInt(m[1], 10, 64)
	chat, err := c.Bot.GetChatWithContext(c.Ctx, rulesChat, nil)
	if err != nil {
		c.Say(c.T("rules.i_can_t_find_that_chat"))
		return true, nil
	}
	status := c.MemberStatus(rulesChat, c.User.Id)
	if !isGroup(chat.Type) || status == "" || status == "left" || status == "kicked" {
		c.Say(c.T("rules.you_are_not_in_that_chat"))
		return true, nil
	}
	plain := &gotgbot.Chat{Id: chat.Id, Type: chat.Type, Title: chat.Title, Username: chat.Username}
	sent, err := sendRules(c, plain, false)
	if err != nil {
		return true, err
	}
	if !sent {
		title := chat.Title
		if title == "" {
			title = c.T("rules.that_chat")
		}
		c.Say(c.T("rules.has_no_rules_yet", core.V{"v": text.Esc(title)}))
	}
	return true, nil
}
