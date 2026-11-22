package core

import (
	"github.com/PaulSonOfLars/gotgbot/v2"
)

// The two switches for the linked channel of a discussion group (modules/admin.go). Telegram forwards every post of the
// channel into the chat and pins it by itself; the chat's admins can have the bot delete those posts (/cleanlinked) or
// unpin them again (/antichannelpin). Like cleanService this runs once everything else has dealt with the message.
func (r *Router) linkedChannel(c *Ctx) {
	m := c.Msg
	if m == nil || m.Chat.Type != "supergroup" {
		return
	}
	forwarded := m.IsAutomaticForward
	var pinned int64
	if pm, ok := m.PinnedMessage.(gotgbot.Message); ok && pm.IsAutomaticForward {
		pinned = pm.MessageId
	}
	if !forwarded && pinned == 0 || !c.active("admin") {
		return
	}
	if forwarded && r.app.Setting(c.Ctx, "clean_linked", m.Chat.Id) {
		_, _ = c.Bot.DeleteMessageWithContext(c.Ctx, m.Chat.Id, m.MessageId, nil)
		return
	}
	if pinned != 0 && r.app.Setting(c.Ctx, "anti_channel_pin", m.Chat.Id) {
		_, _ = c.Bot.UnpinChatMessageWithContext(c.Ctx, m.Chat.Id, &gotgbot.UnpinChatMessageOpts{MessageId: &pinned})
	}
}
