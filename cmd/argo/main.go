// Argo, a modular Telegram group-admin bot: the Go port (prototype).
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"

	"argo/internal/config"
	"argo/internal/core"
	"argo/internal/db"
	"argo/internal/i18n"
	"argo/internal/modules"
	"argo/internal/text"
)

// Update types that carry users or that handlers use. Telegram sends only the types that are asked for.
var allowedUpdates = []string{
	"message", "edited_message", "callback_query", "inline_query", "chosen_inline_result", "chat_member",
	"my_chat_member", "chat_join_request", "message_reaction", "poll_answer", "chat_boost", "removed_chat_boost",
}

const stopStepTimeout = 8 * time.Second // what a step that talks to Telegram may take while the bot stops

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Argo:", err)
		os.Exit(1)
	}
}

func setupLogging(level string) {
	lvl := slog.LevelInfo
	switch level {
	case "DEBUG":
		lvl = slog.LevelDebug
	case "WARNING", "WARN":
		lvl = slog.LevelWarn
	case "ERROR", "CRITICAL":
		lvl = slog.LevelError
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})))
}

// retry calls fn again while it fails (Telegram can't be reached for a moment). Used at start-up, where one bad
// moment of the network would otherwise stop the whole bot.
func retry(ctx context.Context, name string, fn func() error) error {
	delays := []time.Duration{2, 5, 10, 20, 30, 60}
	for attempt := 0; ; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}
		if attempt >= len(delays) || ctx.Err() != nil {
			return err
		}
		wait := delays[attempt] * time.Second
		slog.Warn("A call failed, trying again", "call", name, "err", err, "attempt", attempt+1, "in", wait)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func run() error {
	cfg, err := config.Load(".env")
	if err != nil {
		return fmt.Errorf("configuration error, check your .env file:\n%w", err)
	}
	setupLogging(cfg.LogLevel)

	if err := i18n.Load(cfg.LocalesDir); err != nil {
		return err
	}
	slog.Info("Languages loaded", "codes", strings.Join(i18n.Codes(), ", "))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var database *db.DB
	if err := retryDB(ctx, func() (err error) {
		database, err = db.Connect(ctx, cfg.DatabaseURL)
		return err
	}); err != nil {
		return err
	}
	defer database.Close()
	if err := database.Migrate(ctx); err != nil {
		return fmt.Errorf("could not prepare the database: %w", err)
	}

	bot, err := gotgbot.NewBot(cfg.BotToken, &gotgbot.BotOpts{
		DisableTokenCheck: true, // checked below, with retries
		BotClient: &gotgbot.BaseBotClient{
			Client:             httpClient(),
			DefaultRequestOpts: &gotgbot.RequestOpts{Timeout: 30 * time.Second, APIURL: cfg.TelegramAPIURL},
		},
	})
	if err != nil {
		return err
	}
	// Both calls are tried again when Telegram cannot be reached for a moment.
	if err := retry(ctx, "delete_webhook", func() error {
		_, err := bot.DeleteWebhookWithContext(ctx, &gotgbot.DeleteWebhookOpts{
			DropPendingUpdates: true, // updates that piled up while the bot was offline are discarded
			RequestOpts:        &gotgbot.RequestOpts{Timeout: 20 * time.Second},
		})
		return err
	}); err != nil {
		return err
	}
	if err := retry(ctx, "get_me", func() error {
		me, err := bot.GetMeWithContext(ctx, &gotgbot.GetMeOpts{RequestOpts: &gotgbot.RequestOpts{Timeout: 20 * time.Second}})
		if err == nil {
			bot.User = *me
		}
		return err
	}); err != nil {
		return err
	}
	slog.Info("Authorized", "username", bot.Username, "id", bot.Id)

	app := core.NewApp(cfg, database, bot)
	modules.Register(app)

	dispatcher := ext.NewDispatcher(&ext.DispatcherOpts{
		MaxRoutines: ext.DefaultMaxRoutines,
		Error: func(_ *gotgbot.Bot, _ *ext.Context, err error) ext.DispatcherAction {
			slog.Error("Update failed", "err", err)
			return ext.DispatcherActionNoop
		},
	})
	dispatcher.AddHandler(app.Router.Handler())
	updater := ext.NewUpdater(dispatcher, nil)

	devLog(ctx, app, fmt.Sprintf("Bot started (@%s).", text.Esc(bot.Username)))

	if err := updater.StartPolling(bot, &ext.PollingOpts{
		GetUpdatesOpts: &gotgbot.GetUpdatesOpts{
			Timeout:        30,
			AllowedUpdates: allowedUpdates,
			RequestOpts:    &gotgbot.RequestOpts{Timeout: 40 * time.Second},
		},
	}); err != nil {
		return fmt.Errorf("could not start polling: %w", err)
	}
	slog.Info("Polling for updates", "types", strings.Join(allowedUpdates, ", "))

	<-ctx.Done()

	// The notice goes first, and every step that talks to Telegram has a time limit: the container is killed when
	// stopping takes too long (stop_grace_period), and a slow Telegram must not eat that time.
	slog.Info("Shutting down")
	stopCtx, cancel := context.WithTimeout(context.Background(), stopStepTimeout)
	devLog(stopCtx, app, "Bot stopped.")
	cancel()
	done := make(chan struct{})
	go func() { _ = updater.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(stopStepTimeout):
		slog.Warn("Stopping the updater took too long")
	}
	return nil
}

func retryDB(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 1; attempt <= 6; attempt++ {
		if err = fn(); err == nil {
			return nil
		}
		slog.Warn("The database is not reachable yet", "err", err, "attempt", attempt)
		select {
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

// devLog sends a message to the developer log with the time limit of ctx.
func devLog(ctx context.Context, app *core.App, msg string) { app.DevLog(ctx, msg) }
