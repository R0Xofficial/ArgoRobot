// Package core holds what every module shares: the application object, the update router and the
// middlewares, roles, the user phone book, chat settings, target resolution and the moderation helpers.
package core

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"argo/internal/blacklist"
	"argo/internal/config"
	"argo/internal/db"
)

// App is everything a handler may need. One is made at start-up and shared.
type App struct {
	Cfg *config.Settings
	DB  *db.DB
	Bot *gotgbot.Bot

	Router *Router

	// LogBuffer keeps the log for the scheduled report (nil = reports are off).
	LogBuffer *LogBuffer

	roles    rolesCache
	users    usersCache
	chats    chatsCache
	settings settingsCache
	logs     logsCache
	langs    langCache

	whitelist whitelistCache
	gbans     gbanState
	feds      fedState
	flood     floodState
	locks     cache[int64, *LockConfig]
	nightFail sync.Map // chat id -> time.Time: leave the chat alone until then
	raid      raidState
	marks     memberMarks
	captcha   captchaState
	prebans   prebanState
	cleanup   sync.Mutex // held while the chats are being checked

	registry     registry
	switches     cache[int64, map[string]bool]
	conns        cache[int64, *Connection]
	cooldowns    cache[string, time.Time]
	blTriggers   cache[int64, []*blacklist.Trigger]
	blSettings   cache[int64, BlacklistSettings]
	joinLists    cache[int64, *JoinLists]
	filterCache  cache[int64, *filterList]
	afk          afkState
	adminRefresh adminRefreshState
	mt           mtState

	errMu      sync.Mutex
	lastReport map[string]time.Time // an error by name and start of its text, when it was last reported
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

// ReportError is reportError for modules.
func (a *App) ReportError(what string, err any) { a.reportError(what, err, nil, nil) }

// Cooldown is true when nothing was answered under key for the last d; it then records that something is. It keeps
// a module from answering the same thing in a loop.
func (a *App) Cooldown(key string, d time.Duration) bool {
	now := time.Now()
	if last, ok := a.cooldowns.get(key); ok && now.Sub(last) < d {
		return false
	}
	a.cooldowns.put(key, now)
	return true
}

// ForgetCooldown lets key answer again at once (the thing it was recorded for did not happen after all).
func (a *App) ForgetCooldown(key string) { a.cooldowns.drop(key) }
