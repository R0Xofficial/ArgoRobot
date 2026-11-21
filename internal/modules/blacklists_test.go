package modules_test

import (
	"strings"
	"testing"

	. "argo/internal/testutil"
)

func TestBlacklistAddListRemove(t *testing.T) {
	e := setup(t)
	e.Send(Msg{Chat: chat, From: carol, Text: "/addblacklist spam"})
	want(t, lastText(t, e), "Only chat admins can use this command.")

	e.Send(Msg{Chat: chat, From: alice, Text: "/addblacklist spam\n\"Free  Money\" {ban}\nexact:Hello {tmute 2h}\nstickerpack:t.me/addstickers/Cats\nbad {fly}\na*b*c*d*e*f"})
	got := lastText(t, e)
	for _, part := range []string{"<code>spam</code>", "<code>free money</code> {ban}", "<code>exact:hello</code> {tmute for 2 hours}",
		"<code>stickerpack:cats</code>", "<b>Skipped:</b>", "<code>bad</code>", "unknown action", "<code>a*b*c*d*e*f</code>: too many wildcards"} {
		if !strings.Contains(strings.ToLower(got), strings.ToLower(part)) {
			t.Errorf("missing %q in %q", part, got)
		}
	}

	e.Send(Msg{Chat: chat, From: carol, Text: "/blacklists"})
	got = lastText(t, e)
	if !strings.Contains(got, "• <code>spam</code>") || !strings.Contains(got, "Default action") {
		t.Errorf("list: %q", got)
	}

	e.Send(Msg{Chat: chat, From: alice, Text: "/addblacklist spam {warn}"})
	if !strings.Contains(lastText(t, e), "Updated") {
		t.Errorf("got %q", lastText(t, e))
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/unblacklist SPAM\nnothere"})
	got = lastText(t, e)
	if !strings.Contains(got, "Removed 1 trigger:") || !strings.Contains(got, "Not in the blacklist") {
		t.Errorf("remove: %q", got)
	}
}

func TestBlacklistFromReplyAndSticker(t *testing.T) {
	e := setup(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/addblacklist being rude {mute}", Reply: &Msg{Chat: chat, From: bob, Text: "What*  IS this?"}})
	if got := lastText(t, e); !strings.Contains(got, `what\*`) && !strings.Contains(got, "what\\*") {
		t.Errorf("wildcards of a replied message must be literal: %q", got)
	}
	trig, _ := e.App.Triggers(e.Ctx(), chat)
	if len(trig) != 1 || trig[0].Reason != "being rude" || trig[0].Action != "mute" {
		t.Fatalf("stored: %+v", trig)
	}

	e.Send(Msg{Chat: chat, From: alice, Text: "/addblacklist", Reply: &Msg{Chat: chat, From: bob, Extra: map[string]any{
		"sticker": map[string]any{"file_id": "S", "file_unique_id": "u", "type": "regular", "width": 1, "height": 1, "is_animated": false, "is_video": false, "emoji": "😀", "set_name": "CatPack"},
	}}})
	trig, _ = e.App.Triggers(e.Ctx(), chat)
	if len(trig) != 2 || trig[1].Pattern != "stickerpack:catpack" {
		t.Fatalf("sticker trigger: %+v", trig)
	}
}

func TestBlacklistEnforcement(t *testing.T) {
	e := setup(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/addblacklist spam\nscam {ban}\nbadword {warn}\nmaybe {kick}"})
	e.Fake.Reset()

	// Default action: delete.
	e.Send(Msg{Chat: chat, From: bob, Text: "this is SPAM!!"})
	if len(e.Fake.Calls("deleteMessage")) != 1 || len(e.Fake.Calls("sendMessage")) != 0 {
		t.Fatalf("delete only: %v", e.Fake.Calls("deleteMessage"))
	}
	// Whole words only.
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: bob, Text: "spammer and spams"})
	if len(e.Fake.Calls("deleteMessage")) != 0 {
		t.Fatal("matched inside a word")
	}
	// Edited messages count.
	e.Send(Msg{Chat: chat, From: bob, Text: "spam", Edited: true})
	if len(e.Fake.Calls("deleteMessage")) != 1 {
		t.Fatal("edited message was not checked")
	}

	// Ban.
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: bob, Text: "a scam here"})
	if len(e.Fake.Calls("banChatMember")) != 1 || len(e.Fake.Calls("deleteMessage")) != 1 {
		t.Fatalf("ban: %v", e.Fake.Calls("banChatMember"))
	}
	got := lastText(t, e)
	if !strings.Contains(got, "has been") || !strings.Contains(got, "banned") || !strings.Contains(got, "blacklisted trigger") {
		t.Errorf("notice: %q", got)
	}

	// Warn counts toward the limit (default 3).
	e.Fake.Reset()
	for i := 0; i < 3; i++ {
		e.Send(Msg{Chat: chat, From: carol, Text: "badword"})
	}
	if len(e.Fake.Calls("banChatMember")) != 1 {
		t.Fatalf("the third warn must ban: %d", len(e.Fake.Calls("banChatMember")))
	}
	if !strings.Contains(lastText(t, e), "warn limit") && !strings.Contains(lastText(t, e), "reaching") {
		t.Errorf("notice: %q", lastText(t, e))
	}

	// delete_matches off: a warn keeps the message, a plain "del" still deletes.
	e.Send(Msg{Chat: chat, From: alice, Text: "/blocklistdelete off"})
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: bob, Text: "maybe"})
	if len(e.Fake.Calls("deleteMessage")) != 0 || len(e.Fake.Calls("banChatMember")) != 1 {
		t.Fatalf("kick without delete: del=%d ban=%d", len(e.Fake.Calls("deleteMessage")), len(e.Fake.Calls("banChatMember")))
	}
	e.Send(Msg{Chat: chat, From: bob, Text: "spam"})
	if len(e.Fake.Calls("deleteMessage")) != 1 {
		t.Fatal("a del action must delete whatever /blocklistdelete says")
	}
}

