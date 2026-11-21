package core

import (
	"context"
	"log/slog"
	"strings"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/text"
)

// Managing a group from a private chat.
//
// An admin connects their private chat with the bot to one group. From then on the group's admin commands they send
// privately act on that group, and the bot answers privately. Nothing about their rights changes: every command still
// checks the person against the group itself.

// Modules whose commands are about running the group.
var connectable = map[string]bool{
	"bans": true, "mutes": true, "warns": true, "locks": true, "blacklists": true, "filters": true, "notes": true,
	"greetings": true, "rules": true, "approves": true, "promote": true, "antiflood": true, "joinblacklists": true,
	"admin": true, "captcha": true,
}

// Commands of those modules that don't make sense from a private chat: they delete the command itself, act on
// whoever sent it, or show a saved message that would then land in the group.
var notRemote = map[string]bool{
	"sban": true, "dban": true, "smute": true, "stmute": true, "stempmute": true, "sunmute": true, "dwarn": true,
	"kickme": true, "punchme": true, "get": true, "rules": true, "welcome": true, "goodbye": true,
}

// Connection is the group a person is connected to.
type Connection struct {
	ChatID   int64
	Title    string
	ChatType string
}

// ConnectError says why a connection could not be made (a sentence for people).
type ConnectError struct{ Msg string }

func (e *ConnectError) Error() string { return e.Msg }

// GetConnection is the group this person is connected to, or nil.
func (a *App) GetConnection(ctx context.Context, userID int64) (*Connection, error) {
	if v, ok := a.conns.get(userID); ok {
		return v, nil
	}
	var cn Connection
	var title *string
	found, err := a.DB.Found(ctx, `SELECT chat_id, title, chat_type FROM connections WHERE user_id = $1`, []any{userID}, &cn.ChatID, &title, &cn.ChatType)
	if err != nil {
		return nil, err
	}
	var out *Connection
	if found {
		cn.Title = deref(title)
		out = &cn
	}
	a.conns.put(userID, out)
	return out, nil
}

func (a *App) saveConnection(ctx context.Context, userID, chatID int64, title, chatType string) (*Connection, error) {
	if _, err := a.DB.Exec(ctx, `INSERT INTO connections (user_id, chat_id, title, chat_type) VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id) DO UPDATE SET chat_id = EXCLUDED.chat_id, title = EXCLUDED.title, chat_type = EXCLUDED.chat_type, connected_at = now()`,
		userID, chatID, nullable(title), chatType); err != nil {
		return nil, err
	}
	cn := &Connection{chatID, title, chatType}
	a.conns.put(userID, cn)
	return cn, nil
}

// ClearConnection disconnects. False when there was no connection.
func (a *App) ClearConnection(ctx context.Context, userID int64) (bool, error) {
	cn, err := a.GetConnection(ctx, userID)
	if err != nil || cn == nil {
		return false, err
	}
	if _, err := a.DB.Exec(ctx, `DELETE FROM connections WHERE user_id = $1`, userID); err != nil {
		return false, err
	}
	a.conns.put(userID, nil)
	return true, nil
}

// DropChatConnections: nobody stays connected to a chat the bot has left or that turned connections off. It returns
// who was.
func (a *App) DropChatConnections(ctx context.Context, chatID int64) ([]int64, error) {
	rows, err := a.DB.Pool.Query(ctx, `SELECT user_id FROM connections WHERE chat_id = $1`, chatID)
	if err != nil {
		return nil, err
	}
	var people []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		people = append(people, id)
	}
	rows.Close()
	if _, err := a.DB.Exec(ctx, `DELETE FROM connections WHERE chat_id = $1`, chatID); err != nil {
		return nil, err
	}
	a.dropConnectionsCache(chatID)
	return people, nil
}

// OpenConnection is a connection with the person it belongs to.
type OpenConnection struct {
	UserID, ChatID int64
	Title          string
}

// DropAllConnections ends every connection (the module was turned off for the whole bot) and returns the open ones.
func (a *App) DropAllConnections(ctx context.Context) ([]OpenConnection, error) {
	rows, err := a.DB.Pool.Query(ctx, `SELECT user_id, chat_id, title FROM connections`)
	if err != nil {
		return nil, err
	}
	var out []OpenConnection
	for rows.Next() {
		var o OpenConnection
		var title *string
		if err := rows.Scan(&o.UserID, &o.ChatID, &title); err != nil {
			rows.Close()
			return nil, err
		}
		o.Title = deref(title)
		out = append(out, o)
	}
	rows.Close()
	if _, err := a.DB.Exec(ctx, `DELETE FROM connections`); err != nil {
		return nil, err
	}
	a.conns.mu.Lock()
	a.conns.m = nil
	a.conns.mu.Unlock()
	return out, nil
}

