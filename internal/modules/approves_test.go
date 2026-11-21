package modules_test

import (
	"strings"
	"testing"

	. "argo/internal/testutil"
)

func TestApproveFlow(t *testing.T) {
	e := setup(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/approve 20"})
	if !strings.Contains(lastText(t, e), "<b>approved</b>") {
		t.Fatalf("got %q", lastText(t, e))
	}
	ok, err := e.App.IsApproved(e.Ctx(), chat, bob.ID)
	if err != nil || !ok {
		t.Fatalf("not stored: %v %v", ok, err)
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/approve 20"})
	want(t, lastText(t, e), "This user is already approved!")

	e.Send(Msg{Chat: chat, From: alice, Text: "/approval 20"})
	if !strings.Contains(lastText(t, e), "</b> in this chat") {
		t.Errorf("got %q", lastText(t, e))
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/approved"})
	if !strings.Contains(lastText(t, e), "Approved Users in") || !strings.Contains(lastText(t, e), "• ") {
		t.Errorf("got %q", lastText(t, e))
	}

	// Admins and the bot can't be approved; members can't approve.
	e.Send(Msg{Chat: chat, From: alice, Text: "/approve 10"})
	want(t, lastText(t, e), "That user can't be approved: they are a chat admin or me.")
	e.Send(Msg{Chat: chat, From: carol, Text: "/approve 20"})
	want(t, lastText(t, e), "Only chat admins can use this command.")

	e.Send(Msg{Chat: chat, From: alice, Text: "/unapprove 20"})
	if !strings.Contains(lastText(t, e), "no longer approved") {
		t.Errorf("got %q", lastText(t, e))
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/unapprove 20"})
	want(t, lastText(t, e), "This user was not approved in this chat.")
}

func TestDisapproveAll(t *testing.T) {
	e := setup(t)
	e.Fake.SetMember(chat, alice.ID, alice.Name, "creator", nil)
	e.Send(Msg{Chat: chat, From: alice, Text: "/disapproveall"})
	if !strings.Contains(lastText(t, e), "no approved users") {
		t.Errorf("got %q", lastText(t, e))
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/approve 20"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/disapproveall"})
	if kb := e.Fake.Calls("sendMessage")[2].P["reply_markup"]; !strings.Contains(kb, "approve:clear:yes") {
		t.Fatalf("no confirmation buttons: %s", kb)
	}
	e.Press(chat, carol, "approve:clear:yes")
	if ok, _ := e.App.IsApproved(e.Ctx(), chat, bob.ID); !ok {
		t.Fatal("a non-owner cleared the approvals")
	}
	e.Fake.SetMember(chat, carol.ID, carol.Name, "creator", nil)
	e.Press(chat, carol, "approve:clear:yes")
	if ok, _ := e.App.IsApproved(e.Ctx(), chat, bob.ID); ok {
		t.Fatal("approvals were not cleared")
	}
}
