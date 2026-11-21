package core

import (
	"fmt"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/text"
)

// Notices for the developer log (core/devnotify.py): the bot added to or removed from a chat, and what happens to
// federations (created, renamed, handed over, deleted).

func devLine(label string, value any) string { return fmt.Sprintf("<b>%s:</b> %v", label, value) }

func (a *App) devNotify(c *Ctx, tag string, lines ...string) {
	body := strings.Join(append(append([]string{"<b>" + tag + "</b>"}, lines...), devLine("Timestamp", Timestamp(time.Time{}))), "\n")
	a.DevLog(c.Ctx, body)
}

// ChatRef is a chat's name for a log: a link to it when it is public.
func ChatLink(title, username string, chatID int64) string {
	name := title
	if name == "" {
		name = fmt.Sprint(chatID)
	}
	if username != "" {
		return fmt.Sprintf(`<a href="https://t.me/%s">%s</a>`, text.Esc(username), text.Esc(name))
	}
	return text.Esc(name)
}

func memberIn(status string) bool {
	return status == "member" || status == "administrator" || status == "creator" || status == "restricted"
}

func memberOut(status string) bool { return status == "left" || status == "kicked" }

// chatEvent tells the developer log that the bot was added to a chat or taken out of it (a change of its rights
// says nothing).
func (a *App) chatEvent(c *Ctx, e *gotgbot.ChatMemberUpdated) {
	if e.Chat.Type != "group" && e.Chat.Type != "supergroup" && e.Chat.Type != "channel" {
		return
	}
	old, now := e.OldChatMember.GetStatus(), e.NewChatMember.GetStatus()
	where := []string{
		devLine("Chat", ChatLink(e.Chat.Title, e.Chat.Username, e.Chat.Id)),
		devLine("Chat ID", fmt.Sprintf("<code>%d</code>", e.Chat.Id)),
		devLine("Type", e.Chat.Type),
	}
	by := text.UserRef(e.From.Id, FullName(&e.From))
	switch {
	case memberOut(old) && memberIn(now):
		a.devNotify(c, "#NEWGROUP", append(where, devLine("Added by", by))...)
	case memberIn(old) && memberOut(now):
		label := "Left by"
		if now == "kicked" {
			label = "Removed by"
		}
		a.devNotify(c, "#LEFTGROUP", append(where, devLine(label, by))...)
	}
}

func fedLines(f *Federation) []string {
	return []string{devLine("Federation", "<code>"+text.Esc(f.Name)+"</code>"), devLine("Fed ID", "<code>"+f.ID+"</code>")}
}

func (c *Ctx) DevFedCreated(f *Federation, userID int64, name string) {
	c.App.devNotify(c, "#NEWFED", append(fedLines(f), devLine("By", text.UserRef(userID, name)))...)
}

func (c *Ctx) DevFedDeleted(f *Federation, userID int64, name string) {
	c.App.devNotify(c, "#DELFED", append(fedLines(f), devLine("By", text.UserRef(userID, name)))...)
}

func (c *Ctx) DevFedRenamed(f *Federation, newName string, userID int64, name string) {
	c.App.devNotify(c, "#RENAMEFED",
		devLine("Old Name", "<code>"+text.Esc(f.Name)+"</code>"),
		devLine("New Name", "<code>"+text.Esc(newName)+"</code>"),
		devLine("Fed ID", "<code>"+f.ID+"</code>"),
		devLine("By", text.UserRef(userID, name)))
}

func (c *Ctx) DevFedTransferred(f *Federation, newID int64, newName string) {
	c.App.devNotify(c, "#FEDTRANSFER", append(fedLines(f),
		devLine("Previous owner", text.UserRef(f.OwnerID, f.OwnerName)),
		devLine("New owner", text.UserRef(newID, newName)))...)
}
