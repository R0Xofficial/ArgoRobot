package modules_test

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"argo/internal/core"
	"argo/internal/modules"

	. "argo/internal/testutil"
)

var fedID = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

func pm(p Person, text string) Msg { return Msg{Chat: p.ID, Type: "private", From: p, Text: text} }

func fedSetup(t *testing.T) (*Env, string) {
	t.Helper()
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/newfed Test"})
	contains(t, lastText(t, e), "private chat")
	e.Send(pm(alice, "/newfed"))
	contains(t, lastText(t, e), "Use /newfed")
	e.Send(pm(alice, "/newfed My Federation"))
	id := fedID.FindString(lastText(t, e))
	if id == "" {
		t.Fatalf("no federation id: %q", lastText(t, e))
	}
	e.Send(pm(alice, "/newfed Another"))
	contains(t, lastText(t, e), "already own a federation")
	return e, id
}

func TestFederationLifecycle(t *testing.T) {
	e, id := fedSetup(t)
	e.Send(pm(alice, "/fedinfo"))
	got := lastText(t, e)
	contains(t, got, "My Federation")
	contains(t, got, id)
	contains(t, got, "Not subscribed")
	// the Federation Admins button shows the same list as /fedadmins
	infoCalls := e.Fake.Calls("sendMessage")
	markup := infoCalls[len(infoCalls)-1].P["reply_markup"]
	contains(t, markup, "Federation Admins")
	contains(t, markup, "fa:"+id)
	// in a group the list is for the admins of that group: members get neither the command nor the click
	e.Send(Msg{Chat: chat, From: dave, Text: "/fedadmins " + id})
	contains(t, lastText(t, e), "Only the admins of this chat")
	e.Send(Msg{Chat: chat, From: alice, Text: "/fedadmins " + id})
	contains(t, lastText(t, e), "It has no other admins.")
	e.Send(Msg{Chat: chat, From: alice, Text: "/fedinfo"})
	contains(t, e.Fake.Calls("sendMessage")[len(e.Fake.Calls("sendMessage"))-1].P["reply_markup"], "fa:"+id)
	e.Send(Msg{Chat: chat, From: dave, Text: "/fedinfo " + id})
	contains(t, e.Fake.Calls("sendMessage")[len(e.Fake.Calls("sendMessage"))-1].P["reply_markup"], "fa:"+id) // everybody sees the button
	e.Press(chat, dave, "fa:"+id)                                                                            // but a member can't press it
	if len(e.Fake.Calls("editMessageReplyMarkup")) != 0 {
		t.Fatal("a member's click must not take the button off")
	}
	// in private anyone may, and the button comes off after one click
	e.Send(pm(bob, "/fedadmins "+id))
	contains(t, lastText(t, e), "It has no other admins.")
	e.PressOn(alice.ID, "private", 555, alice, "fa:"+id)
	contains(t, lastText(t, e), "My Federation")
	contains(t, lastText(t, e), "It has no other admins.")
	if len(e.Fake.Calls("editMessageReplyMarkup")) != 1 {
		t.Fatalf("the button should come off: %+v", e.Fake.Calls("editMessageReplyMarkup"))
	}
	// without an ID: the federation the person owns
	e.Send(pm(alice, "/fedadmins"))
	contains(t, lastText(t, e), "My Federation")
	e.Send(pm(alice, "/fedsubs"))
	contains(t, lastText(t, e), "My Federation")
	e.Send(pm(bob, "/fedadmins"))
	contains(t, lastText(t, e), "don't own a federation")
	e.Send(pm(alice, "/renamefed Renamed"))
	contains(t, lastText(t, e), "Renamed")
	e.Send(pm(alice, "/myfeds"))
	contains(t, lastText(t, e), "(Owner)")
	e.Send(pm(bob, "/renamefed x"))
	contains(t, lastText(t, e), "don't own a federation")
	// the owner (and the developer log) hear about it
	if !said(e, "#NEWFED") || !said(e, "#RENAMEFED") {
		t.Fatalf("developer log: %q", sent(e))
	}
	// only the chat's owner joins it
	e.Send(Msg{Chat: chat, From: alice, Text: "/joinfed " + id})
	contains(t, lastText(t, e), "Only the owner")
	e.Send(Msg{Chat: chat, From: owner, Text: "/joinfed nonsense"})
	contains(t, lastText(t, e), "Use /joinfed")
	e.Send(Msg{Chat: chat, From: owner, Text: "/joinfed 3f1c2a3e-1111-2222-3333-444455556666"})
	contains(t, lastText(t, e), "can't find a federation")
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: owner, Text: "/joinfed " + id})
	answered(t, e, "joined the federation")
	e.Send(Msg{Chat: chat, From: bob, Text: "/chatfed"})
	contains(t, lastText(t, e), "Renamed")
	e.Send(Msg{Chat: chat, From: owner, Text: "/joinfed " + id})
	contains(t, lastText(t, e), "already in")
	if !said(e, "New FedChat") {
		t.Fatalf("the owner is told: %q", sent(e))
	}
	// the owner decides what the chat does
	e.Send(Msg{Chat: chat, From: alice, Text: "/quietfed"})
	contains(t, lastText(t, e), "Quiet mode is <b>off</b>")
	e.Send(Msg{Chat: chat, From: alice, Text: "/quietfed on"})
	contains(t, lastText(t, e), "now <b>on</b>")
	e.Send(Msg{Chat: chat, From: alice, Text: "/quietfed maybe"})
	contains(t, lastText(t, e), "Use /quietfed")
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: owner, Text: "/leavefed"})
	answered(t, e, "left the federation")
	e.Send(Msg{Chat: chat, From: owner, Text: "/leavefed"})
	contains(t, lastText(t, e), "not in a federation")
}

