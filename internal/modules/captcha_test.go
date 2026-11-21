package modules_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/PaulSonOfLars/gotgbot/v2"
	"image/png"
	"strconv"
	"strings"
	"testing"
	"time"

	"argo/internal/core"
	"argo/internal/i18n"
	. "argo/internal/testutil"
)

func callbackData(call Call) []string {
	var markup struct {
		Rows [][]struct {
			Data string `json:"callback_data"`
			Text string `json:"text"`
		} `json:"inline_keyboard"`
	}
	_ = json.Unmarshal([]byte(call.P["reply_markup"]), &markup)
	var out []string
	for _, row := range markup.Rows {
		for _, b := range row {
			out = append(out, b.Data)
		}
	}
	return out
}

func buttonTexts(call Call) []string {
	var markup struct {
		Rows [][]struct {
			Text string `json:"text"`
		} `json:"inline_keyboard"`
	}
	_ = json.Unmarshal([]byte(call.P["reply_markup"]), &markup)
	var out []string
	for _, row := range markup.Rows {
		for _, b := range row {
			out = append(out, b.Text)
		}
	}
	return out
}

func lastCall(e *Env, method string) Call {
	calls := e.Fake.Calls(method)
	if len(calls) == 0 {
		e.T.Fatalf("no %s call", method)
	}
	return calls[len(calls)-1]
}

// pressIn presses a button of a message in the private chat of the person.
func pressIn(e *Env, chatID int64, typ string, from Person, data string) {
	e.Raw(map[string]any{"callback_query": map[string]any{
		"id": "cb", "from": user(from), "chat_instance": "x", "data": data,
		"message": map[string]any{"message_id": 556, "date": 1000, "chat": map[string]any{"id": chatID, "type": typ, "title": "Test group"}, "text": "bot message"},
	}})
}

func appCtx(e *Env) *core.Ctx {
	return &core.Ctx{App: e.App, Bot: e.Bot, Ctx: e.Ctx(), L: i18n.English}
}

func newMember(e *Env, p Person) {
	e.Fake.SetMember(chat, p.ID, p.Name, "member", nil)
}

func mute(e *Env, p Person) { // what Telegram would now say about the person
	e.Fake.SetMember(chat, p.ID, p.Name, "restricted", map[string]any{"can_send_messages": false, "is_member": true})
}

func TestCaptchaTests(t *testing.T) {
	for i := 0; i < 200; i++ {
		m := core.MakeCaptcha("math", nil)
		options := strings.Split(m.Layout, ",")
		seen := map[string]bool{}
		for _, o := range options {
			seen[o] = true
			if n, err := strconv.Atoi(o); err != nil || n < 0 {
				t.Fatalf("option %q", o)
			}
		}
		if len(options) != 6 || len(seen) != 6 || !seen[m.Answer] || !strings.HasSuffix(m.Question, "= ?") {
			t.Fatalf("math: %+v", m)
		}
		var a, b int
		var op string
		if _, err := fmt.Sscanf(m.Question, "%d %s %d = ?", &a, &op, &b); err != nil {
			t.Fatalf("question %q: %v", m.Question, err)
		}
		want := map[string]int{"+": a + b, "-": a - b, "×": a * b}[op]
		if strconv.Itoa(want) != m.Answer {
			t.Fatalf("%q is not %s", m.Question, m.Answer)
		}
		x := core.MakeCaptcha("text", nil)
		if len(x.Answer) != 5 || len(x.Layout) != 5 || x.Layout == x.Answer {
			t.Fatalf("text: %+v", x)
		}
		letters := map[rune]bool{}
		for _, r := range x.Answer {
			letters[r] = true
			if !strings.ContainsRune("ABCDEFGHJKLMNPQRSTUVWXYZ23456789", r) || !strings.ContainsRune(x.Layout, r) {
				t.Fatalf("text: %+v", x)
			}
		}
		if len(letters) != 5 {
			t.Fatalf("distinct: %+v", x)
		}
	}
	if m := core.MakeCaptcha("button", nil); m.Answer != "" || m.Layout != "" {
		t.Fatalf("button: %+v", m)
	}
	img, err := png.Decode(bytes.NewReader(core.CaptchaPicture("AB2CD", nil)))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 40+5*54 || b.Dy() != 116 {
		t.Fatalf("size %v", b)
	}
	if !core.CaptchaPictureWorks() {
		t.Fatal("the picture works")
	}
}

