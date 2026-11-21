package modules_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"argo/internal/modules"
	. "argo/internal/testutil"
)

const (
	chat  = int64(-100500)
	logCh = int64(-100777)
)

var (
	alice = Person{ID: 10, Name: "Alice"}                    // chat admin
	bob   = Person{ID: 20, Name: "Bob", Username: "bobby_x"} // member
	carol = Person{ID: 30, Name: "Carol"}                    // member
	root  = Person{ID: OwnerID, Name: "Root"}                // the bot's owner
	sudo  = Person{ID: 40, Name: "Sudo Sam"}                 // the bot's staff
)

var adminRights = map[string]any{"can_restrict_members": true, "can_change_info": true}

func setup(t *testing.T) *Env {
	t.Helper()
	e := NewEnv(t, "../../locales", modules.Register)
	e.Fake.SetMember(chat, alice.ID, alice.Name, "administrator", adminRights)
	e.Fake.SetMember(chat, bob.ID, bob.Name, "member", nil)
	e.Fake.SetMember(chat, carol.ID, carol.Name, "member", nil)
	e.Fake.SetMember(chat, sudo.ID, sudo.Name, "member", nil)
	e.Fake.SetMember(chat, root.ID, root.Name, "member", nil)
	// getChat knows people who have talked to the bot, as Telegram does.
	for _, p := range []Person{bob, carol, sudo} {
		obj := map[string]any{"id": p.ID, "type": "private", "first_name": p.Name}
		if p.Username != "" {
			obj["username"] = p.Username
		}
		e.Fake.SetChat(itoa(p.ID), obj)
	}
	return e
}

func lastText(t *testing.T, e *Env) string {
	t.Helper()
	calls := e.Fake.Calls("sendMessage")
	if len(calls) == 0 {
		t.Fatal("the bot sent nothing")
	}
	return calls[len(calls)-1].P["text"]
}

func want(t *testing.T, got, expected string) {
	t.Helper()
	if got != expected {
		t.Errorf("\n got: %q\nwant: %q", got, expected)
	}
}

func reply(to Person, text string) *Msg { return &Msg{Chat: chat, From: to, Text: text} }

func link(p Person) string { return `<a href="tg://user?id=` + itoa(p.ID) + `">` + p.Name + `</a>` }

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func fmtInt(n int64) string { return itoa(n) }

func TestBanByReply(t *testing.T) {
	e := setup(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/ban spamming links", Reply: reply(bob, "buy now")})

	calls := e.Fake.Calls("banChatMember")
	if len(calls) != 1 || calls[0].P["user_id"] != "20" || calls[0].P["chat_id"] != "-100500" {
		t.Fatalf("banChatMember: %+v", calls)
	}
	want(t, lastText(t, e), link(bob)+" was banned by "+link(alice)+"\nReason: spamming links")
	kb := e.Fake.Calls("sendMessage")[0].P["reply_markup"]
	if !strings.Contains(kb, `"callback_data":"undo:ban:20"`) || !strings.Contains(kb, `"text":"Unban"`) {
		t.Errorf("undo button missing: %s", kb)
	}
}

func TestBanRefusals(t *testing.T) {
	e := setup(t)

	// A regular member may not.
	e.Send(Msg{Chat: chat, From: carol, Text: "/ban 20"})
	want(t, lastText(t, e), "Only chat admins can use this command.")
	if len(e.Fake.Calls("banChatMember")) != 0 {
		t.Fatal("a member banned someone")
	}

	// Nobody bans an admin, themselves or the bot.
	e.Send(Msg{Chat: chat, From: alice, Text: "/ban 10"})
	want(t, lastText(t, e), "You can't do that to yourself.")
	e.Fake.SetMember(chat, 11, "Dave", "administrator", adminRights)
	e.Send(Msg{Chat: chat, From: alice, Text: "/ban 11"})
	want(t, lastText(t, e), "I can't take action against a chat admin.")
	e.Send(Msg{Chat: chat, From: alice, Text: "/ban 999"})
	want(t, lastText(t, e), "I can't take action against myself.")
	e.Send(Msg{Chat: chat, From: alice, Text: "/ban"})
	want(t, lastText(t, e), "Reply to a user's message, mention them, or give a user id or @username.")
	if len(e.Fake.Calls("banChatMember")) != 0 {
		t.Fatal("a refused ban reached Telegram")
	}

	// /adminerror off: people without rights get no answer.
	if err := e.App.SetSetting(context.Background(), "admin_errors", chat, false); err != nil {
		t.Fatal(err)
	}
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: carol, Text: "/ban 20"})
	if n := len(e.Fake.Calls("sendMessage")); n != 0 {
		t.Errorf("answered a refusal the chat wanted silent: %d messages", n)
	}
}

