package modules

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/core"
	"argo/internal/text"
)

// Zombies: remove deleted accounts from a chat (modules/zombies.py). With API_ID and API_HASH set the members of the
// chat are listed through MTProto, so deleted accounts nobody has seen write are found too. Without them (or when the
// list can't be had) the sweep goes through the people the bot has seen in the chat and asks Telegram about each one.

const (
	deletedName = "Deleted Account"
	maxChecked  = 5000
)

// zombiePause is the pause between Telegram calls, to stay far from the flood limits.
var zombiePause = 60 * time.Millisecond

var (
	zombiesRunning sync.Map // chat id -> struct{}
	zombieTasks    sync.WaitGroup
)

func registerZombies(r *core.Router) { r.Command(cmdZombies, "zombies") }

func isDeletedName(first string) bool {
	return strings.EqualFold(strings.TrimSpace(first), deletedName)
}

func zombiePresent(m *gotgbot.MergedChatMember) bool {
	if m.Status == "restricted" {
		return m.IsMember
	}
	return m.Status == "member"
}

// scanDeleted: which of the people I know are deleted accounts still in the chat, going by what Telegram says about each.
func scanDeleted(ctx context.Context, c *core.Ctx, chatID int64, ids []int64) []int64 {
	var deleted []int64
	unreadable, gone := 0, 0
	for _, id := range ids {
		cm, err := c.Bot.GetChatMemberWithContext(ctx, chatID, id, nil)
		if err != nil {
			if wait := retryAfter(err); wait > 0 {
				sleepFor(wait)
				continue
			}
			unreadable++ // e.g. an account Telegram no longer returns
			sleepFor(zombiePause)
			continue
		}
		m := cm.MergeChatMember()
		present := zombiePresent(&m)
		if !present {
			gone++ // known to me, but no longer in the chat
		}
		name := strings.TrimSpace(m.User.FirstName)
		if !isDeletedName(name) && (name == "" || strings.Contains(strings.ToLower(name), "delet")) {
			slog.Info("Zombies: odd account", "user", id, "chat", chatID, "name", name, "status", m.Status)
		}
		if isDeletedName(name) && present {
			deleted = append(deleted, id)
		}
		sleepFor(zombiePause)
	}
	if unreadable > 0 || gone > 0 {
		slog.Info("Zombies: checked", "chat", chatID, "checked", len(ids), "unreadable", unreadable, "gone", gone, "found", len(deleted))
	}
	return deleted
}

func sweepZombies(c *core.Ctx, progress *gotgbot.Message, ids []int64) {
	ctx := context.Background()
	chatID := c.Msg.Chat.Id
	found, removed, failed := 0, 0, 0
	var reasons []string
	seen := map[string]bool{}
	func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("Zombie sweep failed", "chat", chatID, "panic", r)
			}
		}()
		var deleted []int64
		if listed, ok := c.App.ChatMembers(ctx, chatID); ok {
			for _, m := range listed {
				if m.Deleted {
					deleted = append(deleted, m.ID)
				}
			}
			slog.Info("Zombies: members listed", "chat", chatID, "members", len(listed), "deleted", len(deleted))
		} else {
			deleted = scanDeleted(ctx, c, chatID, ids)
		}
		found = len(deleted)
		for _, id := range deleted {
			_, err := c.Bot.BanChatMemberWithContext(ctx, chatID, id, nil)
			if err == nil {
				_, err = c.Bot.UnbanChatMemberWithContext(ctx, chatID, id, &gotgbot.UnbanChatMemberOpts{OnlyIfBanned: true})
			}
			if err = c.Perform(err); err == nil {
				removed++
			} else {
				failed++
				reason := err.Error()
				var me *core.ModerationError
				if errors.As(err, &me) {
					reason = me.Msg
				} else if tg := core.TgError(err); tg != nil {
					reason = tg.Description
				}
				if !seen[reason] {
					seen[reason] = true
					reasons = append(reasons, reason)
				}
				slog.Warn("Zombies: could not remove", "user", id, "chat", chatID, "reason", reason)
				if failed >= 3 && removed == 0 {
					break // the same refusal every time: stop instead of hammering Telegram
				}
			}
			sleepFor(zombiePause)
		}
	}()
	zombiesRunning.Delete(chatID)

	var msg string
	if found == 0 {
		msg = c.T("zombies.no_deleted_accounts_found")
	} else {
		msg = c.T("zombies.removed_deleted_account", core.V{"removed": removed, "noun": c.L.PluralTag("zombies.noun", removed)})
		if failed > 0 {
			escaped := make([]string, len(reasons))
			for i, r := range reasons {
				escaped[i] = text.Esc(r)
			}
			msg += c.T("zombies.i_couldn_t_remove", core.V{"failed": failed}) + strings.Join(escaped, "; ")
		}
	}
	if progress == nil || c.EditSent(progress, msg, nil) != nil {
		_ = c.SayTo(chatID, msg)
	}
}

func cmdZombies(c *core.Ctx) error {
	if c.BeginChatCommand(core.AuthorizeOpts{}) == nil {
		return nil
	}
	chatID := c.Msg.Chat.Id
	if _, busy := zombiesRunning.LoadOrStore(chatID, struct{}{}); busy {
		c.Say(c.T("zombies.i_m_already_checking_this_chat"))
		return nil
	}
	ids, err := c.App.UsersIn(c.Ctx, chatID)
	if err != nil {
		zombiesRunning.Delete(chatID)
		return err
	}
	if len(ids) > maxChecked {
		ids = ids[:maxChecked]
	}
	if len(ids) == 0 && !c.App.MTProtoConfigured() {
		zombiesRunning.Delete(chatID)
		c.Say(c.T("zombies.i_haven_t_seen_anyone_in"))
		return nil
	}
	progress, _ := c.ReplyText(c.T("zombies.looking_for_deleted_accounts_this_can"))
	zombieTasks.Add(1)
	go func() {
		defer zombieTasks.Done()
		detached := *c // the request is over when this runs: its context is cancelled
		detached.Ctx = context.Background()
		sweepZombies(&detached, progress, ids)
	}()
	return nil
}