func TestBlacklistExemptions(t *testing.T) {
	e := setup(t)
	if _, err := e.DB.Exec(e.Ctx(), "INSERT INTO roles (user_id, role) VALUES (40, 2)"); err != nil {
		t.Fatal(err)
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/addblacklist spam"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/approve 20"})
	e.Fake.Reset()

	for name, from := range map[string]Person{"admin": alice, "approved": bob, "staff": sudo, "owner": root} {
		e.Fake.Reset()
		e.Send(Msg{Chat: chat, From: from, Text: "spam"})
		if len(e.Fake.Calls("deleteMessage")) != 0 {
			t.Errorf("%s must be exempt", name)
		}
	}
	// Carol (no role, not approved) is not exempt.
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: carol, Text: "spam"})
	if len(e.Fake.Calls("deleteMessage")) == 0 {
		t.Error("an ordinary member must be caught")
	}
}

func TestBlacklistFailureComplaintOnce(t *testing.T) {
	e := setup(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/addblacklist scam {ban}"})
	e.Fake.Reset()
	e.Fake.Fail("banChatMember", 400, "Bad Request: not enough rights to restrict/unrestrict chat member")
	e.Fake.Fail("banChatMember", 400, "Bad Request: not enough rights to restrict/unrestrict chat member")
	e.Send(Msg{Chat: chat, From: bob, Text: "scam"})
	e.Send(Msg{Chat: chat, From: carol, Text: "scam"})
	if n := len(e.Fake.Calls("sendMessage")); n != 1 {
		t.Fatalf("the complaint must be sent once per 5 minutes, got %d", n)
	}
}

func TestBlacklistSettingsCommands(t *testing.T) {
	e := setup(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/setblacklistmode tmute 30m"})
	if !strings.Contains(lastText(t, e), "30 minutes") {
		t.Errorf("got %q", lastText(t, e))
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/setblacklistmode fly"})
	if !strings.Contains(lastText(t, e), "Unknown action") && !strings.Contains(lastText(t, e), "unknown action") {
		t.Errorf("got %q", lastText(t, e))
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/setblacklistmode off"})
	if !strings.Contains(lastText(t, e), "off") {
		t.Errorf("got %q", lastText(t, e))
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/addblacklist spam"})
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: bob, Text: "spam"})
	if len(e.Fake.Calls("deleteMessage")) != 0 {
		t.Fatal("with the default action off nothing happens")
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/blocklistdelete maybe"})
	if !strings.Contains(lastText(t, e), "blocklistdelete") {
		t.Errorf("got %q", lastText(t, e))
	}
}

func TestUnblacklistAll(t *testing.T) {
	e := setup(t)
	e.Fake.SetMember(chat, alice.ID, alice.Name, "creator", nil)
	e.Send(Msg{Chat: chat, From: alice, Text: "/addblacklist one\ntwo"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/unblacklistall"})
	e.Press(chat, alice, "blacklist:clear:yes")
	if trig, _ := e.App.Triggers(e.Ctx(), chat); len(trig) != 0 {
		t.Fatalf("still %d triggers", len(trig))
	}
}

func TestWarnFilters(t *testing.T) {
	e := setup(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/addwarn \"free money\" no scams"})
	if !strings.Contains(lastText(t, e), "free money") {
		t.Fatalf("got %q", lastText(t, e))
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/warnlist"})
	if !strings.Contains(lastText(t, e), "free money</code> - no scams") {
		t.Errorf("got %q", lastText(t, e))
	}
	e.Send(Msg{Chat: bob.ID, Type: "private", From: bob, Text: "free money"}) // private chats are not checked
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: bob, Text: "get FREE money now"})
	if !strings.Contains(lastText(t, e), "warn") {
		t.Errorf("got %q", lastText(t, e))
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/nowarn \"free money\""})
	if !strings.Contains(lastText(t, e), "removed") {
		t.Errorf("got %q", lastText(t, e))
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/nowarn \"free money\""})
	if !strings.Contains(lastText(t, e), "no warn filter") && !strings.Contains(lastText(t, e), "There is no") {
		t.Errorf("got %q", lastText(t, e))
	}
}