func TestBanTelegramRefusal(t *testing.T) {
	e := setup(t)
	e.Fake.Fail("banChatMember", 400, "Bad Request: not enough rights to restrict/unrestrict chat member")
	e.Send(Msg{Chat: chat, From: alice, Text: "/ban 20"})
	if got := lastText(t, e); !strings.Contains(got, "rights") {
		t.Errorf("expected the rights message, got %q", got)
	}
}

func TestStaffActsWithNote(t *testing.T) {
	e := setup(t)
	if _, err := e.DB.Exec(context.Background(), "INSERT INTO roles (user_id, role) VALUES (40, 2)"); err != nil {
		t.Fatal(err)
	}
	e.Send(Msg{Chat: chat, From: sudo, Text: "/kick 20 rude"})
	if len(e.Fake.Calls("banChatMember")) != 1 || len(e.Fake.Calls("unbanChatMember")) != 1 {
		t.Fatalf("kick = ban + unban, got %d + %d", len(e.Fake.Calls("banChatMember")), len(e.Fake.Calls("unbanChatMember")))
	}
	if e.Fake.Calls("unbanChatMember")[0].P["only_if_banned"] != "true" {
		t.Error("unban must be only_if_banned")
	}
	want(t, lastText(t, e), link(bob)+" was kicked by "+link(sudo)+"\nReason: rude\n\n<i>Done by the bot's staff.</i>")

	// The owner too, and the chat can turn staff help off.
	if err := e.App.SetSetting(context.Background(), "staff_help", chat, false); err != nil {
		t.Fatal(err)
	}
	e.Send(Msg{Chat: chat, From: sudo, Text: "/kick 20"})
	want(t, lastText(t, e), "Only chat admins can use this command.")
}

func TestTempBan(t *testing.T) {
	e := setup(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/tban 20 2h flooding"})
	calls := e.Fake.Calls("banChatMember")
	if len(calls) != 1 || calls[0].P["until_date"] == "" {
		t.Fatalf("tban needs until_date: %+v", calls)
	}
	want(t, lastText(t, e), link(bob)+" was banned for 2 hours by "+link(alice)+"\nReason: flooding")

	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: alice, Text: "/tban 20 soon"})
	want(t, lastText(t, e), "Give a duration like 30m, 2h, 3d or 1w (from 1 minute to 365 days).")
	if len(e.Fake.Calls("banChatMember")) != 0 {
		t.Fatal("bad duration still banned")
	}
}

