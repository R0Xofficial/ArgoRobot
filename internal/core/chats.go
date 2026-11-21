package core

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

// Chats the bot is in, and who was seen where (core/chats.py in the Python version).

// chatTables is everything the bot keeps for one chat. A chat the bot no longer belongs to has no use for any of
// it, so removeChat deletes all of it. The list is the Python version's CHAT_TABLES.
var chatTables = []string{
	"user_chats", "gban_bans", "captcha_pending", "captcha_settings", "warns", "warn_settings", "chat_logs",
	"disabled_commands", "connections", "chat_settings", "flood_settings", "blacklist", "blacklist_settings",
	"greetings", "fed_chats", "fed_applied", "antiraid", "nightmode", "locks", "lock_settings", "lock_allowlist",
	"rules", "join_blacklist", "notes", "filters", "approvals",
}

// migratedTables follow a chat to its new id when a group becomes a supergroup.
var migratedTables = []string{
	"chat_settings", "warn_settings", "warns", "approvals", "gban_bans", "flood_settings", "blacklist",
	"blacklist_settings", "filters", "greetings", "join_blacklist", "notes", "rules", "locks", "lock_settings",
	"lock_allowlist", "fed_chats", "fed_applied", "disabled_commands", "connections", "captcha_settings",
	"captcha_pending", "chat_logs", "antiraid", "nightmode",
}

type chatKey struct {
	kind, title, username string
	admin                 *bool
	at                    time.Time
}

type chatsCache struct {
	chats    cache[int64, chatKey]
	presence cache[[2]int64, time.Time]
}

func isGroup(kind string) bool { return kind == "group" || kind == "supergroup" }

func isChatKind(kind string) bool { return isGroup(kind) || kind == "channel" }

func (a *App) rememberChat(ctx context.Context, chat gotgbot.Chat, botAdmin *bool) error {
	if !isChatKind(chat.Type) {
		return nil
	}
	if c, ok := a.chats.chats.get(chat.Id); ok && c.kind == chat.Type && c.title == chat.Title && c.username == chat.Username &&
		(botAdmin == nil || (c.admin != nil && *c.admin == *botAdmin)) && time.Since(c.at) < refreshEvery {
		return nil
	}
	_, err := a.DB.Exec(ctx, `
INSERT INTO chats (chat_id, type, title, username, bot_admin)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (chat_id) DO UPDATE SET
    type = EXCLUDED.type,
    title = EXCLUDED.title,
    username = EXCLUDED.username,
    bot_admin = COALESCE(EXCLUDED.bot_admin, chats.bot_admin),
    last_seen = now()`, chat.Id, chat.Type, nullable(chat.Title), nullable(chat.Username), botAdmin)
	if err == nil {
		a.chats.chats.put(chat.Id, chatKey{chat.Type, chat.Title, chat.Username, botAdmin, time.Now()})
	}
	return err
}

func (a *App) rememberPresence(ctx context.Context, userID int64, chat gotgbot.Chat) error {
	if placeholderSenders[userID] || !isGroup(chat.Type) {
		return nil
	}
	key := [2]int64{userID, chat.Id}
	if at, ok := a.chats.presence.get(key); ok && time.Since(at) < refreshEvery {
		return nil
	}
	_, err := a.DB.Exec(ctx, `INSERT INTO user_chats (user_id, chat_id) VALUES ($1, $2)
ON CONFLICT (user_id, chat_id) DO UPDATE SET last_seen = now()`, userID, chat.Id)
	if err == nil {
		a.chats.presence.put(key, time.Now())
	}
	return err
}

