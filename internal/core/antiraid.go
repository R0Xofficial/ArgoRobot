package core

import (
	"context"
	"sync"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

// Antiraid: when many accounts join a chat within a short time, the chat is in a raid for a while and every newcomer
// is dealt with (muted until the raid is over, kicked or banned) (core/antiraid.py).
//
// Off by default. Settings per chat: how many joins in how many seconds make a raid, how long the raid lasts, and
// what happens to the newcomers. The people who joined in the window that started the raid are dealt with as well,
// they are the raid. Joins are counted in memory; the end of a running raid is kept in the database so a restart
// does not end it early.

const (
	RaidMinJoins, RaidMaxJoins       = 3, 100
	RaidMinWindow, RaidMaxWindow     = 10, 3600
	RaidMinDuration, RaidMaxDuration = 60, 7 * 86400
	raidSeenFor                      = 60 * time.Second // the same join can arrive twice (service message and update)
	raidRemovedFor                   = 30 * time.Second
	raidMemory                       = 5000
)

// RaidModes are what a raid can do to newcomers.
var RaidModes = []string{"mute", "kick", "ban"}

// RaidSettings are the antiraid settings of a chat.
type RaidSettings struct {
	Enabled  bool
	Joins    int
	Window   int
	Duration int
	Mode     string
	Until    float64 // unix time when the running raid ends, 0 when there is none
}

func defaultRaid() RaidSettings {
	return RaidSettings{Joins: 10, Window: 60, Duration: 3600, Mode: "mute"}
}

// Raging tells whether a raid is running.
func (s RaidSettings) Raging(now time.Time) bool {
	return s.Enabled && s.Until > float64(now.UnixNano())/1e9
}

type joinStamp struct {
	at   time.Time
	user int64
}

type raidState struct {
	mu      sync.Mutex
	cache   map[int64]RaidSettings
	recent  map[int64][]joinStamp
	seen    map[[2]int64]time.Time
	removed map[[2]int64]time.Time
}

func (a *App) forgetRaid(chatID int64) {
	a.raid.mu.Lock()
	delete(a.raid.cache, chatID)
	a.raid.mu.Unlock()
}

// RaidSettings reads the antiraid settings of a chat.
func (a *App) RaidSettings(ctx context.Context, chatID int64) (RaidSettings, error) {
	a.raid.mu.Lock()
	s, ok := a.raid.cache[chatID]
	a.raid.mu.Unlock()
	if ok {
		return s, nil
	}
	s = defaultRaid()
	if _, err := a.DB.Found(ctx, `SELECT enabled, joins, window_seconds, duration_seconds, mode,
COALESCE(EXTRACT(EPOCH FROM raid_until)::float8, 0) FROM antiraid WHERE chat_id = $1`,
		[]any{chatID}, &s.Enabled, &s.Joins, &s.Window, &s.Duration, &s.Mode, &s.Until); err != nil {
		return s, err
	}
	a.raid.mu.Lock()
	if a.raid.cache == nil || len(a.raid.cache) >= cacheLimit {
		a.raid.cache = map[int64]RaidSettings{}
	}
	a.raid.cache[chatID] = s
	a.raid.mu.Unlock()
	return s, nil
}

// UpdateRaid changes enabled, joins, window, duration or mode (the keys of values); the rest stays.
func (a *App) UpdateRaid(ctx context.Context, chatID int64, values map[string]any) error {
	cur, err := a.RaidSettings(ctx, chatID)
	if err != nil {
		return err
	}
	if v, ok := values["enabled"]; ok {
		cur.Enabled = v.(bool)
	}
	if v, ok := values["joins"]; ok {
		cur.Joins = v.(int)
	}
	if v, ok := values["window"]; ok {
		cur.Window = v.(int)
	}
	if v, ok := values["duration"]; ok {
		cur.Duration = v.(int)
	}
	if v, ok := values["mode"]; ok {
		cur.Mode = v.(string)
	}
	_, err = a.DB.Exec(ctx, `INSERT INTO antiraid (chat_id, enabled, joins, window_seconds, duration_seconds, mode)
VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (chat_id) DO UPDATE SET enabled = EXCLUDED.enabled,
joins = EXCLUDED.joins, window_seconds = EXCLUDED.window_seconds,
duration_seconds = EXCLUDED.duration_seconds, mode = EXCLUDED.mode`,
		chatID, cur.Enabled, cur.Joins, cur.Window, cur.Duration, cur.Mode)
	a.forgetRaid(chatID)
	if !cur.Enabled {
		a.raid.mu.Lock()
		delete(a.raid.recent, chatID)
		a.raid.mu.Unlock()
	}
	return err
}

// setRaidUntil starts (or, with 0, ends) a raid.
func (a *App) setRaidUntil(ctx context.Context, chatID int64, until float64) error {
	var t *time.Time
	if until != 0 {
		v := time.Unix(0, int64(until*1e9)).UTC()
		t = &v
	}
	_, err := a.DB.Exec(ctx, `INSERT INTO antiraid (chat_id, raid_until) VALUES ($1, $2)
ON CONFLICT (chat_id) DO UPDATE SET raid_until = EXCLUDED.raid_until`, chatID, t)
	a.forgetRaid(chatID)
	return err
}

// RaidWasRemoved tells whether the raid has just dealt with this newcomer (there is nobody left to greet or test).
func (a *App) RaidWasRemoved(chatID, userID int64) bool {
	a.raid.mu.Lock()
	defer a.raid.mu.Unlock()
	at, ok := a.raid.removed[[2]int64{chatID, userID}]
	return ok && time.Since(at) < raidRemovedFor
}

func (a *App) markRaidRemoved(chatID, userID int64) {
	a.raid.mu.Lock()
	defer a.raid.mu.Unlock()
	if a.raid.removed == nil || len(a.raid.removed) > raidMemory {
		a.raid.removed = map[[2]int64]time.Time{}
	}
	a.raid.removed[[2]int64{chatID, userID}] = time.Now()
}

func (a *App) firstSight(chatID, userID int64) bool {
	a.raid.mu.Lock()
	defer a.raid.mu.Unlock()
	if a.raid.seen == nil || len(a.raid.seen) > raidMemory {
		a.raid.seen = map[[2]int64]time.Time{}
	}
	k := [2]int64{chatID, userID}
	last, ok := a.raid.seen[k]
	a.raid.seen[k] = time.Now()
	return !ok || time.Since(last) >= raidSeenFor
}

func (c *Ctx) raidExempt(chatID int64, u *gotgbot.User) bool {
	return u.IsBot || u.Id == c.Bot.Id || IsImmune(u.Id) || c.App.GetRole(c.Ctx, u.Id) != NoRole ||
		c.isApproved(chatID, u.Id) || c.IsChatAdmin(chatID, u.Id) ||
		c.BanApplies(chatID, u.Id) // being banned: a mute now would undo the ban
}

func (c *Ctx) isApproved(chatID, userID int64) bool {
	ok, _ := c.App.IsApproved(c.Ctx, chatID, userID)
	return ok
}

// RaidResult is what a join did to a raid.
type RaidResult struct {
	Started  bool
	People   []int64 // the ones dealt with
	Settings RaidSettings
}

// RaidJoined: newcomers arrived. Counts them, starts a raid when there are too many, and deals with the newcomers of
// a raid. It returns nil when nothing happened. Errors of Telegram (no right to restrict) are returned as they are.
func (c *Ctx) RaidJoined(chatID int64, users []gotgbot.User) (*RaidResult, error) {
	a := c.App
	s, err := a.RaidSettings(c.Ctx, chatID)
	if err != nil || !s.Enabled {
		return nil, err
	}
	var people []gotgbot.User
	for _, u := range users {
		if a.firstSight(chatID, u.Id) && !c.raidExempt(chatID, &u) {
			people = append(people, u)
		}
	}
	if len(people) == 0 {
		return nil, nil
	}

	now := time.Now()
	started := false
	victims := make([]int64, 0, len(people))
	for _, u := range people {
		victims = append(victims, u.Id)
	}
	if !s.Raging(now) {
		a.raid.mu.Lock()
		if a.raid.recent == nil || len(a.raid.recent) > raidMemory {
			a.raid.recent = map[int64][]joinStamp{}
		}
		recent := a.raid.recent[chatID]
		for len(recent) > 0 && now.Sub(recent[0].at) > time.Duration(s.Window)*time.Second {
			recent = recent[1:]
		}
		for _, u := range people {
			recent = append(recent, joinStamp{now, u.Id})
		}
		if len(recent) < s.Joins {
			a.raid.recent[chatID] = recent
			a.raid.mu.Unlock()
			return nil, nil
		}
		started = true
		victims = victims[:0]
		seen := map[int64]bool{}
		for _, j := range recent {
			if !seen[j.user] {
				seen[j.user] = true
				victims = append(victims, j.user)
			}
		}
		delete(a.raid.recent, chatID)
		a.raid.mu.Unlock()
		if err := a.setRaidUntil(c.Ctx, chatID, float64(now.UnixNano())/1e9+float64(s.Duration)); err != nil {
			return nil, err
		}
		if s, err = a.RaidSettings(c.Ctx, chatID); err != nil {
			return nil, err
		}
	}

	var done []int64
	for _, id := range victims {
		if err := c.raidDealWith(chatID, id, s); err != nil {
			return nil, err
		}
		done = append(done, id)
	}
	return &RaidResult{Started: started, People: done, Settings: s}, nil
}

// raidDealWith is what the raid does to a newcomer.
func (c *Ctx) raidDealWith(chatID, userID int64, s RaidSettings) error {
	var err error
	if s.Mode == "mute" {
		remaining := int(s.Until - float64(time.Now().UnixNano())/1e9)
		if remaining < 60 {
			remaining = 60
		}
		_, err = c.Punish(chatID, userID, "tmute", remaining)
	} else {
		_, err = c.Punish(chatID, userID, s.Mode, 0)
	}
	if err != nil {
		return err
	}
	c.App.markRaidRemoved(chatID, userID)
	return nil
}

// EndRaid ends a running raid. It reports whether there was one.
func (a *App) EndRaid(ctx context.Context, chatID int64) (bool, error) {
	s, err := a.RaidSettings(ctx, chatID)
	if err != nil || !s.Raging(time.Now()) {
		return false, err
	}
	return true, a.setRaidUntil(ctx, chatID, 0)
}