func TestFedBanEnforcement(t *testing.T) {
	e, id := fedSetup(t)
	e.Send(Msg{Chat: chat, From: owner, Text: "/joinfed " + id})
	e.Send(Msg{Chat: chat, From: alice, Text: "/setfedlog"}) // not in a private chat: fine, in the chat to log
	contains(t, lastText(t, e), "now the log")
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: carol, Text: "/fban 20 spam"}) // not a fed admin
	contains(t, lastText(t, e), "not an admin")
	e.Send(Msg{Chat: chat, From: alice, Text: "/fban 10 me"})
	contains(t, lastText(t, e), "admin of the federation")
	if err := e.App.SetRole(e.Ctx(), 61, core.Support, 0, "S", ""); err != nil {
		t.Fatal(err)
	}
	// the owner of the federation decides: the bot's staff can be fbanned like anybody else
	e.Send(Msg{Chat: chat, From: alice, Text: "/fban 61 staff"})
	contains(t, lastText(t, e), "New FedBan")
	e.Send(Msg{Chat: chat, From: alice, Text: "/unfban 61"})
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: alice, Text: "/fban 20 spam"})
	contains(t, lastText(t, e), "New FedBan")
	contains(t, lastText(t, e), "<code>spam</code>")
	if c := e.Fake.Calls("banChatMember"); len(c) != 1 || c[0].P["user_id"] != "20" {
		t.Fatalf("banned where it was given: %+v", c)
	}
	logged := 0
	for _, c := range e.Fake.Calls("sendMessage") {
		if c.P["chat_id"] == itoa(chat) && strings.Contains(c.P["text"], "New FedBan") {
			logged++
		}
	}
	if logged < 2 { // the answer and the federation log (this same chat)
		t.Fatalf("log: %d", logged)
	}
	// the same again: nothing changes; another reason asks
	e.Send(Msg{Chat: chat, From: alice, Text: "/fban 20 spam"})
	contains(t, lastText(t, e), "already fbanned")
	e.Send(Msg{Chat: chat, From: alice, Text: "/fban 20 better reason"})
	contains(t, lastText(t, e), "update this reason")
	asked := e.Fake.LastID()
	e.PressOn(chat, "supergroup", asked, carol, "fr:y:"+id+":20")
	e.PressOn(chat, "supergroup", asked, alice, "fr:y:"+id+":20")
	var edit string
	for _, c := range e.Fake.Calls("editMessageText") {
		edit = c.P["text"]
	}
	contains(t, edit, "FedBan Reason Update")
	contains(t, edit, "<code>spam</code>")
	contains(t, edit, "<code>better reason</code>")
	// their messages go, once banned
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: bob, Text: "hello"})
	if deleted(e) != 1 || len(e.Fake.Calls("banChatMember")) != 1 {
		t.Fatalf("enforced: deleted %d bans %d", deleted(e), len(e.Fake.Calls("banChatMember")))
	}
	contains(t, strings.Join(sent(e), "\n"), "is banned in this chat's federation")
	// quiet chats say nothing
	e.Send(Msg{Chat: chat, From: alice, Text: "/quietfed on"})
	e.Fake.Reset()
	// stats
	e.Send(Msg{Chat: chat, From: carol, Text: "/fedstat 20 " + id})
	contains(t, lastText(t, e), "banned in")
	e.Send(Msg{Chat: chat, From: carol, Text: "/fedstat 20"})
	contains(t, lastText(t, e), "banned in 1 federation:")
	e.Send(Msg{Chat: chat, From: carol, Text: "/fedstat"})
	contains(t, lastText(t, e), "not banned in any federation")
	// lifting
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: alice, Text: "/unfban 30"})
	contains(t, lastText(t, e), "is not banned in")
	e.Send(Msg{Chat: chat, From: alice, Text: "/unfban 20"})
	contains(t, lastText(t, e), "Un-FedBan")
	modules.WaitFedTasks()
	if c := e.Fake.Calls("unbanChatMember"); len(c) != 1 || c[0].P["user_id"] != "20" {
		t.Fatalf("lifted in the chats of the federation: %+v", c)
	}
	if ban, _, _ := e.App.EffectiveFedBan(e.Ctx(), chat, 20); ban != nil {
		t.Fatal("no longer banned")
	}
}

