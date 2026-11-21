package core

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Group administrators: a bot cannot list ordinary members, but Telegram does tell it who the admins
// are, so they are recorded in the background, at most every few hours per group.

const (
	adminRefreshEvery = 6 * time.Hour
	adminRefreshLimit = 50_000
)

type adminRefreshState struct {
	mu      sync.Mutex
	checked map[int64]time.Time
	wg      sync.WaitGroup
}

// ScheduleAdminRefresh records the administrators of a group in the background. It is done at most every six hours
// per group (right away with force, for instance when the bot has just been added) and never delays an update.
func (a *App) ScheduleAdminRefresh(chatID int64, force bool) {
	s := &a.adminRefresh
	s.mu.Lock()
	last, ok := s.checked[chatID]
	if !force && ok && time.Since(last) < adminRefreshEvery {
		s.mu.Unlock()
		return
	}
	if s.checked == nil || len(s.checked) >= adminRefreshLimit {
		s.checked = map[int64]time.Time{}
	}
	s.checked[chatID] = time.Now() // marked now so a burst of updates starts only one request
	s.wg.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		admins, err := a.Bot.GetChatAdministratorsWithContext(ctx, chatID, nil)
		if err != nil {
			slog.Debug("Could not read the administrators", "chat", chatID, "err", err)
			return
		}
		for _, m := range admins {
			u := m.GetUser()
			if err := a.Remember(ctx, &u); err != nil {
				slog.Debug("Could not record an administrator", "chat", chatID, "err", err)
			}
		}
	}()
}

// WaitAdminRefresh waits for the refreshes that are running (for tests and for stopping).
func (a *App) WaitAdminRefresh() { a.adminRefresh.wg.Wait() }
