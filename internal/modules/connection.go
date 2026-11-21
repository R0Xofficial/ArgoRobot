package modules

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/core"
	"argo/internal/text"
)

// Managing a group from a private chat.

func registerConnection(r *core.Router) {
	r.Command(cmdConnect, "connect")
	r.Command(cmdDisconnect, "disconnect")
	r.Command(cmdConnection, "connection")
}

func groupName(title string, chatID int64) string {
	if title == "" {
		title = strconv.FormatInt(chatID, 10)
	}
	return "<b>" + text.Esc(title) + "</b>"
}

var usernameRE = regexp.MustCompile(`^\w+$`)

// connectReference is a chat id (int64) or "@username" from what the person typed, nil when it is neither.
func connectReference(arg string) any {
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		return nil
	}
	word := fields[0]
	if digits := strings.TrimLeft(word, "-"); digits != "" && strings.Trim(digits, "0123456789") == "" {
		if n, err := strconv.ParseInt(word, 10, 64); err == nil {
			return n
		}
	}
	name := strings.TrimLeft(word, "@")
	if name != "" && usernameRE.MatchString(name) {
		return "@" + name
	}
	return nil
}

// connectAndTell connects the sender to a group and tells them how it went.
func connectAndTell(c *core.Ctx, reference any) error {
	cn, err := c.Connect(c.User.Id, reference)
	if err != nil {
		var ce *core.ConnectError
		if errors.As(err, &ce) {
			c.Say(ce.Msg)
			return nil
		}
		return err
	}
	c.Say(c.T("connection.you_are_connected_to_its_admin", core.V{"group_name": groupName(cn.Title, cn.ChatID)}))
	return nil
}

func cmdConnect(c *core.Ctx) error {
	chat := c.Msg.Chat
	if core.IsGroup(chat) {
		if c.BeginChatCommand(core.AuthorizeOpts{Right: "can_manage_chat"}) == nil {
			return nil
		}
		url := fmt.Sprintf("https://t.me/%s?start=connect_%d", c.Bot.Username, chat.Id)
		markup := &gotgbot.InlineKeyboardMarkup{InlineKeyboard: [][]gotgbot.InlineKeyboardButton{{
			{Text: c.T("connection.connect"), Url: url},
		}}}
		_, err := c.Reply(c.T("connection.tap_the_button_to_connect_your"), markup)
		return err
	}
	if chat.Type != "private" {
		c.Say(c.T("connection.use_this_in_a_group_or"))
		return nil
	}
	arg := strings.TrimSpace(c.Args)
	if arg == "" {
		cn, err := c.App.GetConnection(c.Ctx, c.User.Id)
		if err != nil {
			return err
		}
		if cn != nil {
			c.Say(c.T("connection.you_are_connected_to_use_disconnect", core.V{"group_name": groupName(cn.Title, cn.ChatID)}))
		} else {
			c.Say(c.T("connection.use_connect_lt_chat_id_or"))
		}
		return nil
	}
	ref := connectReference(arg)
	if ref == nil {
		c.Say(c.T("connection.give_me_the_group_s_id"))
		return nil
	}
	return connectAndTell(c, ref)
}

func cmdDisconnect(c *core.Ctx) error {
	if c.Msg.Chat.Type != "private" {
		c.Say(c.T("connection.send_this_to_me_in_a"))
		return nil
	}
	cn, err := c.App.GetConnection(c.Ctx, c.User.Id)
	if err != nil {
		return err
	}
	if cn == nil {
		c.Say(c.T("connection.you_are_not_connected_to_a"))
		return nil
	}
	if ok, err := c.App.ClearConnection(c.Ctx, c.User.Id); err != nil {
		return err
	} else if !ok {
		c.Say(c.T("connection.you_are_not_connected_to_a"))
		return nil
	}
	c.Say(c.T("connection.you_are_no_longer_connected_to", core.V{"group_name": groupName(cn.Title, cn.ChatID)}))
	return nil
}

func cmdConnection(c *core.Ctx) error {
	if c.Msg.Chat.Type != "private" {
		c.Say(c.T("connection.send_this_to_me_in_a"))
		return nil
	}
	cn, err := c.App.GetConnection(c.Ctx, c.User.Id)
	if err != nil {
		return err
	}
	if cn == nil {
		c.Say(c.T("connection.you_are_not_connected_to_a_2"))
		return nil
	}
	c.Say(c.T("connection.you_are_connected_to", core.V{"group_name": groupName(cn.Title, cn.ChatID), "chat_id": cn.ChatID}))
	return nil
}

var connectLinkRE = regexp.MustCompile(`(?i)^/start(?:@\w+)?\s+connect_(-?\d+)\s*$`)

// watchConnectLink handles t.me/<bot>?start=connect_<chat id>, the button /connect sends in a group.
func watchConnectLink(c *core.Ctx) (bool, error) {
	if c.Edited || c.Msg == nil || c.User == nil || c.Msg.Chat.Type != "private" || c.Msg.Text == "" {
		return false, nil
	}
	m := connectLinkRE.FindStringSubmatch(c.Msg.Text)
	if m == nil {
		return false, nil
	}
	id, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return false, nil
	}
	// The deep link is handled by the always-on root module, which answers in English; the person speaks the chat's language.
	if l, err := c.App.ChatLanguage(c.Ctx, c.Msg.Chat.Id); err == nil {
		c.L = l
	}
	return true, connectAndTell(c, id)
}