func TestFedRequireReasonAndSubscription(t *testing.T) {
	e, id := fedSetup(t)
	e.Send(Msg{Chat: chat, From: owner, Text: "/joinfed " + id})
	e.Send(pm(alice, "/fedreason on"))
	contains(t, lastText(t, e), "now <b>on</b>")
	e.Send(Msg{Chat: chat, From: alice, Text: "/fban 20"})
	contains(t, lastText(t, e), "requires a reason")
	e.Send(pm(alice, "/fedreason maybe"))
	contains(t, lastText(t, e), "Use on or off")
	// another federation, owned by Carol, subscribes to this one
	e.Send(pm(carol, "/newfed Subscriber"))
	other := fedID.FindString(lastText(t, e))
	e.Send(pm(carol, "/subfed "+other))
	contains(t, lastText(t, e), "can't subscribe to itself")
	e.Fake.Reset()
	e.Send(pm(carol, "/subfed "+id))
	answered(t, e, "now inherits the bans")
	e.Send(pm(carol, "/subfed "+id))
	contains(t, lastText(t, e), "already subscribed")
	e.Send(pm(alice, "/fedsubs"))
	contains(t, lastText(t, e), "Number of subscribed feds: 1")
	e.Send(pm(carol, "/fedsubs"))
	contains(t, lastText(t, e), "Subscribed to the following feds")
	// a ban of the first federation applies in the chat of the second
	if _, err := e.App.BanInFed(e.Ctx(), id, 20, "Bob", "scam", alice.ID, "Alice"); err != nil {
		t.Fatal(err)
	}
	e.Send(Msg{Chat: -100999, From: owner, Text: "/joinfed " + other})
	e.Fake.SetMember(-100999, owner.ID, owner.Name, "creator", nil)
	e.Fake.SetMember(-100999, BotID, "Argo", "administrator", allRights)
	e.Fake.Reset()
	e.Send(Msg{Chat: -100999, From: owner, Text: "/joinfed " + other})
	answered(t, e, "joined the federation")
	e.Fake.Reset()
	e.Send(Msg{Chat: -100999, From: bob, Text: "hi"})
	if c := e.Fake.Calls("banChatMember"); len(c) != 1 || c[0].P["chat_id"] != "-100999" {
		t.Fatalf("inherited: %+v", c)
	}
	e.Fake.Reset()
	e.Send(pm(carol, "/unsubfed "+id))
	answered(t, e, "no longer inherits")
	e.Send(pm(carol, "/unsubfed "+id))
	contains(t, lastText(t, e), "not subscribed")
	// the owner of a federation can't be banned in it
	e.Send(pm(alice, "/fban 10 reason"))
	contains(t, lastText(t, e), "admin of the federation")
}