func TestCaptchaCommands(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: bob, Text: "/captcha on"})
	contains(t, lastText(t, e), "Only chat admins")
	e.Send(Msg{Chat: chat, From: alice, Text: "/captcha"})
	contains(t, lastText(t, e), "<b>Enabled:</b> <code>off</code>")
	e.Send(Msg{Chat: chat, From: alice, Text: "/captcha maybe"})
	contains(t, lastText(t, e), "Use /captcha")
	e.Send(Msg{Chat: chat, From: alice, Text: "/captcha on"})
	contains(t, lastText(t, e), "Captchas are <b>on</b>")
	e.Send(Msg{Chat: chat, From: alice, Text: "/captcha on"})
	contains(t, lastText(t, e), "already")
	e.Send(Msg{Chat: chat, From: alice, Text: "/captchamode"})
	contains(t, lastText(t, e), "button")
	e.Send(Msg{Chat: chat, From: alice, Text: "/captchamode hard"})
	contains(t, lastText(t, e), "Use /captchamode")
	e.Send(Msg{Chat: chat, From: alice, Text: "/captchamode text2"})
	contains(t, lastText(t, e), "text2")
	e.Send(Msg{Chat: chat, From: alice, Text: "/captcharules on"})
	contains(t, lastText(t, e), "no rules yet")
	e.Send(Msg{Chat: chat, From: alice, Text: "/captcharules"})
	contains(t, lastText(t, e), "no rules yet")
	e.Send(Msg{Chat: chat, From: alice, Text: "/captchamutetime 30s"})
	contains(t, lastText(t, e), "Use /captchamutetime")
	e.Send(Msg{Chat: chat, From: alice, Text: "/captchamutetime 2h"})
	contains(t, lastText(t, e), "2 hours")
	e.Send(Msg{Chat: chat, From: alice, Text: "/captchakicktime 10m"})
	contains(t, lastText(t, e), "10 minutes")
	contains(t, lastText(t, e), "off")
	e.Send(Msg{Chat: chat, From: alice, Text: "/captchakick on"})
	contains(t, lastText(t, e), "10 minutes")
	e.Send(Msg{Chat: chat, From: alice, Text: "/setcaptchatext " + strings.Repeat("x", 41)})
	contains(t, lastText(t, e), "too long")
	e.Send(Msg{Chat: chat, From: alice, Text: "/setcaptchatext  I am <b>human</b> "})
	contains(t, lastText(t, e), "I am &lt;b&gt;human&lt;/b&gt;")
	e.Send(Msg{Chat: chat, From: alice, Text: "/captcha"})
	got := lastText(t, e)
	contains(t, got, "<code>I am &lt;b&gt;human&lt;/b&gt;</code>")
	contains(t, got, "<code>text2</code>")
	e.Send(Msg{Chat: chat, From: alice, Text: "/resetcaptchatext"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/captcha off"})
	contains(t, lastText(t, e), "Captchas are <b>off</b>")
}

func TestCaptchaButtonFlow(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/captcha on"})
	newMember(e, carol)
	e.Fake.Reset()
	e.Send(joinMsg(carol))
	if c := e.Fake.Calls("restrictChatMember"); len(c) != 1 || c[0].P["user_id"] != "30" || c[0].P["permissions"] != "{}" {
		t.Fatalf("muted: %+v", c)
	}
	welcome := lastCall(e, "sendMessage")
	if data := callbackData(welcome); len(data) != 1 || data[0] != "capw:"+itoa(chat) {
		t.Fatalf("the welcome carries the button: %v", data)
	}
	if !e.App.CaptchaHeld(e.Ctx(), chat, carol.ID) {
		t.Fatal("held")
	}
	row, _ := e.App.CaptchaFind(e.Ctx(), chat, carol.ID)
	if !row.Attached || !row.Welcomed || row.Kind != "join" {
		t.Fatalf("row: %+v", row)
	}
	// somebody else pressing it is told they are fine
	e.Fake.Reset()
	e.Press(chat, bob, "capw:"+itoa(chat))
	if a := lastCall(e, "answerCallbackQuery"); !strings.Contains(a.P["text"], "already verified") || a.P["show_alert"] != "true" {
		t.Fatalf("answer: %+v", a.P)
	}
	// the right person is let in
	mute(e, carol)
	e.Fake.Reset()
	e.Press(chat, carol, "capw:"+itoa(chat))
	if c := e.Fake.Calls("restrictChatMember"); len(c) != 1 || !strings.Contains(c[0].P["permissions"], `"can_send_messages":true`) {
		t.Fatalf("unmuted: %+v", c)
	}
	if a := lastCall(e, "answerCallbackQuery"); !strings.Contains(a.P["text"], "verified") {
		t.Fatalf("answer: %+v", a.P)
	}
	if e.App.CaptchaHeld(e.Ctx(), chat, carol.ID) {
		t.Fatal("no longer held")
	}
	if len(e.Fake.Calls("sendMessage")) != 0 {
		t.Fatal("their welcome carried the button: no second one")
	}
	// somebody who has just passed is not tested again
	e.Fake.Reset()
	e.Send(joinMsg(carol))
	if len(e.Fake.Calls("restrictChatMember")) != 0 {
		t.Fatal("not tested again")
	}
}

func TestCaptchaNotForEveryone(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/captcha on"})
	newMember(e, carol)
	e.Send(Msg{Chat: chat, From: alice, Text: "/approve", Reply: &Msg{Chat: chat, From: carol, Text: "x"}})
	e.Fake.Reset()
	e.Send(joinMsg(carol)) // approved
	e.Send(joinMsg(Person{ID: OwnerID, Name: "Boss"}))
	e.Send(joinMsg(alice)) // an admin
	if len(e.Fake.Calls("restrictChatMember")) != 0 {
		t.Fatalf("held: %+v", e.Fake.Calls("restrictChatMember"))
	}
	// with the welcome off there is no button to carry: nobody is muted
	e.Send(Msg{Chat: chat, From: alice, Text: "/welcome off"})
	newMember(e, bob)
	e.Fake.Reset()
	e.Send(joinMsg(bob))
	if len(e.Fake.Calls("restrictChatMember")) != 0 {
		t.Fatal("no welcome, no captcha")
	}
}

func TestCaptchaMathInPrivate(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/captcha on"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/captchamode math"})
	newMember(e, carol)
	e.Send(joinMsg(carol))
	mute(e, carol)
	e.Fake.Reset()
	e.Press(chat, carol, "capw:"+itoa(chat))
	if a := lastCall(e, "answerCallbackQuery"); !strings.HasSuffix(a.P["url"], "?start=cap_"+itoa(chat)) {
		t.Fatalf("it sends them to the private chat: %+v", a.P)
	}
	c := appCtx(e)
	e.Fake.Reset()
	if note := c.OpenCaptchaInPrivate(&gotgbot.User{Id: carol.ID, FirstName: carol.Name}, chat, carol.ID); note != "" {
		t.Fatalf("note: %q", note)
	}
	private := lastCall(e, "sendMessage")
	if private.P["chat_id"] != itoa(carol.ID) {
		t.Fatalf("sent to the person: %+v", private.P)
	}
	contains(t, private.P["text"], "Solve this")
	row, _ := e.App.CaptchaFind(e.Ctx(), chat, carol.ID)
	options := strings.Split(row.Layout, ",")
	if len(callbackData(private)) != 6 || row.MessageChatID != carol.ID {
		t.Fatalf("buttons: %v %+v", callbackData(private), row)
	}
	// a wrong answer gives a new question
	wrong := 0
	for i, o := range options {
		if o != row.Answer {
			wrong = i
			break
		}
	}
	e.Fake.Reset()
	pressIn(e, carol.ID, "private", carol, fmt.Sprintf("cap:%d:%d", row.ID, wrong))
	if a := lastCall(e, "answerCallbackQuery"); !strings.Contains(a.P["text"], "new one") {
		t.Fatalf("answer: %+v", a.P)
	}
	if len(e.Fake.Calls("editMessageText")) != 1 {
		t.Fatal("the message shows the new question")
	}
	// somebody else may not press it
	row, _ = e.App.CaptchaFind(e.Ctx(), chat, carol.ID)
	e.Fake.Reset()
	pressIn(e, carol.ID, "private", bob, fmt.Sprintf("cap:%d:0", row.ID))
	if a := lastCall(e, "answerCallbackQuery"); !strings.Contains(a.P["text"], "isn't for you") {
		t.Fatalf("answer: %+v", a.P)
	}
	// the right one lets them in
	options = strings.Split(row.Layout, ",")
	right := 0
	for i, o := range options {
		if o == row.Answer {
			right = i
		}
	}
	e.Fake.Reset()
	pressIn(e, carol.ID, "private", carol, fmt.Sprintf("cap:%d:%d", row.ID, right))
	if e.App.CaptchaHeld(e.Ctx(), chat, carol.ID) {
		t.Fatal("through")
	}
	if c := e.Fake.Calls("restrictChatMember"); len(c) != 1 {
		t.Fatalf("unmuted: %+v", c)
	}
	if ed := lastCall(e, "editMessageText"); !strings.Contains(ed.P["text"], "Done") {
		t.Fatalf("told in private: %+v", ed.P)
	}
	// and now their welcome comes, as they were not welcomed under a button
	if row.Attached && len(e.Fake.Calls("sendMessage")) != 0 {
		t.Fatal("an attached welcome is not repeated")
	}
}