// dropConnectionsCache forgets cached connections to a chat (the rows are moved when a group changes its id).
func (a *App) dropConnectionsCache(chatID int64) {
	a.conns.mu.Lock()
	defer a.conns.mu.Unlock()
	for user, cn := range a.conns.m {
		if cn != nil && cn.ChatID == chatID {
			delete(a.conns.m, user)
		}
	}
}

// ConnectionsOff says why connections can't be used (turned off for the whole bot, or in that group), else "".
func (c *Ctx) ConnectionsOff(chatID int64) string {
	if !c.App.moduleEnabled("connection") {
		return c.T("connection.connections_are_switched_off")
	}
	if chatID != 0 && c.App.ModuleOff(c.Ctx, chatID, "connection") {
		return c.T("connection.connections_are_switched_off_in_that")
	}
	return ""
}

// Connect connects a person to a group they administer. reference is a chat id (int64) or a "@username".
func (c *Ctx) Connect(userID int64, reference any) (*Connection, error) {
	var chat *gotgbot.ChatFullInfo
	var err error
	switch r := reference.(type) {
	case int64:
		chat, err = c.Bot.GetChatWithContext(c.Ctx, r, nil)
	case string:
		chat, err = c.getChatByUsername(r)
	}
	if err != nil || chat == nil {
		return nil, &ConnectError{c.T("connection.i_can_t_find_that_chat")}
	}
	if !isGroupType(chat.Type) {
		return nil, &ConnectError{c.T("connection.you_can_only_connect_to_a")}
	}
	if why := c.ConnectionsOff(chat.Id); why != "" {
		return nil, &ConnectError{why}
	}
	if s := c.MemberStatus(chat.Id, c.Bot.Id); s == "" || s == "left" || s == "kicked" {
		return nil, &ConnectError{c.T("connection.i_am_not_in_that_group")}
	}
	status := c.MemberStatus(chat.Id, userID)
	if status == "" {
		return nil, &ConnectError{c.T("connection.i_couldn_t_check_your_rights")}
	}
	if status != "creator" && status != "administrator" {
		return nil, &ConnectError{c.T("connection.only_admins_of_that_group_can")}
	}
	return c.App.saveConnection(c.Ctx, userID, chat.Id, chat.Title, chat.Type)
}

func (a *App) isRemote(name string) bool {
	_, module, ok := a.CommandOf(name)
	return ok && connectable[module] && !notRemote[strings.ToLower(name)]
}

// connected runs a connected admin's group command, sent in a private chat, for the group: the message is rewritten
// to look as if it was sent there, and the answers go back to the private chat. Everything else passes untouched.
func (r *Router) connected(c *Ctx, b *gotgbot.Bot) {
	m := c.Msg
	if c.Edited || m.Chat.Type != "private" || c.User == nil || m.Text == "" {
		return
	}
	name, _, ok := ParseCommand(m.Text, r.app.Cfg.CommandPrefix, b.Username)
	if !ok || !r.app.isRemote(name) {
		return
	}
	cn, err := r.app.GetConnection(c.Ctx, c.User.Id)
	if err != nil {
		slog.Debug("Looking up a connection failed", "err", err)
		return
	}
	if cn == nil {
		return
	}
	if why := c.ConnectionsOff(cn.ChatID); why != "" {
		// Connections were turned off while this one was open: it ends by itself.
		_, _ = r.app.ClearConnection(c.Ctx, c.User.Id)
		title := cn.Title
		if title == "" {
			title = itoa(cn.ChatID)
		}
		_, _ = c.SayToQuiet(m.Chat.Id, c.T("middlewares.your_connection_to_has_ended", V{"why": why, "v": text.Esc(title)}))
		return
	}
	copy := *m
	copy.Chat = gotgbot.Chat{Id: cn.ChatID, Type: cn.ChatType, Title: cn.Title}
	copy.MessageId, copy.ReplyToMessage, copy.MessageThreadId, copy.IsTopicMessage = 0, nil, 0, false
	c.Origin = m.Chat.Id
	c.Msg = &copy
}

// SayToQuiet sends a message to another chat and ignores a failure.
func (c *Ctx) SayToQuiet(chatID int64, msg string) (*gotgbot.Message, error) {
	m, err := c.send(chatID, msg, &gotgbot.SendMessageOpts{ParseMode: parseHTML})
	return m, err
}

// cleanCommand deletes a command the bot has dealt with, in chats whose admins turned that on with /cleanbluetext.
// Only commands meant for this bot are removed.
func (r *Router) cleanCommand(c *Ctx, b *gotgbot.Bot) {
	m := c.Msg
	if c.Origin != 0 || !isGroupType(m.Chat.Type) || m.Text == "" || !r.app.moduleEnabled("admin") {
		return
	}
	if !r.app.Setting(c.Ctx, "clean_commands", m.Chat.Id) {
		return
	}
	_, _ = b.DeleteMessageWithContext(c.Ctx, m.Chat.Id, m.MessageId, nil)
}
