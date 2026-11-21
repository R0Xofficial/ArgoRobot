package testutil

import (
	"context"
	"encoding/json"
	"os"
	"sync/atomic"
	"testing"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/config"
	"argo/internal/core"
	"argo/internal/db"
	"argo/internal/i18n"
)

const (
	OwnerID = 1
	BotID   = 999
)

// Env is a bot running against a fake Telegram and a real PostgreSQL database.
type Env struct {
	T    *testing.T
	App  *core.App
	Bot  *gotgbot.Bot
	Fake *Fake
	DB   *db.DB
	seq  atomic.Int64
}

// TestDatabaseURL is where the tests find a PostgreSQL they may wipe.
func TestDatabaseURL() string {
	if u := os.Getenv("TEST_DATABASE_URL"); u != "" {
		return u
	}
	return "postgres://postgres@/argotest?host=/tmp&port=54329"
}

// NewEnv wipes the test database, creates the schema and starts a bot against a fake Telegram.
// registerModules adds the modules under test (it is passed in so this package does not import them).
func NewEnv(t *testing.T, localesDir string, registerModules func(*core.App)) *Env {
	t.Helper()
	if err := i18n.Load(localesDir); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	database, err := db.Connect(ctx, TestDatabaseURL())
	if err != nil {
		t.Skipf("no test database: %v", err)
	}
	if _, err := database.Exec(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public"); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	fake := NewFake()
	bot, err := gotgbot.NewBot("123:TEST", &gotgbot.BotOpts{
		BotClient: &gotgbot.BaseBotClient{DefaultRequestOpts: &gotgbot.RequestOpts{APIURL: fake.Srv.URL}},
	})
	if err != nil {
		t.Fatal(err)
	}
	fake.Reset()
	app := core.NewApp(&config.Settings{OwnerID: OwnerID, CommandPrefix: "/!", SupportChatUsername: "@ArgoRobotSupport"}, database, bot)
	registerModules(app)
	if err := app.LoadModuleState(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fake.Close(); database.Close() })
	return &Env{T: t, App: app, Bot: bot, Fake: fake, DB: database}
}

// Person is a user in a test.
type Person struct {
	ID       int64
	Name     string
	Username string
}

// JSONForTest is the Telegram User object of the person.
func (p Person) JSONForTest() map[string]any { return p.json() }

func (p Person) json() map[string]any {
	u := map[string]any{"id": p.ID, "is_bot": false, "first_name": p.Name}
	if p.Username != "" {
		u["username"] = p.Username
	}
	return u
}

// Msg describes a message to send.
type Msg struct {
	Chat       int64
	Type       string // supergroup (default), group, private
	From       Person
	Text       string
	Entities   []map[string]any
	Reply      *Msg
	SenderChat map[string]any // written on behalf of a chat
	Photo      string         // file id: the message is a photo and Text is its caption
	Extra      map[string]any // more fields of the message (sticker, forward_origin, ...)
	Edited     bool           // delivered as an edited message
}

func (m Msg) json(id int64) map[string]any {
	typ := m.Type
	if typ == "" {
		typ = "supergroup"
	}
	chat := map[string]any{"id": m.Chat, "type": typ}
	if typ != "private" {
		chat["title"] = "Test group"
	}
	out := map[string]any{"message_id": id, "date": 1000, "chat": chat, "from": m.From.json(), "text": m.Text}
	if m.Photo != "" {
		delete(out, "text")
		out["photo"] = []map[string]any{{"file_id": m.Photo + "-small", "file_unique_id": "s", "width": 10, "height": 10}, {"file_id": m.Photo, "file_unique_id": "b", "width": 100, "height": 100}}
		if m.Text != "" {
			out["caption"] = m.Text
		}
	}
	for k, v := range m.Extra {
		out[k] = v
	}
	if len(m.Entities) > 0 {
		if m.Photo != "" {
			out["caption_entities"] = m.Entities
		} else {
			out["entities"] = m.Entities
		}
	}
	if m.SenderChat != nil {
		out["sender_chat"] = m.SenderChat
	}
	if m.Reply != nil {
		out["reply_to_message"] = m.Reply.json(id - 1)
	}
	return out
}

func (e *Env) deliver(update map[string]any) {
	e.T.Helper()
	update["update_id"] = e.seq.Add(1)
	raw, err := json.Marshal(update)
	if err != nil {
		e.T.Fatal(err)
	}
	var u gotgbot.Update
	if err := json.Unmarshal(raw, &u); err != nil {
		e.T.Fatalf("bad update: %v\n%s", err, raw)
	}
	e.App.Router.Handle(e.Bot, &u)
}

// Send delivers a message and waits until it is handled (handling is synchronous).
func (e *Env) Send(m Msg) {
	e.T.Helper()
	key := "message"
	if m.Edited {
		key = "edited_message"
	}
	e.deliver(map[string]any{key: m.json(e.seq.Load() + 100)})
}

// Press delivers a button press under a message of the bot in chat.
func (e *Env) Press(chat int64, from Person, data string) {
	e.T.Helper()
	e.deliver(map[string]any{"callback_query": map[string]any{
		"id": "cb", "from": from.json(), "chat_instance": "x", "data": data,
		"message": map[string]any{"message_id": 555, "date": 1000, "chat": map[string]any{"id": chat, "type": "supergroup", "title": "Test group"}, "text": "bot message"},
	}})
}

// Raw delivers an update given as JSON-like data.
func (e *Env) Raw(update map[string]any) { e.T.Helper(); e.deliver(update) }

// Mention is a text_mention entity (a person without a @username).
func TextMention(offset, length int, p Person) map[string]any {
	return map[string]any{"type": "text_mention", "offset": offset, "length": length, "user": p.json()}
}

func Mention(offset, length int) map[string]any {
	return map[string]any{"type": "mention", "offset": offset, "length": length}
}

// Ctx is a context for direct calls to the app in tests.
func (e *Env) Ctx() context.Context { return context.Background() }

// PressOn presses a button under the message with this id (the last one the bot sent, say) in a chat of a type.
func (e *Env) PressOn(chat int64, typ string, msgID int, from Person, data string) {
	e.T.Helper()
	e.deliver(map[string]any{"callback_query": map[string]any{
		"id": "cb", "from": from.json(), "chat_instance": "x", "data": data,
		"message": map[string]any{"message_id": msgID, "date": 1000, "chat": map[string]any{"id": chat, "type": typ, "title": "Test group"}, "text": "bot message"},
	}})
}