func TestCaptchaTextAndPicture(t *testing.T) {
	for _, mode := range []string{"text", "text2"} {
		e := setup2(t)
		e.Send(Msg{Chat: chat, From: alice, Text: "/captcha on"})
		e.Send(Msg{Chat: chat, From: alice, Text: "/captchamode " + mode})
		newMember(e, carol)
		e.Send(joinMsg(carol))
		mute(e, carol)
		e.Fake.Reset()
		if note := appCtx(e).OpenCaptchaInPrivate(&gotgbot.User{Id: carol.ID, FirstName: carol.Name}, chat, carol.ID); note != "" {
			t.Fatal(note)
		}
		method := "sendMessage"
		if mode == "text2" {
			method = "sendPhoto"
		}
		first := lastCall(e, method)
		if n := len(callbackData(first)); n != 5 {
			t.Fatalf("%s: five characters: %v", mode, callbackData(first))
		}
		row, _ := e.App.CaptchaFind(e.Ctx(), chat, carol.ID)
		if (mode == "text2") != row.IsPhoto {
			t.Fatalf("%s: photo %v", mode, row.IsPhoto)
		}
		press := func(i int) {
			pressIn(e, carol.ID, "private", carol, fmt.Sprintf("cap:%d:%d", row.ID, i))
		}
		// a wrong first character starts again
		layout := []rune(row.Layout)
		wrong := 0
		for i, r := range layout {
			if r != []rune(row.Answer)[0] {
				wrong = i
				break
			}
		}
		e.Fake.Reset()
		press(wrong)
		if a := lastCall(e, "answerCallbackQuery"); !strings.Contains(a.P["text"], "Wrong order") {
			t.Fatalf("%s: %+v", mode, a.P)
		}
		// the right order
		for _, ch := range row.Answer {
			idx := strings.IndexRune(row.Layout, ch)
			e.Fake.Reset()
			press(idx)
		}
		if e.App.CaptchaHeld(e.Ctx(), chat, carol.ID) {
			t.Fatalf("%s: through", mode)
		}
	}
}

