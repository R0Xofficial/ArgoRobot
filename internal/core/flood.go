package core

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Antiflood: settings of a chat and the counting of messages. The commands and the action taken
// are in modules/antiflood.go.
//
// Two things can trigger flood control, and a chat can use either or both:
//
//	the limit  more than N messages in a row from one person (nobody else wrote in between). Off by default.
//	the timer  N messages from one person within some seconds, whoever else writes. Off by default.
//
// Counting happens in memory only: it is about the last few seconds, and losing it on a restart costs nothing.

const (
	FloodMinCount, FloodMaxCount     = 2, 100
	FloodMinSeconds, FloodMaxSeconds = 1, 24 * 3600
	maxTrackedIDs                    = 100 // messages remembered for /clearflood (deleteMessages takes at most 100)
)

// FloodSettings are the settings of a chat.
type FloodSettings struct {
	Limit        int // 0 = off
	TimerCount   int // 0 = off
	TimerSeconds int
	Mode         string
	ModeSeconds  int
	Clear        bool
	Antichannel  bool
}

// Active tells whether the chat counts messages at all.
func (s FloodSettings) Active() bool { return s.Limit != 0 || s.TimerCount != 0 }

func defaultFlood() FloodSettings { return FloodSettings{Mode: "mute"} }

type floodRun struct {
	userID int64
	count  int
	ids    []int64
	group  string // the album the last message belonged to
}

type timedSeen struct {
	at    time.Time
	id    int64
	group string
}

type floodState struct {
	settings cache[int64, FloodSettings]
	mu       sync.Mutex
	runs     map[int64]*floodRun
	timed    map[[2]int64][]timedSeen
}

// FloodSettings reads the settings of a chat.
func (a *App) FloodSettings(ctx context.Context, chatID int64) (FloodSettings, error) {
	if s, ok := a.flood.settings.get(chatID); ok {
		return s, nil
	}
	s := defaultFlood()
	found, err := a.DB.Found(ctx, `SELECT flood_limit, timer_count, timer_seconds, mode, mode_seconds, clear_flood, antichannel
FROM flood_settings WHERE chat_id = $1`, []any{chatID}, &s.Limit, &s.TimerCount, &s.TimerSeconds, &s.Mode, &s.ModeSeconds, &s.Clear, &s.Antichannel)
	if err != nil {
		return defaultFlood(), err
	}
	if !found {
		s = defaultFlood()
	}
	a.flood.settings.put(chatID, s)
	return s, nil
}

var floodColumns = map[string]bool{"flood_limit": true, "timer_count": true, "timer_seconds": true, "mode": true,
	"mode_seconds": true, "clear_flood": true, "antichannel": true}

// UpdateFlood changes some settings of a chat (column name -> value) and returns all of them.
func (a *App) UpdateFlood(ctx context.Context, chatID int64, fields map[string]any) (FloodSettings, error) {
	var names []string
	var args = []any{chatID}
	var holders, sets []string
	for name, v := range fields {
		if !floodColumns[name] {
			return FloodSettings{}, fmt.Errorf("unknown flood setting %q", name)
		}
		names = append(names, name)
		args = append(args, v)
		holders = append(holders, fmt.Sprintf("$%d", len(args)))
		sets = append(sets, fmt.Sprintf("%s = EXCLUDED.%s", name, name))
	}
	if len(names) == 0 {
		return FloodSettings{}, fmt.Errorf("no flood settings given")
	}
	if _, err := a.DB.Exec(ctx, fmt.Sprintf("INSERT INTO flood_settings (chat_id, %s) VALUES ($1, %s) ON CONFLICT (chat_id) DO UPDATE SET %s",
		strings.Join(names, ", "), strings.Join(holders, ", "), strings.Join(sets, ", ")), args...); err != nil {
		return FloodSettings{}, err
	}
	a.flood.settings.drop(chatID)
	return a.FloodSettings(ctx, chatID)
}

// RecordFlood counts a message. It returns the ids of the messages that make up the flood when a limit was reached
// (and starts counting again), otherwise nil. An album counts as one message, however many photos it holds.
func (a *App) RecordFlood(s FloodSettings, chatID, userID, messageID int64, now time.Time, group string) []int64 {
	f := &a.flood
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.runs == nil || len(f.runs) >= cacheLimit {
		f.runs = map[int64]*floodRun{}
	}
	run := f.runs[chatID]
	if run == nil || run.userID != userID {
		run = &floodRun{userID: userID}
		f.runs[chatID] = run
	}
	sameAlbum := group != "" && run.group == group
	run.group = group
	if !sameAlbum {
		run.count++
	}
	run.ids = append(run.ids, messageID)
	if len(run.ids) > maxTrackedIDs {
		run.ids = run.ids[len(run.ids)-maxTrackedIDs:]
	}

	var ids []int64
	if s.Limit != 0 && run.count > s.Limit {
		ids = append(ids, run.ids...)
	}

	if s.TimerCount != 0 {
		if f.timed == nil || len(f.timed) >= cacheLimit {
			f.timed = map[[2]int64][]timedSeen{}
		}
		key := [2]int64{chatID, userID}
		seen := append(f.timed[key], timedSeen{now, messageID, group})
		if len(seen) > maxTrackedIDs {
			seen = seen[len(seen)-maxTrackedIDs:]
		}
		for len(seen) > 0 && now.Sub(seen[0].at) > time.Duration(s.TimerSeconds)*time.Second {
			seen = seen[1:]
		}
		f.timed[key] = seen
		distinct := map[string]bool{}
		for _, it := range seen { // an album's photos share one group key
			if it.group != "" {
				distinct["g"+it.group] = true
			} else {
				distinct[fmt.Sprint("m", it.id)] = true
			}
		}
		if len(distinct) >= s.TimerCount && ids == nil {
			for _, it := range seen {
				ids = append(ids, it.id)
			}
		}
	}
	if ids != nil {
		f.resetLocked(chatID, userID)
	}
	return ids
}

func (f *floodState) resetLocked(chatID, userID int64) {
	if run := f.runs[chatID]; run != nil && run.userID == userID {
		delete(f.runs, chatID)
	}
	delete(f.timed, [2]int64{chatID, userID})
}

func (a *App) forgetFlood(chatID int64) {
	a.flood.settings.drop(chatID)
	a.flood.mu.Lock()
	defer a.flood.mu.Unlock()
	delete(a.flood.runs, chatID)
	for k := range a.flood.timed {
		if k[0] == chatID {
			delete(a.flood.timed, k)
		}
	}
}