// removeChat: the bot no longer belongs to a chat; forget it and delete everything kept for it, in one transaction.
func (a *App) removeChat(ctx context.Context, chatID int64) error {
	a.forgetChat(chatID)
	tx, err := a.DB.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, table := range append(append([]string{}, chatTables...), "chats") {
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", "public."+table).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			continue
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE chat_id = $1", table), chatID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// migrateChat: a group became a supergroup. Its id changed, but it is the same chat.
func (a *App) migrateChat(ctx context.Context, oldID, newID int64) error {
	a.forgetChat(oldID)
	a.forgetChat(newID)
	if _, err := a.DB.Exec(ctx, `
INSERT INTO chats (chat_id, type, title, username, bot_admin, first_seen)
SELECT $2, 'supergroup', title, username, bot_admin, first_seen FROM chats WHERE chat_id = $1
ON CONFLICT (chat_id) DO NOTHING`, oldID, newID); err != nil {
		return err
	}
	if _, err := a.DB.Exec(ctx, `UPDATE user_chats SET chat_id = $2 WHERE chat_id = $1
AND user_id NOT IN (SELECT user_id FROM user_chats WHERE chat_id = $2)`, oldID, newID); err != nil {
		return err
	}
	for _, table := range migratedTables {
		var exists bool
		if err := a.DB.Row(ctx, "SELECT to_regclass($1) IS NOT NULL", []any{"public." + table}, &exists); err != nil {
			return err
		}
		if !exists {
			continue
		}
		// A row of the new id (made before the move) would make the move collide: the old data wins.
		if _, err := a.DB.Exec(ctx, fmt.Sprintf("UPDATE %s SET chat_id = $2 WHERE chat_id = $1", table), oldID, newID); err != nil {
			slog.Warn("Could not move a table to the new chat id", "table", table, "err", err)
		}
	}
	a.forgetChat(oldID)
	a.forgetChat(newID)
	return a.removeChat(ctx, oldID)
}

// forgetChat drops the in-memory copies, so a chat the bot rejoins is written again.
func (a *App) forgetChat(chatID int64) {
	a.chats.chats.drop(chatID)
	a.langs.drop(chatID)
	a.settings.forget(chatID)
	a.logs.drop(chatID)
	a.chats.presence.mu.Lock()
	for k := range a.chats.presence.m {
		if k[1] == chatID {
			delete(a.chats.presence.m, k)
		}
	}
	a.chats.presence.mu.Unlock()
}

// recordUpdate is the UsersMiddleware: it writes down the users and chats an update tells about.
// It never fails an update.
func (a *App) recordUpdate(c *Ctx) {
	ctx, cancel := context.WithTimeout(c.Ctx, 5*time.Second)
	defer cancel()
	u := c.Update
	removed := map[int64]bool{}
	var chats []gotgbot.Chat
	var botAdmin = map[int64]*bool{}

	if e := u.MyChatMember; e != nil {
		status := e.NewChatMember.GetStatus()
		if status == "left" || status == "kicked" {
			removed[e.Chat.Id] = true
			logErr("Could not remove a chat", a.removeChat(ctx, e.Chat.Id))
		} else {
			admin := status == "administrator" || status == "creator"
			botAdmin[e.Chat.Id] = &admin
		}
	}
	if m := u.Message; m != nil && m.MigrateToChatId != 0 {
		removed[m.Chat.Id] = true
		logErr("Could not migrate a chat", a.migrateChat(ctx, m.Chat.Id, m.MigrateToChatId))
	}

	for _, user := range collectUsers(u) {
		logErr("Could not store a user", a.Remember(ctx, user))
	}
	switch {
	case u.Message != nil:
		chats = append(chats, u.Message.Chat)
	case u.EditedMessage != nil:
		chats = append(chats, u.EditedMessage.Chat)
	case u.MyChatMember != nil:
		chats = append(chats, u.MyChatMember.Chat)
	case u.ChatMember != nil:
		chats = append(chats, u.ChatMember.Chat)
	case u.ChatJoinRequest != nil:
		chats = append(chats, u.ChatJoinRequest.Chat)
	case u.CallbackQuery != nil:
		if m, ok := u.CallbackQuery.Message.(gotgbot.Message); ok {
			chats = append(chats, m.Chat)
		}
	}
	for _, chat := range chats {
		if !removed[chat.Id] {
			logErr("Could not store a chat", a.rememberChat(ctx, chat, botAdmin[chat.Id]))
		}
	}
	for _, p := range presence(u) {
		if !removed[p.chat.Id] {
			logErr("Could not store a presence", a.rememberPresence(ctx, p.user, p.chat))
		}
	}
}

type seen struct {
	user int64
	chat gotgbot.Chat
}

// presence: people certain to have been in a group, going by an update: whoever wrote, was replied to, joined or left.
func presence(u *gotgbot.Update) []seen {
	var out []seen
	add := func(user *gotgbot.User, chat gotgbot.Chat) {
		if user != nil {
			out = append(out, seen{user.Id, chat})
		}
	}
	for _, m := range []*gotgbot.Message{u.Message, u.EditedMessage} {
		if m == nil {
			continue
		}
		add(m.From, m.Chat)
		for i := range m.NewChatMembers {
			add(&m.NewChatMembers[i], m.Chat)
		}
		add(m.LeftChatMember, m.Chat)
		if m.ReplyToMessage != nil {
			add(m.ReplyToMessage.From, m.Chat)
		}
	}
	if cb := u.CallbackQuery; cb != nil {
		if m, ok := cb.Message.(gotgbot.Message); ok {
			add(&cb.From, m.Chat)
		}
	}
	for _, e := range []*gotgbot.ChatMemberUpdated{u.ChatMember, u.MyChatMember} {
		if e != nil {
			add(&e.From, e.Chat)
			nu := e.NewChatMember.GetUser()
			add(&nu, e.Chat)
		}
	}
	return out
}

// collectUsers: every user an update tells us about, without duplicates.
func collectUsers(u *gotgbot.Update) []*gotgbot.User {
	found := map[int64]*gotgbot.User{}
	var order []int64
	add := func(user *gotgbot.User) {
		if user == nil || placeholderSenders[user.Id] {
			return
		}
		if _, ok := found[user.Id]; !ok {
			found[user.Id] = user
			order = append(order, user.Id)
		}
	}
	var fromMessage func(m *gotgbot.Message, depth int)
	fromMessage = func(m *gotgbot.Message, depth int) {
		add(m.From)
		add(m.ViaBot)
		for i := range m.NewChatMembers {
			add(&m.NewChatMembers[i])
		}
		add(m.LeftChatMember)
		for _, entities := range [][]gotgbot.MessageEntity{m.Entities, m.CaptionEntities} {
			for i := range entities {
				if entities[i].Type == "text_mention" {
					add(entities[i].User)
				}
			}
		}
		if depth > 0 {
			if m.ReplyToMessage != nil {
				fromMessage(m.ReplyToMessage, depth-1)
			}
		}
	}
	for _, m := range []*gotgbot.Message{u.Message, u.EditedMessage} {
		if m != nil {
			fromMessage(m, 1)
		}
	}
	if cb := u.CallbackQuery; cb != nil {
		add(&cb.From)
	}
	for _, e := range []*gotgbot.ChatMemberUpdated{u.MyChatMember, u.ChatMember} {
		if e != nil {
			add(&e.From)
			ou, nu := e.OldChatMember.GetUser(), e.NewChatMember.GetUser()
			add(&ou)
			add(&nu)
		}
	}
	if r := u.ChatJoinRequest; r != nil {
		add(&r.From)
	}
	out := make([]*gotgbot.User, 0, len(order))
	for _, id := range order {
		out = append(out, found[id])
	}
	return out
}
