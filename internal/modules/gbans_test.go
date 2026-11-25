package modules_test

import (
	"strings"
	"testing"
	"time"

	"argo/internal/core"
	"argo/internal/modules"

	. "argo/internal/testutil"
)

var support = Person{ID: 61, Name: "Support Sue"}

func gbanSetup(t *testing.T) *Env {
	t.Helper()
	e := setup2(t)
	if err := e.App.SetRole(e.Ctx(), sudo.ID, core.Sudo, 0, sudo.Name, ""); err != nil {
		t.Fatal(err)
	}
	if err := e.App.SetRole(e.Ctx(), support.ID, core.Support, 0, support.Name, ""); err != nil {
		t.Fatal(err)
	}
	e.Fake.SetMember(chat, support.ID, support.Name, "member", nil)
	e.Fake.SetChat(itoa(support.ID), map[string]any{"id": support.ID, "type": "private", "first_name": support.Name})
	return e
}

func ownerLog(e *Env) []string { // what went to the user log (the owner's private chat when no log chat is set)
	var out []string
	for _, c := range e.Fake.Calls("sendMessage") {
		if c.P["chat_id"] == itoa(OwnerID) {
			out = append(out, c.P["text"])
		}
	}
	return out
}

func TestGbanByStaff(t *testing.T) {
	e := gbanSetup(t)
	e.Send(Msg{Chat: chat, From: bob, Text: "/gban 30 spam"})
	if len(e.Fake.Calls("banChatMember")) != 0 {
		t.Fatal("a member cannot gban")
	}
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: sudo, Text: "/gban 30"})
	contains(t, lastText(t, e), "Give a reason")
	e.Send(Msg{Chat: chat, From: sudo, Text: "/gban 30 spammer"})
	want(t, lastText(t, e), "Done! Globally banned.")
	if c := e.Fake.Calls("banChatMember"); len(c) != 1 || c[0].P["user_id"] != "30" || c[0].P["chat_id"] != itoa(chat) {
		t.Fatalf("the user is removed where the command was given: %+v", c)
	}
	log := ownerLog(e)
	if len(log) != 1 || !strings.Contains(log[0], "#GBANNED") || !strings.Contains(log[0], "<code>spammer</code>") {
		t.Fatalf("log: %q", log)
	}
	if n, _ := e.App.GbanCount(e.Ctx()); n != 1 {
		t.Fatalf("count %d", n)
	}
	// the same reason again changes nothing, another one updates
	e.Send(Msg{Chat: chat, From: sudo, Text: "/gban 30 spammer"})
	contains(t, lastText(t, e), "No changes made")
	e.Send(Msg{Chat: chat, From: sudo, Text: "/gban 30 worse"})
	contains(t, lastText(t, e), "Old Reason:</b> <code>spammer</code>")
	// whoever is protected can't be banned
	e.Send(Msg{Chat: chat, From: sudo, Text: "/gban 61 nope"})
	contains(t, lastText(t, e), "Nice Try")
	e.Send(Msg{Chat: chat, From: sudo, Text: "/gban 777000 nope"})
	contains(t, lastText(t, e), "take action against")
	if ok, _ := e.App.WhitelistAdd(e.Ctx(), 20, 1, "Bob", ""); !ok {
		t.Fatal("whitelist")
	}
	e.Send(Msg{Chat: chat, From: sudo, Text: "/gban 20 nope"})
	contains(t, lastText(t, e), "whitelisted")
	e.Send(Msg{Chat: chat, From: sudo, Text: "/gban 30 " + strings.Repeat("x", 501)})
	contains(t, lastText(t, e), "too long")
}