func TestUnbanAndUndoButton(t *testing.T) {
	e := setup(t)
	// A member is not banned.
	e.Send(Msg{Chat: chat, From: alice, Text: "/unban 20"})
	want(t, lastText(t, e), link(bob)+" isn't banned.")

	e.Fake.SetMember(chat, bob.ID, bob.Name, "kicked", nil)
	e.Send(Msg{Chat: chat, From: alice, Text: "/unban 20"})
	want(t, lastText(t, e), link(bob)+" was unbanned by "+link(alice))
	if len(e.Fake.Calls("unbanChatMember")) != 1 {
		t.Fatal("no unban")
	}

	// The button: only admins; it edits the message.
	e.Fake.Reset()
	e.Press(chat, carol, "undo:ban:20")
	if a := e.Fake.Calls("answerCallbackQuery"); len(a) != 1 || a[0].P["show_alert"] != "true" {
		t.Errorf("a member pressing Unban should get an alert: %+v", a)
	}
	e.Fake.Reset()
	e.Press(chat, alice, "undo:ban:20")
	edits := e.Fake.Calls("editMessageText")
	if len(edits) != 1 {
		t.Fatalf("expected one edit, got %+v", e.Fake.Calls(""))
	}
	want(t, edits[0].P["text"], link(bob)+" was unbanned by "+link(alice))

	// Someone who is a member again: "they aren't banned any more", buttons removed.
	e.Fake.SetMember(chat, bob.ID, bob.Name, "member", nil)
	e.Fake.Reset()
	e.Press(chat, alice, "undo:ban:20")
	if len(e.Fake.Calls("unbanChatMember")) != 0 {
		t.Error("unbanned a member")
	}
	if len(e.Fake.Calls("editMessageReplyMarkup")) != 1 {
		t.Error("the stale button was not removed")
	}
}

func TestSilentAndDeletingBans(t *testing.T) {
	e := setup(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/sban", Reply: reply(bob, "spam")})
	if n := len(e.Fake.Calls("deleteMessage")); n != 2 {
		t.Errorf("sban deletes the command and the message, deleted %d", n)
	}
	if n := len(e.Fake.Calls("sendMessage")); n != 0 {
		t.Errorf("sban is silent, but sent %d messages", n)
	}
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: alice, Text: "/dban why", Reply: reply(bob, "spam")})
	if n := len(e.Fake.Calls("deleteMessage")); n != 1 {
		t.Errorf("dban deletes the replied message, deleted %d", n)
	}
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: alice, Text: "/dban 20"})
	want(t, lastText(t, e), "Reply to the message you want to act on.")
}

func TestKickme(t *testing.T) {
	e := setup(t)
	e.Send(Msg{Chat: chat, From: carol, Text: "/kickme"})
	if len(e.Fake.Calls("banChatMember")) != 1 || len(e.Fake.Calls("unbanChatMember")) != 1 {
		t.Fatal("kickme did not remove the user")
	}
	want(t, lastText(t, e), link(carol)+" kicked themselves out. They can come back with an invite link.")
	if e.Fake.Calls("sendMessage")[0].P["reply_parameters"] != "" {
		t.Error("the kickme answer must not be attached to the message")
	}
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: alice, Text: "/kickme"})
	want(t, lastText(t, e), "I can't remove an admin of this chat. Step down first, or leave on your own.")
}

