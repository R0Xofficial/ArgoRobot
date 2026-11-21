package core

import (
	"context"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"

	"argo/internal/i18n"
)

// Role is a bot-wide role. The zero value means "regular user".
type Role int

const (
	NoRole Role = iota
	Support
	Sudo
	Developer
	Owner
)

func (r Role) String() string {
	switch r {
	case Support:
		return "Support"
	case Sudo:
		return "Sudo"
	case Developer:
		return "Developer"
	case Owner:
		return "Owner"
	}
	return ""
}

// Ctx is one update being handled: who wrote, in which chat, in which language.
type Ctx struct {
	App    *App
	Bot    *gotgbot.Bot
	Update *gotgbot.Update
	Ctx    context.Context

	Msg  *gotgbot.Message       // the message, or the message a button is under
	Cb   *gotgbot.CallbackQuery // set for button presses
	User *gotgbot.User          // who wrote or pressed
	Role Role
	L    i18n.Lang

	Cmd  string // lowercase command name, without prefix and @bot
	Args string // everything after the command, trimmed
}

// T writes a text in the language of the chat being handled.
func (c *Ctx) T(tag string, values ...i18n.V) string { return c.L.T(tag, values...) }

// Chat is the chat of the message (or of the message a button is under).
func (c *Ctx) Chat() gotgbot.Chat { return c.Msg.Chat }

// Handler handles a command or a button.
type Handler func(*Ctx) error

type command struct {
	names   []string
	handler Handler
}

type button struct {
	prefix  string
	handler Handler
}

// Router sends commands and buttons to the modules that registered for them.
type Router struct {
	app      *App
	commands map[string]*command
	buttons  []button
}

func newRouter(app *App) *Router {
	return &Router{app: app, commands: map[string]*command{}}
}

// Command registers a handler for one or more command names (case does not matter).
func (r *Router) Command(handler Handler, names ...string) {
	c := &command{names: names, handler: handler}
	for _, name := range names {
		r.commands[strings.ToLower(name)] = c
	}
}

// Button registers a handler for buttons whose data starts with prefix. The first registered prefix that fits wins.
func (r *Router) Button(prefix string, handler Handler) {
	r.buttons = append(r.buttons, button{prefix, handler})
}

// ParseCommand splits "/Ban@MyBot some args" into ("ban", "some args"). ok is false for anything that is not
// a command for this bot: no prefix, or addressed to another bot.
func ParseCommand(text, prefixes, botUsername string) (name, args string, ok bool) {
	if text == "" || !strings.ContainsRune(prefixes, []rune(text)[0]) {
		return "", "", false
	}
	word, rest := text, ""
	if i := strings.IndexFunc(text, unicode.IsSpace); i >= 0 {
		word, rest = text[:i], text[i:]
	}
	word = word[len(string([]rune(word)[0])):]
	name, addressed, _ := strings.Cut(word, "@")
	if name == "" {
		return "", "", false
	}
	if addressed != "" && !strings.EqualFold(addressed, botUsername) {
		return "", "", false
	}
	return strings.ToLower(name), strings.TrimSpace(rest), true
}

// Handle is the entry point for every update: middlewares first, then the handler.
func (r *Router) Handle(b *gotgbot.Bot, update *gotgbot.Update) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c := &Ctx{App: r.app, Bot: b, Update: update, Ctx: ctx, L: i18n.English}

	defer func() {
		if p := recover(); p != nil {
			slog.Error("Handler panicked", "panic", p)
			r.app.reportError("a handler (panic)", p)
		}
	}()

	// Middlewares. The phone book is a convenience and never a reason to drop an update.
	r.app.recordUpdate(c)

	switch {
	case update.Message != nil && update.Message.Text != "":
		c.Msg, c.User = update.Message, update.Message.From
		name, args, ok := ParseCommand(c.Msg.Text, r.app.Cfg.CommandPrefix, b.Username)
		if !ok {
			return
		}
		cmd := r.commands[name]
		if cmd == nil {
			return
		}
		c.Cmd, c.Args = name, args
		if c.User != nil {
			slog.Info("command", "command", "/"+name, "user", c.User.Id, "username", c.User.Username, "chat", c.Msg.Chat.Id, "type", c.Msg.Chat.Type)
		}
		r.prepare(c, c.Msg.Chat.Id)
		r.run(c, cmd.handler)
	case update.CallbackQuery != nil:
		cb := update.CallbackQuery
		c.Cb, c.User = cb, &cb.From
		chatID := cb.From.Id
		if m, ok := cb.Message.(gotgbot.Message); ok {
			c.Msg = &m
			chatID = m.Chat.Id
		}
		for _, bt := range r.buttons {
			if strings.HasPrefix(cb.Data, bt.prefix) {
				slog.Info("callback", "data", cb.Data, "user", cb.From.Id, "username", cb.From.Username)
				r.prepare(c, chatID)
				r.run(c, bt.handler)
				return
			}
		}
	}
}

// prepare is the RolesMiddleware and the LanguageMiddleware.
func (r *Router) prepare(c *Ctx, chatID int64) {
	if c.User != nil {
		c.Role = r.app.GetRole(c.Ctx, c.User.Id)
	}
	c.L = r.app.UseChat(c.Ctx, chatID)
}

func (r *Router) run(c *Ctx, h Handler) {
	if err := h(c); err != nil {
		slog.Error("Handler failed", "cmd", c.Cmd, "err", err)
		what := "/" + c.Cmd
		if c.Cb != nil {
			what = "a button (" + c.Cb.Data + ")"
		}
		r.app.reportError(what, err)
	}
}

// Handler adapts the router to gotgbot's dispatcher: every update goes through Handle.
func (r *Router) Handler() ext.Handler { return catchAll{r} }

type catchAll struct{ r *Router }

func (catchAll) Name() string                                { return "argo-router" }
func (catchAll) CheckUpdate(*gotgbot.Bot, *ext.Context) bool { return true }
func (h catchAll) HandleUpdate(b *gotgbot.Bot, c *ext.Context) error {
	h.r.Handle(b, c.Update)
	return nil
}