func TestGbanEnforcement(t *testing.T) {
	e := gbanSetup(t)
	e.Send(Msg{Chat: chat, From: sudo, Text: "/gban 30 spammer"})
	e.Fake.Reset()
	// their message is deleted, they are banned once and the chat is told
	e.Send(Msg{Chat: chat, From: carol, Text: "hello"})
	e.Send(Msg{Chat: chat, From: carol, Text: "hello again"})
	if deleted(e) != 2 {
		t.Fatalf("both messages go: %d", deleted(e))
	}
	if len(e.Fake.Calls("banChatMember")) != 1 {
		t.Fatalf("banned once: %d", len(e.Fake.Calls("banChatMember")))
	}
	s := sent(e)
	if len(s) != 1 || !strings.Contains(s[0], "Detected globally banned user") || !strings.Contains(s[0], "I banned them here") || !strings.Contains(s[0], "<code>spammer</code>") ||
		!strings.Contains(s[0], "@ArgoRobotSupport") {
		t.Fatalf("alert: %q", s)
	}
	// a command of theirs does not run
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: carol, Text: "/id"})
	if len(sent(e)) > 1 {
		t.Fatalf("nothing answers them: %q", sent(e))
	}
	// the chat can turn enforcement off, but only its owner
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: alice, Text: "/gbans off"})
	if len(e.Fake.Calls("banChatMember")) != 0 {
		t.Fatal("not the owner")
	}
	e.Send(Msg{Chat: chat, From: owner, Text: "/gbans off"})
	contains(t, lastText(t, e), "DISABLED")
	e.Send(Msg{Chat: chat, From: alice, Text: "/gbans"})
	contains(t, lastText(t, e), "<b>DISABLED</b>")
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: carol, Text: "hello"})
	if deleted(e) != 0 || len(e.Fake.Calls("banChatMember")) != 0 {
		t.Fatal("this chat leaves them alone")
	}
	e.Send(Msg{Chat: chat, From: owner, Text: "/gbans on"})
	// admins and approved users are spared
	if err := e.App.SetRole(e.Ctx(), 99, core.Support, 0, "x", ""); err != nil {
		t.Fatal(err)
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/gstat"})
}

func TestGbanJoinAndMemberUpdate(t *testing.T) {
	e := gbanSetup(t)
	core.MemberWait = 0
	t.Cleanup(func() { core.MemberWait = 3 * time.Second })
	e.Send(Msg{Chat: chat, From: sudo, Text: "/gban 101 raider"})
	if _, err := e.App.AddGban(e.Ctx(), 102, "Two", "also", 0, "", 0, ""); err != nil {
		t.Fatal(err)
	}
	e.Fake.Reset()
	e.Send(joinMsg(newcomer(101), newcomer(103)))
	if c := e.Fake.Calls("banChatMember"); len(c) != 1 || c[0].P["user_id"] != "101" {
		t.Fatalf("only the banned newcomer: %+v", c)
	}
	// only banned newcomers: the join message goes no further
	e.Fake.Reset()
	e.Send(joinMsg(newcomer(102)))
	if len(e.Fake.Calls("banChatMember")) != 1 {
		t.Fatal("banned")
	}
	// a member update of a banned person
	e.Fake.Reset()
	memberUpdate(e, newcomer(101), newcomer(101), "left", "member")
	if len(e.Fake.Calls("banChatMember")) != 0 {
		t.Fatal("dealt with a moment ago") // the 30 second window
	}
	if _, err := e.App.AddGban(e.Ctx(), 104, "Four", "late", 0, "", 0, ""); err != nil {
		t.Fatal(err)
	}
	memberUpdate(e, newcomer(104), newcomer(104), "left", "member")
	if c := e.Fake.Calls("banChatMember"); len(c) != 1 || c[0].P["user_id"] != "104" {
		t.Fatalf("a banned person who joins unseen: %+v", c)
	}
	// somebody who is only banned or promoted is not a join
	e.Fake.Reset()
	if _, err := e.App.AddGban(e.Ctx(), 105, "Five", "x", 0, "", 0, ""); err != nil {
		t.Fatal(err)
	}
	memberUpdate(e, newcomer(105), alice, "member", "restricted")
	if len(e.Fake.Calls("banChatMember")) != 0 {
		t.Fatal("not a join or a leave")
	}
}

