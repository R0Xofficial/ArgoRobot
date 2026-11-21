package modules

import (
	"log/slog"

	"argo/internal/core"
	"argo/internal/text"
)

// Delete messages in bulk (modules/purges.py).

const (
	rightDelete   = "can_delete_messages"
	rightsRefused = "purges.rights_refused"
	maxPurge      = 1000 // messages cleared by one command
	maxReason     = 500  // characters of a reason that are shown
	purgeBatch    = 100  // Telegram deletes at most this many messages per request
)

func registerPurges(r *core.Router) {
	r.Command(cmdDel, "del", "delete")
	r.Command(func(c *core.Ctx) error { return purge(c, false) }, "purge")
	r.Command(func(c *core.Ctx) error { return purge(c, true) }, "spurge")
}

func beginPurge(c *core.Ctx) *core.Actor {
	return c.BeginChatCommand(core.AuthorizeOpts{Right: rightDelete})
}

func badRequest(err error) bool {
	tg := core.TgError(err)
	return tg != nil && tg.Code == 400
}

func cmdDel(c *core.Ctx) error {
	if beginPurge(c) == nil {
		return nil
	}
	target := c.RequireReply()
	if target == nil {
		return nil
	}
	_, err := c.Bot.DeleteMessageWithContext(c.Ctx, c.Msg.Chat.Id, target.MessageId, nil)
	if err = c.Perform(err, rightsRefused); err != nil {
		var me *core.ModerationError
		if asModeration(err, &me) {
			c.Deny(err)
			return nil
		}
		if badRequest(err) {
			c.Say(c.T("purges.i_can_t_delete_that_message"))
			return nil
		}
		return err
	}
	_, _ = c.Bot.DeleteMessageWithContext(c.Ctx, c.Msg.Chat.Id, c.Msg.MessageId, nil) // the command itself
	return nil
}

func purge(c *core.Ctx, silent bool) error {
	if beginPurge(c) == nil {
		return nil
	}
	target := c.RequireReply()
	if target == nil {
		return nil
	}
	first, last := target.MessageId, c.Msg.MessageId
	if first >= last {
		c.Say(c.T("purges.reply_to_a_message_that_came"))
		return nil
	}
	count := last - first + 1
	if count > maxPurge {
		c.Say(c.T("purges.that_is_too_many_messages_i", core.V{"MAX_PURGE": maxPurge}))
		return nil
	}
	ids := make([]int64, 0, count)
	for id := first; id <= last; id++ {
		ids = append(ids, id)
	}
	for start := 0; start < len(ids); start += purgeBatch {
		end := min(start+purgeBatch, len(ids))
		_, err := c.Bot.DeleteMessagesWithContext(c.Ctx, c.Msg.Chat.Id, ids[start:end], nil)
		if err = c.Perform(err, rightsRefused); err != nil {
			var me *core.ModerationError
			if asModeration(err, &me) {
				c.Deny(err)
				return nil
			}
			if badRequest(err) {
				slog.Info("Purge failed", "chat", c.Msg.Chat.Id, "err", err)
				c.Say(c.T("purges.i_couldn_t_delete_those_messages"))
				return nil
			}
			return err
		}
	}
	if silent {
		return nil
	}
	msg := c.T("purges.purge_complete_up_to_messages_deleted", core.V{"count": count})
	if reason := []rune(trimSpace(c.Args)); len(reason) > 0 {
		if len(reason) > maxReason {
			reason = reason[:maxReason]
		}
		msg += c.T("purges.reason", core.V{"v": text.Esc(string(reason))})
	}
	_, err := c.SendPlain(msg)
	return err
}
