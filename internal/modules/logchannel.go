package modules

import (
	"strconv"
	"strings"

	"argo/internal/core"
	"argo/internal/text"
)

// Log channel: a copy of a chat's moderation actions in a chat of the admins' choice.

func registerLogchannel(r *core.Router) {
	r.Command(cmdSetlog, "setlog")
	r.Command(cmdUnsetlog, "unsetlog")
	r.Command(cmdLogchannel, "logchannel")
}

// wantedLog is the log chat the person named: an id, a @username, or the channel of a forwarded post.
func wantedLog(c *core.Ctx) string {
	word := ""
	if f := strings.Fields(c.Args); len(f) > 0 {
		word = f[0]
	}
	if word != "" && isDigits(strings.TrimPrefix(word, "-")) && strings.TrimPrefix(word, "-") != "" {
		return word
	}
	if strings.HasPrefix(word, "@") && len(word) > 1 {
		return word
	}
	if replied := core.RepliedMessage(c.Msg); replied != nil && replied.ForwardOrigin != nil {
		if origin := replied.ForwardOrigin.MergeMessageOrigin(); origin.Chat != nil {
			return strconv.FormatInt(origin.Chat.Id, 10)
		}
	}
	return ""
}

func logBegin(c *core.Ctx) *core.Actor {
	if !isGroup(c.Msg.Chat.Type) {
		c.Say(c.T("common.this_command_only_works_in_groups"))
		return nil
	}
	return c.BeginChatCommand(core.AuthorizeOpts{Right: rightChangeInfo})
}

func cmdSetlog(c *core.Ctx) error {
	actor := logBegin(c)
	if actor == nil {
		return nil
	}
	wanted := wantedLog(c)
	if wanted == "" {
		c.Say(c.T("logchannel.tell_me_where_to_log_setlog"))
		return nil
	}
	target, err := c.GetChatRef(wanted)
	if err != nil {
		c.Say(c.T("logchannel.i_can_t_find_that_chat"))
		return nil
	}
	switch {
	case target.Type != "group" && target.Type != "supergroup" && target.Type != "channel":
		c.Say(c.T("logchannel.the_log_has_to_be_a"))
		return nil
	case target.Id == c.Msg.Chat.Id:
		c.Say(c.T("logchannel.a_chat_can_t_be_its"))
		return nil
	}
	// The person must be an admin there, or anyone could point a log at someone else's chat.
	if actor.ID == 0 || !c.IsChatAdmin(target.Id, actor.ID) {
		c.Say(c.T("logchannel.you_have_to_be_an_admin"))
		return nil
	}
	title := c.Msg.Chat.Title
	if title == "" {
		title = c.T("common.this_chat")
	}
	if err := c.SayTo(target.Id, c.T("logchannel.this_chat_is_now_the_log", core.V{"title": text.Esc(title)})); err != nil {
		c.Say(c.T("logchannel.i_can_t_write_in_that"))
		return nil
	}
	if err := c.App.SetLog(c.Ctx, c.Msg.Chat.Id, target.Id, actor.ID); err != nil {
		return err
	}
	name := target.Title
	if name == "" {
		name = strconv.FormatInt(target.Id, 10)
	}
	c.Say(c.T("logchannel.done_this_chat_s_log_goes", core.V{"v": text.Esc(name)}))
	return nil
}

func cmdUnsetlog(c *core.Ctx) error {
	if logBegin(c) == nil {
		return nil
	}
	removed, err := c.App.UnsetLog(c.Ctx, c.Msg.Chat.Id)
	if err != nil {
		return err
	}
	if !removed {
		c.Say(c.T("logchannel.this_chat_has_no_log"))
		return nil
	}
	c.Say(c.T("logchannel.this_chat_is_no_longer_logged"))
	return nil
}

func cmdLogchannel(c *core.Ctx) error {
	if logBegin(c) == nil {
		return nil
	}
	target, err := c.App.LogChat(c.Ctx, c.Msg.Chat.Id)
	if err != nil {
		return err
	}
	if target == 0 {
		c.Say(c.T("logchannel.this_chat_has_no_log_set"))
		return nil
	}
	name := c.T("logchannel.a_chat_i_can_no_longer")
	if chat, err := c.Bot.GetChatWithContext(c.Ctx, target, nil); err == nil {
		t := chat.Title
		if t == "" {
			t = strconv.FormatInt(target, 10)
		}
		name = text.Esc(t)
	}
	c.Say(c.T("logchannel.this_chat_is_logged_in", core.V{"name": name, "target": target}))
	return nil
}
