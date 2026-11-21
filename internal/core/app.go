// Package core holds what every module shares: the application object, the update router and the
// middlewares, roles, the user phone book, chat settings, target resolution and the moderation helpers.
package core

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/config"
	"argo/internal/db"
	"argo/internal/text"
)

// App is everything a handler may need. One is made at start-up and shared.
type App struct {
	Cfg *config.Settings
	DB  *db.DB
	Bot *gotgbot.Bot

	Router *Router

	roles    rolesCache
	users    usersCache
	chats    chatsCache
	settings settingsCache
	logs     logsCache
	langs    langCache

	errMu      sync.Mutex
	lastReport time.Time
}

func NewApp(cfg *config.Settings, database *db.DB, bot *gotgbot.Bot) *App {
	app := &App{Cfg: cfg, DB: database, Bot: bot}
	app.Router = newRouter(app)
	return app
}

// cache is a small bounded map: when full it is simply emptied (the database is the truth).
type cache[K comparable, V any] struct {
	mu sync.Mutex
	m  map[K]V
}

const cacheLimit = 20000

func (c *cache[K, V]) get(k K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[k]
	return v, ok
}

func (c *cache[K, V]) put(k K, v V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) >= cacheLimit {
		c.m = map[K]V{}
	}
	c.m[k] = v
}

func (c *cache[K, V]) drop(k K) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, k)
}

func (a *App) bg() context.Context { return context.Background() }

func logErr(what string, err error) {
	if err != nil {
		slog.Warn(what, "err", err)
	}
}

var errNoRows = db.ErrNoRows

// DevLog sends a message to the developer log (DEV_LOG_CHAT, else the owner's private chat). It never fails.
func (a *App) DevLog(ctx context.Context, msg string) {
	chat, thread := a.Cfg.DevLogChat, a.Cfg.DevLogThread
	if chat == 0 {
		chat, thread = a.Cfg.OwnerID, 0
	}
	if _, err := a.Bot.SendMessageWithContext(ctx, chat, msg, &gotgbot.SendMessageOpts{
		ParseMode: "HTML", MessageThreadId: thread, LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true},
	}); err != nil {
		slog.Warn("Could not send a developer log message", "err", err)
	}
}

// reportError tells the developer log about a failure, at most once every few seconds so a loop of failures
// can't flood it.
func (a *App) reportError(what string, err any) {
	a.errMu.Lock()
	quiet := time.Since(a.lastReport) < 10*time.Second
	if !quiet {
		a.lastReport = time.Now()
	}
	a.errMu.Unlock()
	if quiet {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a.DevLog(ctx, "<b>Error</b> in "+text.Esc(what)+":\n<code>"+text.Esc(text.Truncate(fmt.Sprint(err), 1500))+"</code>")
}
