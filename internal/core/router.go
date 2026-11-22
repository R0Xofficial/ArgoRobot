package core

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sort"
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

	Origin int64  // the private chat a command came from, when it is run for a connected group
	Edited bool   // the message is an edited one
	Cmd    string // lowercase command name, without prefix and @bot
	Args   string // everything after the command, trimmed

	ServiceGone bool // a handler already deleted the service message
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
	module  string
	min     Role // who may use it; lower roles are not answered at all
}

type button struct {
	prefix  string
	handler Handler
	module  string
	min     Role
}

// Router sends commands and buttons to the modules that registered for them.
type Router struct {
	app       *App
	commands  map[string]*command
	buttons   []button
	watchers  []watcher
	members   []memberWatcher
	mine      []memberWatcher // updates about the bot's own membership
	requests  []requestWatcher
	reactions []reactionWatcher
	current   string // the module being registered
}

// MemberWatcher looks at a chat_member update: somebody joined, left, was banned or promoted. It returns true when
// it dealt with the update, which stops the watchers after it.
type MemberWatcher func(c *Ctx, e *gotgbot.ChatMemberUpdated) (bool, error)

type memberWatcher struct {
	module   string
	priority int
	name     string
	fn       MemberWatcher
}

// ReactionWatcher looks at a message_reaction update: somebody added or removed a reaction. It returns true when it
// dealt with the update, which stops the watchers after it. e.User is nil for a reaction made on behalf of a chat.
type ReactionWatcher func(c *Ctx, e *gotgbot.MessageReactionUpdated) (bool, error)

type reactionWatcher struct {
	module   string
	priority int
	name     string
	fn       ReactionWatcher
}

// WatchReactions registers a watcher of reactions. Lower priorities run first.
func (r *Router) WatchReactions(priority int, name string, fn ReactionWatcher) {
	r.reactions = append(r.reactions, reactionWatcher{r.current, priority, name, fn})
	sort.SliceStable(r.reactions, func(i, j int) bool { return r.reactions[i].priority < r.reactions[j].priority })
}

// RequestWatcher looks at a join request: somebody asked to join a chat that approves its members.
type RequestWatcher func(c *Ctx, req *gotgbot.ChatJoinRequest) error

type requestWatcher struct {
	module string
	name   string
	fn     RequestWatcher
}

// WatchJoinRequests registers a watcher of join requests. All of them see every request.
func (r *Router) WatchJoinRequests(name string, fn RequestWatcher) {
	r.requests = append(r.requests, requestWatcher{r.current, name, fn})
}

// WatchMyMember registers a watcher of my_chat_member updates: the bot was added, removed or its rights changed.
func (r *Router) WatchMyMember(name string, fn MemberWatcher) {
	r.mine = append(r.mine, memberWatcher{r.current, 0, name, fn})
}

// WatchMembers registers a watcher of chat_member updates. Lower priorities run first.
func (r *Router) WatchMembers(priority int, name string, fn MemberWatcher) {
	r.members = append(r.members, memberWatcher{r.current, priority, name, fn})
	sort.SliceStable(r.members, func(i, j int) bool { return r.members[i].priority < r.members[j].priority })
}

// Watcher looks at every message (and edited message), not only commands. It returns true when it dealt with the
// message, which stops everything after it.
type Watcher func(*Ctx) (bool, error)

type watcher struct {
	module   string
	priority int
	name     string
	fn       Watcher
}

// Watch registers a watcher. Negative priorities run before commands, the others after them; lower runs first.
func (r *Router) Watch(priority int, name string, fn Watcher) {
	r.watchers = append(r.watchers, watcher{r.current, priority, name, fn})
	sort.SliceStable(r.watchers, func(i, j int) bool { return r.watchers[i].priority < r.watchers[j].priority })
}

func newRouter(app *App) *Router {
	return &Router{app: app, commands: map[string]*command{}}
}

// Command registers a handler for one or more command names (case does not matter).
func (r *Router) Command(handler Handler, names ...string) { r.CommandRole(NoRole, handler, names...) }

// CommandRole is Command for people who hold at least the role; anyone else is not answered.
func (r *Router) CommandRole(min Role, handler Handler, names ...string) {
	c := &command{names: names, handler: handler, module: r.current, min: min}
	r.app.recordCommand(names, r.current)
	for _, name := range names {
		r.commands[strings.ToLower(name)] = c
	}
}

// Button registers a handler for buttons whose data starts with prefix. The first registered prefix that fits wins.
func (r *Router) Button(prefix string, handler Handler) { r.ButtonRole(NoRole, prefix, handler) }

