package core

import (
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/i18n"
	"argo/internal/text"
)

// Punishments applied automatically by modules (core/enforce.py): one place decides what "mute", "tban" and the
// rest mean, so every module words and performs them the same way.

var EnforceModes = []string{"ban", "kick", "mute", "tban", "tmute"}

func IsEnforceMode(mode string) bool {
	for _, m := range EnforceModes {
		if m == mode {
			return true
		}
	}
	return false
}

func IsTimedMode(mode string) bool { return mode == "tban" || mode == "tmute" }

// Punish applies mode to a user. It returns what was done, e.g. 'muted' or 'banned for 2 hours'.
func (c *Ctx) Punish(chatID, userID int64, mode string, seconds int) (string, error) {
	var until int64
	if IsTimedMode(mode) {
		until = time.Now().Add(time.Duration(seconds) * time.Second).Unix()
	}
	switch mode {
	case "ban", "kick":
		if err := c.Perform(wrap(c.Bot.BanChatMemberWithContext(c.Ctx, chatID, userID, nil))); err != nil {
			return "", err
		}
		if mode == "kick" {
			if err := c.Perform(wrap(c.Bot.UnbanChatMemberWithContext(c.Ctx, chatID, userID, &gotgbot.UnbanChatMemberOpts{OnlyIfBanned: true}))); err != nil {
				return "", err
			}
		}
	case "tban":
		if err := c.Perform(wrap(c.Bot.BanChatMemberWithContext(c.Ctx, chatID, userID, &gotgbot.BanChatMemberOpts{UntilDate: until}))); err != nil {
			return "", err
		}
	default:
		opts := &gotgbot.RestrictChatMemberOpts{}
		if until != 0 {
			opts.UntilDate = until
		}
		if err := c.Perform(wrap(c.Bot.RestrictChatMemberWithContext(c.Ctx, chatID, userID, gotgbot.ChatPermissions{}, opts))); err != nil {
			return "", err
		}
	}
	return Outcome(c.L, mode, seconds), nil
}

// wrap drops the bool of a Telegram call: only its error matters.
func wrap(_ bool, err error) error { return err }

// Outcome is what mode did, as text in a language: 'muted' or 'banned for 2 hours'.
func Outcome(l i18n.Lang, mode string, seconds int) string {
	switch {
	case mode == "ban":
		return l.T("enforce.banned")
	case mode == "kick":
		return l.T("enforce.kicked")
	case mode == "tban":
		return l.T("enforce.banned_for", V{"duration_text": text.DurationText(l, seconds)})
	case IsTimedMode(mode) && seconds != 0:
		return l.T("enforce.muted_for", V{"duration_text": text.DurationText(l, seconds)})
	}
	return l.T("enforce.muted")
}