func TestCaptchaRulesPhase(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/setrules Be *nice*"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/captcha on"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/captcharules on"})
	newMember(e, carol)
	e.Send(joinMsg(carol))
	mute(e, carol)
	e.Fake.Reset()
	// the button mode with rules: the rules come at once in the private chat, accepting them is the test
	e.Press(chat, carol, "capw:"+itoa(chat))
	if a := lastCall(e, "answerCallbackQuery"); !strings.Contains(a.P["url"], "start=cap_") {
		t.Fatalf("to the private chat: %+v", a.P)
	}
	e.Fake.Reset()
	if note := appCtx(e).OpenCaptchaInPrivate(&gotgbot.User{Id: carol.ID, FirstName: carol.Name}, chat, carol.ID); note != "" {
		t.Fatal(note)
	}
	rules := lastCall(e, "sendMessage")
	contains(t, rules.P["text"], "Be *nice*")
	row, _ := e.App.CaptchaFind(e.Ctx(), chat, carol.ID)
	if row.Phase != "rules" || len(callbackData(rules)) != 1 || callbackData(rules)[0] != fmt.Sprintf("capr:%d", row.ID) {
		t.Fatalf("rules phase: %+v %v", row, callbackData(rules))
	}
	e.Fake.Reset()
	pressIn(e, carol.ID, "private", bob, fmt.Sprintf("capr:%d", row.ID))
	if a := lastCall(e, "answerCallbackQuery"); !strings.Contains(a.P["text"], "isn't for you") {
		t.Fatalf("%+v", a.P)
	}
	pressIn(e, carol.ID, "private", carol, fmt.Sprintf("capr:%d", row.ID))
	if e.App.CaptchaHeld(e.Ctx(), chat, carol.ID) {
		t.Fatal("accepted the rules")
	}
}