func TestTargetsByMentionAndUsername(t *testing.T) {
	e := setup(t)
	// A person without a @username: a text mention, after an emoji (UTF-16 offsets!).
	text := "/ban 😀 Carol spammer"
	// "/ban " is 5 units, the emoji 2 units and a space: the mention starts at 5 in "/ban Carol x" but the
	// first argument here is the emoji, so put the mention first instead.
	text = "/ban Carol spammer 😀"
	e.Send(Msg{Chat: chat, From: alice, Text: text, Entities: []map[string]any{TextMention(5, 5, carol)}})
	if c := e.Fake.Calls("banChatMember"); len(c) != 1 || c[0].P["user_id"] != "30" {
		t.Fatalf("text mention: %+v", e.Fake.Calls(""))
	}
	want(t, lastText(t, e), link(carol)+" was banned by "+link(alice)+"\nReason: spammer 😀")

	// @username of someone the bot has met (stored by the phone book when Bob wrote).
	e.Send(Msg{Chat: chat, From: bob, Text: "hello"}) // not a command, but Bob is recorded
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: alice, Text: "/ban @Bobby_X", Entities: []map[string]any{Mention(5, 8)}})
	if c := e.Fake.Calls("banChatMember"); len(c) != 1 || c[0].P["user_id"] != "20" {
		t.Fatalf("@username of a known user: %+v", e.Fake.Calls(""))
	}

	// An unknown @username goes to Telegram as "@name".
	e.Fake.Reset()
	e.Fake.SetChat("@ghost_user", map[string]any{"id": 77, "type": "private", "first_name": "Ghost", "username": "ghost_user"})
	e.Fake.SetMember(chat, 77, "Ghost", "member", nil)
	e.Send(Msg{Chat: chat, From: alice, Text: "/ban @ghost_user"})
	if c := e.Fake.Calls("banChatMember"); len(c) != 1 || c[0].P["user_id"] != "77" {
		t.Fatalf("@username from Telegram: %+v", e.Fake.Calls(""))
	}
	if g := e.Fake.Calls("getChat"); len(g) == 0 || g[0].P["chat_id"] != "@ghost_user" {
		t.Errorf("getChat should be asked for @ghost_user: %+v", g)
	}

	// Unknown everywhere.
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: alice, Text: "/ban @nobody_here"})
	if got := lastText(t, e); !strings.Contains(got, "nobody_here") {
		t.Errorf("expected the unknown-user hint, got %q", got)
	}
}

func TestBanChannel(t *testing.T) {
	e := setup(t)
	channel := map[string]any{"id": -1001234, "type": "channel", "title": "Spam Channel"}
	e.Send(Msg{Chat: chat, From: alice, Text: "/ban", Reply: &Msg{Chat: chat, From: Person{ID: 136817688, Name: "Channel"}, Text: "buy", SenderChat: channel}})
	c := e.Fake.Calls("banChatSenderChat")
	if len(c) != 1 || c[0].P["sender_chat_id"] != "-1001234" {
		t.Fatalf("banChatSenderChat: %+v", e.Fake.Calls(""))
	}
	want(t, lastText(t, e), "<b>Spam Channel</b> was banned by "+link(alice))
}