// ButtonRole is Button for people who hold at least the role.
func (r *Router) ButtonRole(min Role, prefix string, handler Handler) {
	r.buttons = append(r.buttons, button{prefix, handler, r.current, min})
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
			r.app.reportError("a handler (panic)", p, c, debug.Stack())
		}
	}()

	// Middlewares. The phone book is a convenience and never a reason to drop an update.
	r.app.recordUpdate(c)

	switch {
	case update.Message != nil || update.EditedMessage != nil:
		m := update.Message
		if m == nil {
			m = update.EditedMessage
			c.Edited = true
		}
		c.Msg, c.User = m, m.From
		r.prepare(c, m.Chat.Id)
		r.connected(c, b)
		if !c.Edited {
			defer r.cleanService(c)
		}
		if r.watch(c, true) {
			return
		}
		body := m.Text
		if body == "" {
			body = m.Caption // a command can be the caption of a photo
		}
		if body != "" && !c.Edited {
			name, args, ok := ParseCommand(body, r.app.Cfg.CommandPrefix, b.Username)
			if ok {
				if cmd := r.commands[name]; cmd != nil && c.active(cmd.module) {
					if c.Role < cmd.min {
						return
					}
					c.Cmd, c.Args = name, args
					if c.User != nil {
						slog.Info("command", "module", "modules."+cmd.module, "command", "/"+name, "user", c.User.Id, "username", c.User.Username, "chat", m.Chat.Id, "type", m.Chat.Type)
					}
					// A chat can switch a command off for its members (the CommandGuard).
					if !isGroupType(m.Chat.Type) || !c.blocked(name, cmd.module) {
						r.run(c, cmd.handler)
					}
					r.cleanCommand(c, b)
					return
				}
			}
		}
		r.watch(c, false)
	case update.ChatMember != nil:
		e := update.ChatMember
		c.User = &e.From
		r.prepare(c, e.Chat.Id)
		for _, w := range r.members {
			if !c.active(w.module) {
				continue
			}
			handled, err := w.fn(c, e)
			if err != nil {
				slog.Error("Member watcher failed", "watcher", w.name, "err", err)
				r.app.reportError(w.name, err, c, nil)
			}
			if handled {
				break
			}
		}
	case update.MyChatMember != nil:
		e := update.MyChatMember
		c.User = &e.From
		r.prepare(c, e.Chat.Id)
		r.app.chatEvent(c, e)
		for _, w := range r.mine {
			if !r.app.moduleEnabled(w.module) {
				continue
			}
			if handled, err := w.fn(c, e); err != nil {
				slog.Error("Member watcher failed", "watcher", w.name, "err", err)
				r.app.reportError(w.name, err, c, nil)
			} else if handled {
				break
			}
		}
	case update.MessageReaction != nil:
		e := update.MessageReaction
		c.User = e.User
		r.prepare(c, e.Chat.Id)
		for _, w := range r.reactions {
			if !c.active(w.module) {
				continue
			}
			handled, err := w.fn(c, e)
			if err != nil {
				slog.Error("Reaction watcher failed", "watcher", w.name, "err", err)
				r.app.reportError(w.name, err, c, nil)
			}
			if handled {
				break
			}
		}
	case update.ChatJoinRequest != nil:
		req := update.ChatJoinRequest
		c.User = &req.From
		r.prepare(c, req.Chat.Id)
		for _, w := range r.requests {
			if !c.active(w.module) {
				continue
			}
			if err := w.fn(c, req); err != nil {
				slog.Error("Join request watcher failed", "watcher", w.name, "err", err)
				r.app.reportError(w.name, err, c, nil)
			}
		}
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
				r.prepare(c, chatID)
				if !c.active(bt.module) || c.Role < bt.min {
					continue
				}
				slog.Info("callback", "module", "modules."+bt.module, "data", cb.Data, "user", cb.From.Id, "username", cb.From.Username)
				r.run(c, bt.handler)
				return
			}
		}
	}
}

// watch runs the watchers before (early) or after the commands. It reports whether one of them took the message.
func (r *Router) watch(c *Ctx, early bool) bool {
	for _, w := range r.watchers {
		if (w.priority < 0) != early || !c.active(w.module) {
			continue
		}
		handled, err := w.fn(c)
		if err != nil {
			slog.Error("Watcher failed", "watcher", w.name, "err", err)
			r.app.reportError(w.name, err, c, nil)
		}
		if handled {
			return true
		}
	}
	return false
}

// prepare is the RolesMiddleware and the LanguageMiddleware.
func (r *Router) prepare(c *Ctx, chatID int64) {
	if c.User != nil {
		c.Role = r.app.GetRole(c.Ctx, c.User.Id)
	}
	c.L = r.app.UseChat(c.Ctx, chatID)
}

func (r *Router) run(c *Ctx, h Handler) {
	if c.Cb == nil && c.Cmd != "" {
		if cmd := r.commands[c.Cmd]; cmd != nil && r.app.moduleEnglish(cmd.module) {
			c.L = i18n.English // the commands of the people who run the bot are English only
		}
	}
	if c.Cb != nil {
		for _, bt := range r.buttons {
			if strings.HasPrefix(c.Cb.Data, bt.prefix) && r.app.moduleEnglish(bt.module) {
				c.L = i18n.English
				break
			}
		}
	}
	if err := h(c); err != nil {
		slog.Error("Handler failed", "cmd", c.Cmd, "err", err)
		what := "/" + c.Cmd
		if c.Cb != nil {
			what = "a button (" + c.Cb.Data + ")"
		}
		r.app.reportError(what, err, c, nil)
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