func TestCaptchaSweep(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/captcha on"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/captchakick on"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/captchakicktime 5m"})
	e.Send(Msg{Chat: chat, From: alice, Text: "/captchamutetime 1h"})
	newMember(e, carol)
	newMember(e, bob)
	e.Send(joinMsg(carol))
	e.Send(joinMsg(bob))
	e.Fake.Reset()
	if err := e.App.SweepCaptchas(e.Ctx(), e.Bot); err != nil {
		t.Fatal(err)
	}
	if len(e.Fake.Calls("banChatMember")) != 0 {
		t.Fatal("nobody is due yet")
	}
	row, _ := e.App.CaptchaFind(e.Ctx(), chat, carol.ID)
	if row.KickAt == nil || row.UnmuteAt == nil {
		t.Fatalf("times: %+v", row)
	}
	// carol's time ran out: she is removed (and may come back); bob's mute time ended: he is let in
	past := time.Now().Add(-time.Minute)
	if _, err := e.DB.Exec(e.Ctx(), "UPDATE captcha_pending SET kick_at = $1 WHERE user_id = $2", past, carol.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.DB.Exec(e.Ctx(), "UPDATE captcha_pending SET unmute_at = $1, kick_at = NULL WHERE user_id = $2", past, bob.ID); err != nil {
		t.Fatal(err)
	}
	mute(e, bob)
	e.Fake.Reset()
	if err := e.App.SweepCaptchas(e.Ctx(), e.Bot); err != nil {
		t.Fatal(err)
	}
	if c := e.Fake.Calls("banChatMember"); len(c) != 1 || c[0].P["user_id"] != "30" {
		t.Fatalf("kicked: %+v", c)
	}
	if len(e.Fake.Calls("unbanChatMember")) != 1 {
		t.Fatal("and let back in")
	}
	if c := e.Fake.Calls("restrictChatMember"); len(c) != 1 || c[0].P["user_id"] != "20" {
		t.Fatalf("unmuted: %+v", c)
	}
	if e.App.CaptchaHeld(e.Ctx(), chat, carol.ID) || e.App.CaptchaHeld(e.Ctx(), chat, bob.ID) {
		t.Fatal("both are done with")
	}
}

func TestCaptchaOffReleases(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/captcha on"})
	newMember(e, carol)
	e.Send(joinMsg(carol))
	mute(e, carol)
	e.Fake.Reset()
	e.Send(Msg{Chat: chat, From: alice, Text: "/captcha off"})
	contains(t, lastText(t, e), "1 waiting member")
	if c := e.Fake.Calls("restrictChatMember"); len(c) != 1 {
		t.Fatalf("unmuted: %+v", c)
	}
	if e.App.CaptchaHeld(e.Ctx(), chat, carol.ID) {
		t.Fatal("let go")
	}
}

func TestCaptchaJoinRequest(t *testing.T) {
	e := setup2(t)
	e.Send(Msg{Chat: chat, From: alice, Text: "/captcha on"})
	e.Fake.Reset()
	e.Raw(map[string]any{"chat_join_request": map[string]any{
		"chat": map[string]any{"id": chat, "type": "supergroup", "title": "Test group"},
		"from": user(carol), "user_chat_id": carol.ID, "date": 1000,
	}})
	req := lastCall(e, "sendMessage")
	if req.P["chat_id"] != itoa(carol.ID) {
		t.Fatalf("sent privately: %+v", req.P)
	}
	contains(t, req.P["text"], "request")
	row, _ := e.App.CaptchaFind(e.Ctx(), chat, carol.ID)
	if row == nil || row.Kind != "request" || row.ExpiresAt == nil {
		t.Fatalf("row: %+v", row)
	}
	e.Fake.Reset()
	pressIn(e, carol.ID, "private", carol, fmt.Sprintf("cap:%d:0", row.ID))
	if c := e.Fake.Calls("approveChatJoinRequest"); len(c) != 1 || c[0].P["user_id"] != "30" {
		t.Fatalf("approved: %+v", c)
	}
	if e.App.CaptchaHeld(e.Ctx(), chat, carol.ID) {
		t.Fatal("through")
	}
	// the time ran out
	e.Raw(map[string]any{"chat_join_request": map[string]any{
		"chat": map[string]any{"id": chat, "type": "supergroup", "title": "Test group"},
		"from": user(bob), "user_chat_id": bob.ID, "date": 1000,
	}})
	e.Send(Msg{Chat: chat, From: alice, Text: "/captchakick on"})
	if _, err := e.DB.Exec(e.Ctx(), "UPDATE captcha_pending SET kick_at = $1 WHERE user_id = $2", time.Now().Add(-time.Minute), bob.ID); err != nil {
		t.Fatal(err)
	}
	e.Fake.Reset()
	if err := e.App.SweepCaptchas(e.Ctx(), e.Bot); err != nil {
		t.Fatal(err)
	}
	if c := e.Fake.Calls("declineChatJoinRequest"); len(c) != 1 || c[0].P["user_id"] != "20" {
		t.Fatalf("declined: %+v", c)
	}
}