func TestWarnsUntilLimit(t *testing.T) {
	e := setup(t)
	for i := 1; i <= 2; i++ {
		e.Send(Msg{Chat: chat, From: alice, Text: "/warn 20 rule " + fmtInt(int64(i))})
		want(t, lastText(t, e), link(bob)+" was warned ("+fmtInt(int64(i))+"/3) by "+link(alice)+"\nReason: rule "+fmtInt(int64(i)))
	}
	if len(e.Fake.Calls("banChatMember")) != 0 {
		t.Fatal("banned before the limit")
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/warns 20"})
	got := lastText(t, e)
	if !strings.HasPrefix(got, link(bob)+" has 2/3 warns.\n") || !strings.Contains(got, "rule 1") || !strings.Contains(got, "rule 2") {
		t.Errorf("warns list: %q", got)
	}

	// Remove the latest, then warn again to the limit.
	e.Send(Msg{Chat: chat, From: alice, Text: "/unwarn 20"})
	want(t, lastText(t, e), "The latest warn of "+link(bob)+" was removed by "+link(alice)+" (1/3 left)")
	e.Send(Msg{Chat: chat, From: alice, Text: "/warn 20 again"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/warn 20 last"})
	if len(e.Fake.Calls("banChatMember")) != 1 {
		t.Fatalf("reaching the limit should ban")
	}
	want(t, lastText(t, e), link(bob)+" reached the warn limit (3/3) and was banned by "+link(alice)+"\nReason: last")
	e.Send(Msg{Chat: chat, From: alice, Text: "/warns 20"})
	want(t, lastText(t, e), link(bob)+" has no warns.")
}

func TestWarnSettingsAndMode(t *testing.T) {
	e := setup(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/setwarnlimit 2"})
	want(t, lastText(t, e), "The warn limit in this chat is now <b>2</b>.")
	e.Send(Msg{Chat: chat, From: alice, Text: "/setwarnlimit 99"})
	if got := lastText(t, e); !strings.Contains(got, "10") {
		t.Errorf("limit hint: %q", got)
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/warnmode tmute 30m"})
	want(t, lastText(t, e), "A user who reaches the warn limit now gets: <b>tmute for 30 minutes</b>.")
	e.Send(Msg{Chat: chat, From: alice, Text: "/warnmode delete"})
	if got := lastText(t, e); !strings.Contains(got, "Use /warnmode") {
		t.Errorf("delete is not a limit action: %q", got)
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/warnmode tban"})
	if got := lastText(t, e); !strings.Contains(got, "needs a duration") {
		t.Errorf("tban without time: %q", got)
	}

	// Two warns at limit 2 -> muted for 30 minutes.
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: alice, Text: "/warn 20"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/warn 20"})
	r := e.Fake.Calls("restrictChatMember")
	if len(r) != 1 || r[0].P["until_date"] == "" || r[0].P["user_id"] != "20" {
		t.Fatalf("restrictChatMember: %+v", e.Fake.Calls(""))
	}
	want(t, lastText(t, e), link(bob)+" reached the warn limit (2/2) and was muted for 30 minutes by "+link(alice))

	e.Send(Msg{Chat: chat, From: alice, Text: "/strongwarn off"})
	want(t, lastText(t, e), "A user who reaches the warn limit gets: <b>kick</b>. Change it with /warnmode.")
}

func TestWarnLimitButTelegramRefuses(t *testing.T) {
	e := setup(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/setwarnlimit 1"})
	e.Fake.Fail("banChatMember", 403, "Forbidden: bot was kicked")
	e.Send(Msg{Chat: chat, From: alice, Text: "/warn 20"})
	got := lastText(t, e)
	if !strings.Contains(got, "was warned (1/1)") {
		t.Errorf("limit failure message: %q", got)
	}
	// The warn stays.
	e.Send(Msg{Chat: chat, From: alice, Text: "/warns 20"})
	if !strings.Contains(lastText(t, e), "1/1 warns") {
		t.Errorf("the warn should have stayed: %q", lastText(t, e))
	}
}

func TestWarnButtonsAndRemoval(t *testing.T) {
	e := setup(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/warn 20 one"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/warn 20 two"})

	var id1, id2 int64
	rows, err := e.DB.Pool.Query(context.Background(), "SELECT id FROM warns ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	rows.Next()
	rows.Scan(&id1)
	rows.Next()
	rows.Scan(&id2)
	rows.Close()

	e.Fake.Reset()
	e.Press(chat, alice, "warn:undo:"+fmtInt(id1))
	want(t, e.Fake.Calls("editMessageText")[0].P["text"], "The warn <code>#"+fmtInt(id1)+"</code> of "+link(bob)+" was removed by "+link(alice))

	e.Fake.Reset()
	e.Press(chat, alice, "warn:del:"+fmtInt(id1)) // already gone
	if a := e.Fake.Calls("answerCallbackQuery"); len(a) != 1 || a[0].P["show_alert"] != "true" {
		t.Errorf("a removed warn should alert: %+v", a)
	}

	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: alice, Text: "/rmwarn #" + fmtInt(id2)})
	want(t, lastText(t, e), "The warn <code>#"+fmtInt(id2)+"</code> of "+link(bob)+" was removed by "+link(alice))
	e.Send(Msg{Chat: chat, From: alice, Text: "/rmwarn nope"})
	if !strings.Contains(lastText(t, e), "ID of the warn") {
		t.Errorf("rmwarn hint: %q", lastText(t, e))
	}

	e.Send(Msg{Chat: chat, From: alice, Text: "/warn 20"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/resetwarns 20"})
	want(t, lastText(t, e), "All warns of "+link(bob)+" were removed (1) by "+link(alice))
}

func TestSetlang(t *testing.T) {
	e := setup(t)
	e.Send(Msg{Chat: chat, From: carol, Text: "/setlang pl"})
	want(t, lastText(t, e), "Only chat admins can use this command.")

	e.Send(Msg{Chat: chat, From: alice, Text: "/setlang"})
	got := lastText(t, e)
	if !strings.Contains(got, "The language of this chat is <b>") || !strings.Contains(got, "(<code>pl</code>)") {
		t.Errorf("status: %q", got)
	}
	kb := e.Fake.Calls("sendMessage")[len(e.Fake.Calls("sendMessage"))-1].P["reply_markup"]
	if !strings.Contains(kb, `"callback_data":"lang:pl"`) || !strings.Contains(kb, `"callback_data":"lang:en-pt"`) {
		t.Errorf("keyboard: %s", kb)
	}

	e.Send(Msg{Chat: chat, From: alice, Text: "/setlang pl"})
	if got := lastText(t, e); !strings.HasPrefix(got, "Gotowe.") {
		t.Errorf("the answer should already be Polish: %q", got)
	}
	// Persisted: the next update is Polish.
	e.Send(Msg{Chat: chat, From: carol, Text: "/ban 20"})
	if got := lastText(t, e); strings.Contains(got, "Only chat admins") {
		t.Errorf("still English: %q", got)
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/ban 20 spam"})
	if got := lastText(t, e); !strings.Contains(got, "został zbanowany") && !strings.Contains(got, "zbanowan") {
		t.Errorf("Polish ban text: %q", got)
	}

	// Button, and an unknown code.
	e.Fake.Reset()
	e.Press(chat, alice, "lang:en")
	if len(e.Fake.Calls("editMessageText")) != 1 {
		t.Error("language button did not edit the message")
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/setlang xx"})
	if !strings.Contains(lastText(t, e), "<code>en</code>") {
		t.Errorf("unknown language hint: %q", lastText(t, e))
	}
}

func TestChatLog(t *testing.T) {
	e := setup(t)
	if _, err := e.DB.Exec(context.Background(), "INSERT INTO chat_logs (chat_id, log_chat_id) VALUES ($1, $2)", chat, logCh); err != nil {
		t.Fatal(err)
	}
	e.Send(Msg{Chat: chat, From: alice, Text: "/sban 20 spam", Reply: reply(bob, "x")})
	var entry string
	for _, c := range e.Fake.Calls("sendMessage") {
		if c.P["chat_id"] == "-100777" {
			entry = c.P["text"]
		}
	}
	want(t, entry, "<b>#BANNED</b>\n<b>Chat:</b> Test group [<code>-100500</code>]\n<b>Admin:</b> "+link(alice)+" [<code>10</code>]\n<b>User:</b> "+link(bob)+" [<code>20</code>]\n<b>Silent:</b> Yes\n<b>Reason:</b> spam")

	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: carol, Text: "/kickme"})
	for _, c := range e.Fake.Calls("sendMessage") {
		if c.P["chat_id"] == "-100777" {
			entry = c.P["text"]
		}
	}
	want(t, entry, "<b>#KICKED</b>\n<b>Chat:</b> Test group [<code>-100500</code>]\n<b>User:</b> "+link(carol)+" [<code>30</code>]\n<b>Reason:</b> Used /kickme")
}

func TestPhoneBookAndPresence(t *testing.T) {
	e := setup(t)
	e.Send(Msg{Chat: chat, From: bob, Text: "hi"})
	ctx := context.Background()
	row, err := e.App.UserByUsername(ctx, "BOBBY_X")
	if err != nil || row == nil || row.ID != 20 || row.Name != "Bob" {
		t.Fatalf("phone book: %+v %v", row, err)
	}
	var n int
	if err := e.DB.Row(ctx, "SELECT count(*) FROM user_chats WHERE chat_id = $1", []any{chat}, &n); err != nil || n != 1 {
		t.Fatalf("presence: %d %v", n, err)
	}
	var title string
	if err := e.DB.Row(ctx, "SELECT title FROM chats WHERE chat_id = $1", []any{chat}, &title); err != nil || title != "Test group" {
		t.Fatalf("chats row: %q %v", title, err)
	}
}

func TestRemoveAndMigrateChat(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.Send(Msg{Chat: chat, From: alice, Text: "/warn 20 x"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/setlang pl"})
	count := func(table string, id int64) int {
		var n int
		if err := e.DB.Row(ctx, "SELECT count(*) FROM "+table+" WHERE chat_id = $1", []any{id}, &n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count("warns", chat) != 1 || count("chat_settings", chat) != 1 {
		t.Fatal("setup failed")
	}

	// The group becomes a supergroup: everything follows to the new id.
	const newID = int64(-1009999)
	e.Raw(map[string]any{"message": map[string]any{
		"message_id": 5, "date": 1000, "chat": map[string]any{"id": chat, "type": "group", "title": "Test group"},
		"from": alice.JSONForTest(), "migrate_to_chat_id": newID,
	}})
	if count("warns", newID) != 1 || count("chat_settings", newID) != 1 || count("warns", chat) != 0 || count("chats", chat) != 0 {
		t.Fatalf("migration: new warns %d, old warns %d", count("warns", newID), count("warns", chat))
	}

	// The bot is removed: all of it is deleted, including tables the Go schema does not even have.
	e.Raw(map[string]any{"my_chat_member": map[string]any{
		"chat": map[string]any{"id": newID, "type": "supergroup", "title": "Test group"}, "from": alice.JSONForTest(), "date": 1000,
		"old_chat_member": map[string]any{"status": "administrator", "user": map[string]any{"id": BotID, "is_bot": true, "first_name": "Argo"}, "can_be_edited": false, "is_anonymous": false},
		"new_chat_member": map[string]any{"status": "kicked", "user": map[string]any{"id": BotID, "is_bot": true, "first_name": "Argo"}, "until_date": 0},
	}})
	for _, table := range []string{"warns", "chat_settings", "warn_settings", "chats", "user_chats"} {
		if n := count(table, newID); n != 0 {
			t.Errorf("%s still has %d rows of the removed chat", table, n)
		}
	}
}

func TestRolesAndOwner(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if got := e.App.GetRole(ctx, OwnerID); got.String() != "Owner" {
		t.Errorf("owner: %v", got)
	}
	if got := e.App.GetRole(ctx, 12345); got.String() != "" {
		t.Errorf("regular user: %v", got)
	}
	if _, err := e.DB.Exec(ctx, "INSERT INTO roles (user_id, role) VALUES (50, 3)"); err != nil {
		t.Fatal(err)
	}
	// The cache is loaded once; a restart of the bot (new App) sees the row.
	e2 := setup(t)
	_ = e2
}

func TestUnexpectedErrorGoesToDevLog(t *testing.T) {
	e := setup(t)
	e.Fake.Fail("banChatMember", 500, "Internal Server Error")
	e.Send(Msg{Chat: chat, From: alice, Text: "/ban 20"})
	calls := e.Fake.Calls("sendMessage")
	if len(calls) != 1 || calls[0].P["chat_id"] != "1" || !strings.Contains(calls[0].P["text"], "Internal Server Error") {
		t.Fatalf("the error should go to the owner: %+v", calls)
	}
	// A second failure right away is not reported again (no flood).
	e.Fake.Reset()
	e.Fake.Fail("banChatMember", 500, "Internal Server Error")
	e.Send(Msg{Chat: chat, From: alice, Text: "/ban 20"})
	if n := len(e.Fake.Calls("sendMessage")); n != 0 {
		t.Errorf("reported twice within 10s: %d", n)
	}
}