func TestApprovalLiftsGban(t *testing.T) {
	e := gbanSetup(t)
	e.Send(Msg{Chat: chat, From: sudo, Text: "/gban 30 spammer"})
	e.Send(Msg{Chat: chat, From: carol, Text: "hi"}) // banned here by the bot
	if banned, _ := e.App.GbanBannedChats(e.Ctx(), 30); len(banned) != 1 {
		t.Fatalf("recorded: %v", banned)
	}
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: alice, Text: "/approve 30"})
	if c := e.Fake.Calls("unbanChatMember"); len(c) != 1 || c[0].P["user_id"] != "30" {
		t.Fatalf("approval lifts the ban of this chat: %+v", c)
	}
	if banned, _ := e.App.GbanBannedChats(e.Ctx(), 30); len(banned) != 0 {
		t.Fatalf("forgotten: %v", banned)
	}
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: carol, Text: "hi again"})
	if len(e.Fake.Calls("banChatMember")) != 0 || deleted(e) != 0 {
		t.Fatal("approved users are spared")
	}
}

func TestUngbanAndRequests(t *testing.T) {
	e := gbanSetup(t)
	e.Send(Msg{Chat: chat, From: sudo, Text: "/gban 30 spammer"})
	// Support users ask, a Sudo user decides
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: support, Text: "/gban 20 scam"})
	want(t, lastText(t, e), "Request sent.")
	if len(e.Fake.Calls("banChatMember")) != 0 {
		t.Fatal("not before approval")
	}
	var request Call
	for _, c := range e.Fake.Calls("sendMessage") {
		if c.P["chat_id"] == itoa(OwnerID) {
			request = c
		}
	}
	contains(t, request.P["text"], "#GBANREQUEST")
	contains(t, request.P["reply_markup"], "gban:apr:")
	// the request goes to the private chat of every Sudo user and above, not to the log chat
	copies := map[string]bool{}
	for _, c := range e.Fake.Calls("sendMessage") {
		if strings.Contains(c.P["text"], "#GBANREQUEST") {
			copies[c.P["chat_id"]] = true
		}
	}
	if len(copies) != 2 || !copies[itoa(OwnerID)] || !copies[itoa(sudo.ID)] {
		t.Fatalf("copies of the request: %v", copies)
	}
	e.Send(Msg{Chat: chat, From: support, Text: "/gban 20 scam"})
	contains(t, lastText(t, e), "already a pending request")
	id := strings.TrimSuffix(strings.Split(request.P["reply_markup"], "gban:apr:")[1], `"}`)
	id = strings.Trim(strings.SplitN(id, `"`, 2)[0], "}]")
	e.Press(logCh, support, "gban:apr:"+id)
	if len(e.Fake.Calls("banChatMember")) != 0 {
		t.Fatal("Support can't approve")
	}
	e.Press(logCh, sudo, "gban:apr:"+id)
	if len(e.Fake.Calls("banChatMember")) != 1 {
		t.Fatalf("approved: %+v", e.Fake.Calls("banChatMember"))
	}
	if ok, _ := e.App.IsGbanned(e.Ctx(), 20); !ok {
		t.Fatal("banned after approval")
	}
	// the copies of the request show the outcome, the log chat gets it, and the banned user is told
	if n := len(e.Fake.Calls("editMessageText")); n != 3 { // the pressed message and both copies
		t.Fatalf("edited copies: %d", n)
	}
	for _, c := range e.Fake.Calls("sendMessage") {
		if strings.Contains(c.P["text"], "#GBANNED") && strings.Contains(c.P["text"], "Approved by") {
			t.Fatalf("the log doesn't say who approved: %q", c.P["text"])
		}
	}
	told := false
	for _, c := range e.Fake.Calls("sendMessage") {
		if c.P["chat_id"] == "20" && strings.Contains(c.P["text"], "You have been globally banned") && strings.Contains(c.P["text"], "scam") &&
			strings.Contains(c.P["text"], "Timestamp:") && strings.Contains(c.P["text"], "Admin:") && strings.Contains(c.P["text"], "Approved by:") {
			told = true
		}
	}
	if !told {
		t.Fatal("the banned user is told in a private message")
	}
	e.Press(logCh, sudo, "gban:apr:"+id)
	if n := len(e.Fake.Calls("answerCallbackQuery")); n < 3 {
		t.Fatalf("answers: %d", n)
	}
	if a := lastCall(e, "answerCallbackQuery"); !strings.Contains(a.P["text"], "already been handled") {
		t.Fatalf("a second press: %q", a.P["text"])
	}
	// decline
	e.Send(Msg{Chat: chat, From: support, Text: "/gban 20 again"}) // bob is gbanned: update request
	var req2 Call
	for _, c := range e.Fake.Calls("sendMessage") {
		if c.P["chat_id"] == itoa(OwnerID) && strings.Contains(c.P["text"], "#GBANUPDATEREQUEST") {
			req2 = c
		}
	}
	contains(t, req2.P["text"], "#GBANUPDATEREQUEST")
	// lifting
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: sudo, Text: "/ungban 30"})
	contains(t, lastText(t, e), "Give a reason")
	e.Send(Msg{Chat: chat, From: sudo, Text: "/ungban 55 mistake"})
	contains(t, lastText(t, e), "is not globally banned")
	e.Send(Msg{Chat: chat, From: sudo, Text: "/ungban 30 mistake"})
	modules.WaitGbanTasks()
	if ok, _ := e.App.IsGbanned(e.Ctx(), 30); ok {
		t.Fatal("lifted")
	}
	contains(t, strings.Join(sent(e), "\n"), "Let's give them another chance")
	if c := e.Fake.Calls("unbanChatMember"); len(c) == 0 || c[0].P["only_if_banned"] != "true" {
		t.Fatalf("unbanned where they were banned: %+v", c)
	}
	contains(t, strings.Join(sent(e), "\n"), "global ban has been lifted")
	if banned, _ := e.App.GbanBannedChats(e.Ctx(), 30); len(banned) != 0 {
		t.Fatal("records forgotten")
	}
}