func TestFedPromoteTransferDelete(t *testing.T) {
	e, id := fedSetup(t)
	e.Send(pm(alice, "/fedpromote 20"))
	contains(t, lastText(t, e), "wants to make you an admin")
	msg := e.Fake.LastID()
	data := "fp:y:20:" + id
	e.PressOn(alice.ID, "private", msg, carol, data)
	if ok, _ := e.App.IsFedAdmin(e.Ctx(), &core.Federation{ID: id}, 20); ok {
		t.Fatal("only the person it is offered to can accept")
	}
	e.PressOn(alice.ID, "private", msg, bob, data)
	if ok, _ := e.App.IsFedAdmin(e.Ctx(), &core.Federation{ID: id}, 20); !ok {
		t.Fatal("promoted")
	}
	e.Send(pm(alice, "/fedpromote 20"))
	contains(t, lastText(t, e), "already is an admin")
	e.Send(pm(bob, "/fedadmins "+id))
	contains(t, lastText(t, e), "Bob")
	e.Send(pm(bob, "/myfeds"))
	contains(t, lastText(t, e), "(Admin)")
	e.Send(Msg{Chat: chat, From: bob, Text: "/feddemoteme " + id})
	contains(t, lastText(t, e), "private chat")
	e.Fake.Reset()
	e.Send(pm(bob, "/feddemoteme "+id))
	answered(t, e, "no longer an admin")
	e.Send(pm(bob, "/feddemoteme "+id))
	contains(t, lastText(t, e), "not an admin")
	e.Send(pm(alice, "/feddemote 20"))
	contains(t, lastText(t, e), "not an admin")
	// transfer
	e.Send(pm(alice, "/fedtransfer 30"))
	contains(t, lastText(t, e), "wants to give you the federation")
	msg = e.Fake.LastID()
	e.PressOn(alice.ID, "private", msg, carol, "fx:y:30:10")
	if f, _ := e.App.FedOwnedBy(e.Ctx(), 30); f == nil || f.ID != id {
		t.Fatalf("carol owns it now: %+v", f)
	}
	e.Send(pm(alice, "/delfed"))
	contains(t, lastText(t, e), "don't own a federation")
	e.Send(pm(carol, "/delfed"))
	msg = e.Fake.LastID()
	e.PressOn(30, "private", msg, bob, "fd:y:30")
	if f, _ := e.App.GetFed(e.Ctx(), id); f == nil {
		t.Fatal("only the owner can delete")
	}
	e.PressOn(30, "private", msg, carol, "fd:y:30")
	if f, _ := e.App.GetFed(e.Ctx(), id); f != nil {
		t.Fatal("deleted")
	}
}

