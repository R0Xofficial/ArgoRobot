package modules_test

import (
	"context"
	"errors"
	"testing"

	"argo/internal/core"
	"argo/internal/modules"

	. "argo/internal/testutil"
)

func TestZombies(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: bob, Text: "/zombies"})
	contains(t, lastText(t, e), "Only chat admins")
	e.Send(Msg{Chat: chat, From: alice, Text: "/zombies"})
	modules.WaitZombieTasks()
	contains(t, editedText(e), "No deleted accounts")

	for id, name := range map[int64]string{801: "Deleted Account", 802: "Fine Person", 803: "deleted account", 804: "Deleted Account"} {
		status := "member"
		if id == 804 {
			status = "left" // deleted, but no longer in the chat
		}
		e.Fake.SetMember(chat, id, name, status, nil)
		if _, err := e.App.DB.Exec(e.Ctx(), "INSERT INTO user_chats (user_id, chat_id) VALUES ($1, $2)", id, chat); err != nil {
			t.Fatal(err)
		}
	}
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: alice, Text: "/zombies"})
	modules.WaitZombieTasks()
	bans := e.Fake.Calls("banChatMember")
	if len(bans) != 2 || len(e.Fake.Calls("unbanChatMember")) != 2 {
		t.Fatalf("bans %+v", bans)
	}
	got := map[string]bool{}
	for _, b := range bans {
		got[b.P["user_id"]] = true
	}
	if !got["801"] || !got["803"] {
		t.Fatalf("wrong people removed: %v", got)
	}
	contains(t, editedText(e), "Removed <b>2</b>")

	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: alice, Text: "/zombies"})
	modules.WaitZombieTasks()
	if len(e.Fake.Calls("banChatMember")) != 2 { // the two are still "members" in the fake: sweeping again finds them
		t.Fatalf("second sweep: %d", len(e.Fake.Calls("banChatMember")))
	}
}

func editedText(e *Env) string {
	var out string
	for _, c := range e.Fake.Calls("editMessageText") {
		out += c.P["text"] + " "
	}
	return out
}

type fakeLister struct {
	members []core.ChatMember
	fail    bool
}

func (f fakeLister) Members(context.Context, int64) ([]core.ChatMember, error) {
	if f.fail {
		return nil, errors.New("no list")
	}
	return f.members, nil
}
func (fakeLister) Close() error { return nil }

func TestZombiesMTProto(t *testing.T) {
	e := setup2(t)
	// With a member list the people nobody has seen are found, and no getChatMember call is needed.
	e.App.SetMemberLister(fakeLister{members: []core.ChatMember{{ID: 901, Deleted: true}, {ID: 902}, {ID: 903, Deleted: true}}})
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: alice, Text: "/zombies"})
	modules.WaitZombieTasks()
	bans := e.Fake.Calls("banChatMember")
	if len(bans) != 2 {
		t.Fatalf("bans %+v", bans)
	}
	for _, c := range e.Fake.Calls("getChatMember") {
		if c.P["user_id"] == "901" || c.P["user_id"] == "903" {
			t.Errorf("asked Telegram about a listed member: %+v", c)
		}
	}
	contains(t, editedText(e), "Removed <b>2</b>")

	// A list that can't be had: back to asking about the people the bot knows.
	e.App.SetMemberLister(fakeLister{fail: true})
	e.Send(Msg{Chat: chat, From: alice, Text: "/zombies"})
	modules.WaitZombieTasks()
	contains(t, editedText(e), "No deleted accounts")
}