func TestGstat(t *testing.T) {
	e := gbanSetup(t)
	e.Send(Msg{Chat: chat, From: carol, Type: "private", Text: "/gstat"})
	got := lastText(t, e)
	contains(t, got, "Your Global Ban Status")
	contains(t, got, "Not Banned")
	e.Send(Msg{Chat: chat, From: sudo, Text: "/gban 20 a reason"})
	e.Send(Msg{Chat: chat, From: sudo, Text: "/gstat 20"})
	got = lastText(t, e)
	contains(t, got, "<code>Banned</code>")
	contains(t, got, "<code>a reason</code>")
	contains(t, got, "Sudo Sam")
	// anyone can look somebody up, but the admin stays hidden
	e.Send(Msg{Chat: chat, From: alice, Text: "/gstat 20"})
	got = lastText(t, e)
	contains(t, got, "<code>Banned</code>")
	contains(t, got, "<code>a reason</code>")
	if strings.Contains(got, "Sudo Sam") {
		t.Fatalf("only staff sees the admin: %q", got)
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/gstat"})
	contains(t, lastText(t, e), "Your Global Ban Status")
	// staff can't have a global ban, and it says so
	e.Send(Msg{Chat: chat, From: alice, Text: "/gstat 40"})
	contains(t, lastText(t, e), "member of my staff and can't have a global ban")
	if strings.Contains(lastText(t, e), "<b>Status:</b>") {
		t.Fatalf("just the sentence: %q", lastText(t, e))
	}
}

func TestGimportExport(t *testing.T) {
	e := gbanSetup(t)
	e.Send(Msg{Chat: chat, From: sudo, Text: "/gexport"})
	if len(e.Fake.Calls("sendDocument")) != 0 {
		t.Fatal("only the owner")
	}
	e.Send(Msg{Chat: chat, From: root, Text: "/gexport"})
	contains(t, lastText(t, e), "no global bans")
	e.Fake.SetFile("f1", []byte("user_id,name,reason\n101,One,first\n102,Two,second\n20,Bob,whitelisted\n101,One,dupe\n"))
	if ok, _ := e.App.WhitelistAdd(e.Ctx(), 20, 1, "Bob", ""); !ok {
		t.Fatal("whitelist")
	}
	doc := map[string]any{"document": map[string]any{"file_id": "f1", "file_unique_id": "u", "file_size": 80}}
	e.Send(Msg{Chat: chat, From: root, Text: "/gimport", Extra: doc})
	got := strings.Join(sent(e), "\n")
	_ = got
	edits := e.Fake.Calls("editMessageText")
	if len(edits) == 0 || !strings.Contains(edits[len(edits)-1].P["text"], "Imported <b>2</b> bans") ||
		!strings.Contains(edits[len(edits)-1].P["text"], "Skipped <b>1</b>") {
		t.Fatalf("import result: %+v", edits)
	}
	if n, _ := e.App.GbanCount(e.Ctx()); n != 2 {
		t.Fatalf("count %d", n)
	}
	e.Send(Msg{Chat: chat, From: root, Text: "/gimport", Extra: doc}) // already there
	e.Send(Msg{Chat: chat, From: root, Text: "/gexport json"})
	d := e.Fake.Calls("sendDocument")
	if len(d) != 1 || d[0].P["_filename_document"] != "gbans.json" || !strings.Contains(d[0].P["_file_document"], `"user_id": 101`) {
		t.Fatalf("export: %+v", d)
	}
	e.Send(Msg{Chat: chat, From: root, Text: "/gexport"})
	contains(t, lastText(t, e), "Wait a little")
}

// reaction delivers a message_reaction update of somebody in the group.
func reaction(e *Env, u Person) {
	e.T.Helper()
	e.Raw(map[string]any{"message_reaction": map[string]any{
		"chat": map[string]any{"id": chat, "type": "supergroup", "title": "Test group"}, "message_id": 5, "date": 1000,
		"user":         user(u),
		"old_reaction": []any{},
		"new_reaction": []any{map[string]any{"type": "emoji", "emoji": "👍"}},
	}})
}

func TestGbanOnReaction(t *testing.T) {
	e := gbanSetup(t)
	if _, err := e.App.AddGban(e.Ctx(), 106, "Six", "quiet", 0, "", 0, ""); err != nil {
		t.Fatal(err)
	}
	e.Fake.Reset()
	reaction(e, bob) // not banned
	if len(e.Fake.Calls("banChatMember")) != 0 {
		t.Fatal("banned somebody who is not globally banned")
	}
	reaction(e, newcomer(106))
	if c := e.Fake.Calls("banChatMember"); len(c) != 1 || c[0].P["user_id"] != "106" {
		t.Fatalf("a banned person who only reacts: %+v", c)
	}
	// An anonymous reaction (made for a chat) has nobody to act on.
	e.Fake.Reset()
	e.Raw(map[string]any{"message_reaction": map[string]any{
		"chat": map[string]any{"id": chat, "type": "supergroup", "title": "Test group"}, "message_id": 5, "date": 1000,
		"actor_chat":   map[string]any{"id": -100777, "type": "channel", "title": "C"},
		"old_reaction": []any{}, "new_reaction": []any{},
	}})
	if len(e.Fake.Calls("banChatMember")) != 0 {
		t.Fatal("acted on a reaction without a user")
	}
}

func TestDeclinedRequestIsNotLogged(t *testing.T) {
	e := gbanSetup(t)
	e.Send(Msg{Chat: chat, From: support, Text: "/gban 21 meh"})
	var request Call
	for _, c := range e.Fake.Calls("sendMessage") {
		if strings.Contains(c.P["text"], "#GBANREQUEST") {
			request = c
		}
	}
	id := strings.Trim(strings.SplitN(strings.Split(request.P["reply_markup"], "gban:apr:")[1], `"`, 2)[0], "}]")
	before := len(e.Fake.Calls("sendMessage"))
	e.Press(logCh, sudo, "gban:dec:"+id)
	for _, c := range e.Fake.Calls("sendMessage")[before:] {
		if strings.Contains(c.P["text"], "Declined by") {
			t.Fatalf("a declined request is not logged: %q", c.P["text"])
		}
	}
	if n := len(e.Fake.Calls("editMessageText")); n != 3 {
		t.Fatalf("the copies still show the outcome: %d", n)
	}
	if ok, _ := e.App.IsGbanned(e.Ctx(), 21); ok {
		t.Fatal("not banned")
	}
}