func TestFedFilesAndClear(t *testing.T) {
	e, id := fedSetup(t)
	// exporting and importing the ban list is for the private chat only
	e.Send(Msg{Chat: chat, From: alice, Text: "/fedexport"})
	contains(t, lastText(t, e), "private chat")
	e.Send(Msg{Chat: chat, From: alice, Text: "/fedimport"})
	contains(t, lastText(t, e), "private chat")
	e.Send(pm(alice, "/fedexport"))
	contains(t, lastText(t, e), "no bans to export")
	e.Fake.SetFile("fedf", []byte("101,spam\n102,\n10,the owner\n"))
	doc := map[string]any{"document": map[string]any{"file_id": "fedf", "file_unique_id": "u", "file_size": 30}}
	m := pm(alice, "/fedimport minicsv")
	m.Extra = doc
	e.Fake.Reset()
	e.Send(m)
	answered(t, e, "Imported <b>2</b> bans")
	answered(t, e, "Skipped <b>1</b>")
	// an imported ban says so instead of naming the person who ran the import
	e.Send(pm(alice, "/fstat 101 "+id))
	contains(t, lastText(t, e), "Banned by:")
	contains(t, lastText(t, e), "Imported ban")
	e.Send(pm(alice, "/fedimport nonsense"))
	contains(t, lastText(t, e), "Use /fedimport")
	e.Send(pm(alice, "/fedexport json"))
	d := e.Fake.Calls("sendDocument")
	if len(d) != 1 || !strings.HasPrefix(d[0].P["_filename_document"], "fedbans_") || !strings.Contains(d[0].P["_file_document"], `"user_id": 101`) {
		t.Fatalf("export: %+v", d)
	}
	// the developer can clear a ban in any federation
	if err := e.App.SetRole(e.Ctx(), 41, core.Developer, 0, "Dev", ""); err != nil {
		t.Fatal(err)
	}
	dev := Person{ID: 41, Name: "Dev"}
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: dev, Text: "/clearfban 101 " + id + " wrong"})
	got := lastText(t, e)
	contains(t, got, "Federation bans cleared")
	contains(t, got, "<code>wrong</code>")
	e.Send(Msg{Chat: chat, From: dev, Text: "/clearfban 101"})
	contains(t, lastText(t, e), "no federation ban to clear")
	e.Send(Msg{Chat: chat, From: bob, Text: "/clearfban 102"})
	modules.WaitFedTasks()
}

func TestFedLeavesWhenPowerless(t *testing.T) {
	e, id := fedSetup(t)
	e.Send(Msg{Chat: chat, From: owner, Text: "/joinfed " + id})
	core.MemberWait = 0
	t.Cleanup(func() { core.MemberWait = 3 * time.Second })
	e.Raw(map[string]any{"my_chat_member": map[string]any{
		"chat": map[string]any{"id": chat, "type": "supergroup", "title": "Test group"}, "from": user(owner), "date": 1000,
		"old_chat_member": map[string]any{"status": "administrator", "can_restrict_members": true, "user": map[string]any{"id": BotID, "is_bot": true, "first_name": "Argo"}},
		"new_chat_member": map[string]any{"status": "administrator", "can_restrict_members": false, "user": map[string]any{"id": BotID, "is_bot": true, "first_name": "Argo"}},
	}})
	if f, _ := e.App.ChatFed(e.Ctx(), chat); f != nil {
		t.Fatal("the chat leaves its federation")
	}
	if !said(e, "left the federation") {
		t.Fatalf("the chat is told: %q", sent(e))
	}
}

func TestFedBanOnReaction(t *testing.T) {
	e, id := fedSetup(t)
	e.Send(Msg{Chat: chat, From: owner, Text: "/joinfed " + id})
	e.Send(Msg{Chat: chat, From: alice, Text: "/fban 20 spam"})
	e.Fake.Reset()
	reaction(e, carol)
	if len(e.Fake.Calls("banChatMember")) != 0 {
		t.Fatal("banned somebody who is not in the federation's ban list")
	}
	modules.ForgetRecent(chat, 20) // the ban just given counts as dealt with for a while
	reaction(e, bob)
	if c := e.Fake.Calls("banChatMember"); len(c) != 1 || c[0].P["user_id"] != "20" {
		t.Fatalf("a federation-banned person who only reacts: %+v", c)
	}
}
