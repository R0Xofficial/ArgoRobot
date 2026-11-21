package core

import (
	"context"
	"crypto/rand"
	"fmt"
	"github.com/PaulSonOfLars/gotgbot/v2"
	"strings"
	"sync"
	"time"
)

// Federations: chats that share one ban list. A federation has an owner (one per person),
// admins who can ban in it, chats that joined it (a chat is in at most one) and a list of banned users. A federation
// can subscribe to others and then inherits their bans. What every message needs is kept in memory: which federation
// a chat is in, which ones it subscribes to and the ids of each federation's banned users.

const (
	MaxFedName   = 64
	MaxFedAdmins = 50
	MaxFedSubs   = 5
	MaxFedReason = 500
)

// FedError is something the caller asked for that can't be done. Tag and Vals make the message in the right language.
type FedError struct {
	Tag  string
	Vals V
}

func (e *FedError) Error() string { return e.Tag }

func fedErr(tag string, vals ...V) *FedError {
	e := &FedError{Tag: tag}
	if len(vals) > 0 {
		e.Vals = vals[0]
	}
	return e
}

type fedState struct {
	mu      sync.Mutex
	chatFed map[int64]string // "" = in none
	subs    map[string][]string
	ids     map[string]map[int64]bool
}

func (s *fedState) forgetAll() {
	s.mu.Lock()
	s.chatFed, s.subs, s.ids = nil, nil, nil
	s.mu.Unlock()
}

func (a *App) forgetFedChat(chatID int64) {
	a.feds.mu.Lock()
	delete(a.feds.chatFed, chatID)
	a.feds.mu.Unlock()
}

// Federation is one federation.
type Federation struct {
	ID            string
	Name          string
	OwnerID       int64
	OwnerName     string
	Notify        bool
	RequireReason bool
	LogChatID     int64 // 0 = none
	CreatedAt     time.Time
}

// FedBan is one banned user of a federation.
type FedBan struct {
	FedID    string
	FedName  string
	UserID   int64
	Name     string
	Reason   string
	ByID     int64
	ByName   string
	BannedAt time.Time
}

// FedAdmin is one admin of a federation.
type FedAdmin struct {
	UserID int64
	Name   string
}

const fedColumns = "fed_id, name, owner_id, owner_name, notify, require_reason, log_chat_id, created_at"

func scanFed(scan func(...any) error) (*Federation, error) {
	var f Federation
	var ownerName *string
	var logChat *int64
	if err := scan(&f.ID, &f.Name, &f.OwnerID, &ownerName, &f.Notify, &f.RequireReason, &logChat, &f.CreatedAt); err != nil {
		return nil, err
	}
	if ownerName != nil {
		f.OwnerName = *ownerName
	}
	if logChat != nil {
		f.LogChatID = *logChat
	}
	return &f, nil
}

func (a *App) oneFed(ctx context.Context, where string, arg any) (*Federation, error) {
	rows, err := a.DB.Pool.Query(ctx, "SELECT "+fedColumns+" FROM federations WHERE "+where, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanFed(rows.Scan)
}

// CleanFedName tidies a name, or says why it can't be one.
func CleanFedName(s string) (string, error) {
	name := strings.Join(strings.Fields(s), " ")
	if name == "" {
		return "", fedErr("federations.a_federation_needs_a_name")
	}
	if len([]rune(name)) > MaxFedName {
		return "", fedErr("federations.a_name_can_be_at_most", V{"MAX_NAME": MaxFedName})
	}
	return name, nil
}

func (a *App) GetFed(ctx context.Context, id string) (*Federation, error) {
	return a.oneFed(ctx, "fed_id = $1", id)
}

func (a *App) FedOwnedBy(ctx context.Context, userID int64) (*Federation, error) {
	return a.oneFed(ctx, "owner_id = $1", userID)
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func (a *App) CreateFed(ctx context.Context, name string, ownerID int64, ownerName string) (*Federation, error) {
	name, err := CleanFedName(name)
	if err != nil {
		return nil, err
	}
	if f, err := a.FedOwnedBy(ctx, ownerID); err != nil || f != nil {
		if err != nil {
			return nil, err
		}
		return nil, fedErr("federations.you_already_own_a_federation_everyone")
	}
	id := newUUID()
	if _, err := a.DB.Exec(ctx, "INSERT INTO federations (fed_id, name, owner_id, owner_name) VALUES ($1, $2, $3, $4)",
		id, name, ownerID, nullable(ownerName)); err != nil {
		return nil, err
	}
	return a.GetFed(ctx, id)
}

func (a *App) RenameFed(ctx context.Context, id, name string) (string, error) {
	name, err := CleanFedName(name)
	if err != nil {
		return "", err
	}
	_, err = a.DB.Exec(ctx, "UPDATE federations SET name = $2 WHERE fed_id = $1", id, name)
	return name, err
}

// SetFedFlag changes notify or require_reason.
func (a *App) SetFedFlag(ctx context.Context, id, field string, on bool) error {
	if field != "notify" && field != "require_reason" {
		return fmt.Errorf("unknown federation setting %q", field)
	}
	_, err := a.DB.Exec(ctx, "UPDATE federations SET "+field+" = $2 WHERE fed_id = $1", id, on)
	return err
}

// SetFedLogChat sets (or with 0 clears) the federation's log chat.
func (a *App) SetFedLogChat(ctx context.Context, id string, chatID int64) error {
	_, err := a.DB.Exec(ctx, "UPDATE federations SET log_chat_id = $2 WHERE fed_id = $1", id, nullID(chatID))
	return err
}

// DeleteFed removes a federation and everything about it. Bans already made in chats stay.
func (a *App) DeleteFed(ctx context.Context, id string) error {
	for _, sql := range []string{
		"DELETE FROM fed_admins WHERE fed_id = $1",
		"DELETE FROM fed_chats WHERE fed_id = $1",
		"DELETE FROM fed_bans WHERE fed_id = $1",
		"DELETE FROM fed_subs WHERE fed_id = $1 OR sub_id = $1",
		"DELETE FROM federations WHERE fed_id = $1",
	} {
		if _, err := a.DB.Exec(ctx, sql, id); err != nil {
			return err
		}
	}
	a.feds.forgetAll()
	return nil
}

func (a *App) TransferFed(ctx context.Context, id string, newOwner int64, name string) error {
	if f, err := a.FedOwnedBy(ctx, newOwner); err != nil {
		return err
	} else if f != nil {
		return fedErr("common.that_person_already_owns_a_federation")
	}
	if _, err := a.DB.Exec(ctx, "UPDATE federations SET owner_id = $2, owner_name = $3 WHERE fed_id = $1", id, newOwner, nullable(name)); err != nil {
		return err
	}
	_, err := a.DB.Exec(ctx, "DELETE FROM fed_admins WHERE fed_id = $1 AND user_id = $2", id, newOwner)
	return err
}

// --- Admins -----------------------------------------------------------------------------

func (a *App) FedAdmins(ctx context.Context, id string) ([]FedAdmin, error) {
	rows, err := a.DB.Pool.Query(ctx, "SELECT user_id, name FROM fed_admins WHERE fed_id = $1 ORDER BY name, user_id", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FedAdmin
	for rows.Next() {
		var ad FedAdmin
		var name *string
		if err := rows.Scan(&ad.UserID, &name); err != nil {
			return nil, err
		}
		if name != nil {
			ad.Name = *name
		}
		out = append(out, ad)
	}
	return out, rows.Err()
}

// IsFedAdmin: the user is the owner or an admin of the federation.
func (a *App) IsFedAdmin(ctx context.Context, f *Federation, userID int64) (bool, error) {
	if f.OwnerID == userID {
		return true, nil
	}
	return a.DB.Found(ctx, "SELECT 1 FROM fed_admins WHERE fed_id = $1 AND user_id = $2", []any{f.ID, userID}, new(int))
}

func (a *App) AddFedAdmin(ctx context.Context, id string, userID int64, name string) error {
	list, err := a.FedAdmins(ctx, id)
	if err != nil {
		return err
	}
	if len(list) >= MaxFedAdmins {
		return fedErr("federations.a_federation_can_have_at_most", V{"MAX_ADMINS": MaxFedAdmins})
	}
	_, err = a.DB.Exec(ctx, `INSERT INTO fed_admins (fed_id, user_id, name) VALUES ($1, $2, $3)
ON CONFLICT (fed_id, user_id) DO UPDATE SET name = EXCLUDED.name`, id, userID, nullable(name))
	return err
}

func (a *App) RemoveFedAdmin(ctx context.Context, id string, userID int64) (bool, error) {
	return a.DB.Found(ctx, "DELETE FROM fed_admins WHERE fed_id = $1 AND user_id = $2 RETURNING user_id", []any{id, userID}, new(int64))
}

// FedsOf lists the federations the user owns or administers, the owned one first.
func (a *App) FedsOf(ctx context.Context, userID int64) ([]*Federation, error) {
	rows, err := a.DB.Pool.Query(ctx, `SELECT `+fedColumns+` FROM federations f WHERE f.owner_id = $1
OR EXISTS (SELECT 1 FROM fed_admins a WHERE a.fed_id = f.fed_id AND a.user_id = $1)
ORDER BY (f.owner_id = $1) DESC, f.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Federation
	for rows.Next() {
		f, err := scanFed(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// --- Chats ------------------------------------------------------------------------------

// ChatFedID is the id of the federation of the chat, "" when it is in none.
func (a *App) ChatFedID(ctx context.Context, chatID int64) (string, error) {
	a.feds.mu.Lock()
	id, ok := a.feds.chatFed[chatID]
	a.feds.mu.Unlock()
	if ok {
		return id, nil
	}
	var fed string
	found, err := a.DB.Found(ctx, "SELECT fed_id FROM fed_chats WHERE chat_id = $1", []any{chatID}, &fed)
	if err != nil {
		return "", err
	}
	if !found {
		fed = ""
	}
	a.feds.mu.Lock()
	if a.feds.chatFed == nil || len(a.feds.chatFed) >= cacheLimit {
		a.feds.chatFed = map[int64]string{}
	}
	a.feds.chatFed[chatID] = fed
	a.feds.mu.Unlock()
	return fed, nil
}

func (a *App) ChatFed(ctx context.Context, chatID int64) (*Federation, error) {
	id, err := a.ChatFedID(ctx, chatID)
	if err != nil || id == "" {
		return nil, err
	}
	return a.GetFed(ctx, id)
}

func (a *App) JoinFed(ctx context.Context, chatID int64, id string) error {
	_, err := a.DB.Exec(ctx, `INSERT INTO fed_chats (chat_id, fed_id) VALUES ($1, $2)
ON CONFLICT (chat_id) DO UPDATE SET fed_id = EXCLUDED.fed_id`, chatID, id)
	a.forgetFedChat(chatID)
	return err
}

func (a *App) LeaveFed(ctx context.Context, chatID int64) error {
	_, err := a.DB.Exec(ctx, "DELETE FROM fed_chats WHERE chat_id = $1", chatID)
	a.forgetFedChat(chatID)
	return err
}

func (a *App) FedQuiet(ctx context.Context, chatID int64) (bool, error) {
	var quiet bool
	found, err := a.DB.Found(ctx, "SELECT quiet FROM fed_chats WHERE chat_id = $1", []any{chatID}, &quiet)
	return found && quiet, err
}

func (a *App) SetFedQuiet(ctx context.Context, chatID int64, quiet bool) error {
	_, err := a.DB.Exec(ctx, "UPDATE fed_chats SET quiet = $2 WHERE chat_id = $1", chatID, quiet)
	return err
}

// FedChats lists the chats of these federations.
func (a *App) FedChats(ctx context.Context, ids ...string) ([]int64, error) {
	var out []int64
	seen := map[int64]bool{}
	for _, id := range ids {
		rows, err := a.DB.Pool.Query(ctx, "SELECT chat_id FROM fed_chats WHERE fed_id = $1 ORDER BY chat_id", id)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var chat int64
			if err := rows.Scan(&chat); err != nil {
				rows.Close()
				return nil, err
			}
			if !seen[chat] {
				seen[chat] = true
				out = append(out, chat)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (a *App) FedChatCount(ctx context.Context, id string) (int, error) {
	var n int
	err := a.DB.Row(ctx, "SELECT count(*) FROM fed_chats WHERE fed_id = $1", []any{id}, &n)
	return n, err
}

// --- Subscriptions ----------------------------------------------------------------------

// FedSubs are the ids of the federations this one subscribes to.
func (a *App) FedSubs(ctx context.Context, id string) ([]string, error) {
	a.feds.mu.Lock()
	cached, ok := a.feds.subs[id]
	a.feds.mu.Unlock()
	if ok {
		return cached, nil
	}
	rows, err := a.DB.Pool.Query(ctx, "SELECT sub_id FROM fed_subs WHERE fed_id = $1 ORDER BY sub_id", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		list = append(list, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	a.feds.mu.Lock()
	if a.feds.subs == nil || len(a.feds.subs) >= cacheLimit {
		a.feds.subs = map[string][]string{}
	}
	a.feds.subs[id] = list
	a.feds.mu.Unlock()
	return list, nil
}

func (a *App) forgetFedSubs(id string) {
	a.feds.mu.Lock()
	delete(a.feds.subs, id)
	a.feds.mu.Unlock()
}

// FedSubscribers are the federations that subscribe to this one.
func (a *App) FedSubscribers(ctx context.Context, id string) ([]string, error) {
	rows, err := a.DB.Pool.Query(ctx, "SELECT fed_id FROM fed_subs WHERE sub_id = $1", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (a *App) SubscribeFed(ctx context.Context, id, sub string) error {
	if id == sub {
		return fedErr("federations.a_federation_can_t_subscribe_to")
	}
	current, err := a.FedSubs(ctx, id)
	if err != nil {
		return err
	}
	for _, s := range current {
		if s == sub {
			return fedErr("federations.you_are_already_subscribed_to_that")
		}
	}
	if len(current) >= MaxFedSubs {
		return fedErr("federations.a_federation_can_subscribe_to_at", V{"MAX_SUBS": MaxFedSubs})
	}
	_, err = a.DB.Exec(ctx, "INSERT INTO fed_subs (fed_id, sub_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", id, sub)
	a.forgetFedSubs(id)
	return err
}

func (a *App) UnsubscribeFed(ctx context.Context, id, sub string) (bool, error) {
	found, err := a.DB.Found(ctx, "DELETE FROM fed_subs WHERE fed_id = $1 AND sub_id = $2 RETURNING sub_id", []any{id, sub}, new(string))
	a.forgetFedSubs(id)
	return found, err
}

// --- Bans -------------------------------------------------------------------------------

func (a *App) fedBanIDs(ctx context.Context, id string) (map[int64]bool, error) {
	a.feds.mu.Lock()
	ids, ok := a.feds.ids[id]
	a.feds.mu.Unlock()
	if ok {
		return ids, nil
	}
	rows, err := a.DB.Pool.Query(ctx, "SELECT user_id FROM fed_bans WHERE fed_id = $1", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := map[int64]bool{}
	for rows.Next() {
		var u int64
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		set[u] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	a.feds.mu.Lock()
	defer a.feds.mu.Unlock()
	if a.feds.ids == nil || len(a.feds.ids) >= 200 {
		a.feds.ids = map[string]map[int64]bool{}
	}
	if existing, ok := a.feds.ids[id]; ok {
		return existing, nil
	}
	a.feds.ids[id] = set
	return set, nil
}

func (a *App) fedHas(ctx context.Context, id string, userID int64) (bool, error) {
	ids, err := a.fedBanIDs(ctx, id)
	if err != nil {
		return false, err
	}
	a.feds.mu.Lock()
	defer a.feds.mu.Unlock()
	return ids[userID], nil
}

func (a *App) fedMark(ctx context.Context, id string, userID int64, banned bool) {
	ids, err := a.fedBanIDs(ctx, id)
	if err != nil {
		return
	}
	a.feds.mu.Lock()
	defer a.feds.mu.Unlock()
	if banned {
		ids[userID] = true
	} else {
		delete(ids, userID)
	}
}

const fedBanColumns = "fed_id, user_id, name, reason, banned_by, banned_by_name, created_at"

func scanFedBan(scan func(...any) error) (*FedBan, error) {
	var b FedBan
	var name, reason, byName *string
	var by *int64
	if err := scan(&b.FedID, &b.UserID, &name, &reason, &by, &byName, &b.BannedAt); err != nil {
		return nil, err
	}
	if name != nil {
		b.Name = *name
	}
	if reason != nil {
		b.Reason = *reason
	}
	if byName != nil {
		b.ByName = *byName
	}
	if by != nil {
		b.ByID = *by
	}
	return &b, nil
}

func (a *App) GetFedBan(ctx context.Context, id string, userID int64) (*FedBan, error) {
	has, err := a.fedHas(ctx, id, userID)
	if err != nil || !has {
		return nil, err
	}
	rows, err := a.DB.Pool.Query(ctx, "SELECT "+fedBanColumns+" FROM fed_bans WHERE fed_id = $1 AND user_id = $2", id, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanFedBan(rows.Scan)
}

func clipReason(s string) any {
	r := []rune(s)
	if len(r) > MaxFedReason {
		r = r[:MaxFedReason]
	}
	return nullable(string(r))
}

// BanInFed bans a user in a federation. It returns the previous ban when there was one (its reason is replaced).
func (a *App) BanInFed(ctx context.Context, id string, userID int64, name, reason string, byID int64, byName string) (*FedBan, error) {
	old, err := a.GetFedBan(ctx, id, userID)
	if err != nil {
		return nil, err
	}
	if _, err := a.DB.Exec(ctx, `INSERT INTO fed_bans (fed_id, user_id, name, reason, banned_by, banned_by_name, created_at)
VALUES ($1, $2, $3, $4, $5, $6, now())
ON CONFLICT (fed_id, user_id) DO UPDATE SET name = EXCLUDED.name, reason = EXCLUDED.reason,
    banned_by = EXCLUDED.banned_by, banned_by_name = EXCLUDED.banned_by_name`,
		id, userID, nullable(name), clipReason(reason), byID, nullable(byName)); err != nil {
		return nil, err
	}
	a.fedMark(ctx, id, userID, true)
	return old, nil
}

func (a *App) UnbanInFed(ctx context.Context, id string, userID int64) (*FedBan, error) {
	old, err := a.GetFedBan(ctx, id, userID)
	if err != nil || old == nil {
		return nil, err
	}
	if _, err := a.DB.Exec(ctx, "DELETE FROM fed_bans WHERE fed_id = $1 AND user_id = $2", id, userID); err != nil {
		return nil, err
	}
	a.fedMark(ctx, id, userID, false)
	return old, nil
}

func (a *App) FedBans(ctx context.Context, id string) ([]*FedBan, error) {
	rows, err := a.DB.Pool.Query(ctx, "SELECT "+fedBanColumns+" FROM fed_bans WHERE fed_id = $1 ORDER BY created_at, user_id", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*FedBan
	for rows.Next() {
		b, err := scanFedBan(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (a *App) FedBanCount(ctx context.Context, id string) (int, error) {
	ids, err := a.fedBanIDs(ctx, id)
	if err != nil {
		return 0, err
	}
	a.feds.mu.Lock()
	defer a.feds.mu.Unlock()
	return len(ids), nil
}

// ImportFedBans adds rows. With overwrite the old list goes first. It returns how many were added.
func (a *App) ImportFedBans(ctx context.Context, id string, rows []GbanRow, overwrite bool, byID int64, byName string) (int, error) {
	if overwrite {
		if _, err := a.DB.Exec(ctx, "DELETE FROM fed_bans WHERE fed_id = $1", id); err != nil {
			return 0, err
		}
		a.feds.mu.Lock()
		delete(a.feds.ids, id)
		a.feds.mu.Unlock()
	}
	added := 0
	for _, r := range rows {
		known, err := a.fedHas(ctx, id, r.UserID)
		if err != nil {
			return added, err
		}
		if known {
			continue
		}
		if _, err := a.DB.Exec(ctx, `INSERT INTO fed_bans (fed_id, user_id, name, reason, banned_by, banned_by_name, created_at)
VALUES ($1, $2, $3, $4, $5, $6, now()) ON CONFLICT (fed_id, user_id) DO NOTHING`,
			id, r.UserID, nullable(r.Name), clipReason(r.Reason), byID, nullable(byName)); err != nil {
			return added, err
		}
		a.fedMark(ctx, id, r.UserID, true)
		added++
	}
	return added, nil
}

// FedsBanning lists the federations (with the ban) the user is directly banned in.
func (a *App) FedsBanning(ctx context.Context, userID int64) ([]*FedBan, error) {
	rows, err := a.DB.Pool.Query(ctx, `SELECT f.fed_id, b.user_id, b.name, b.reason, b.banned_by, b.banned_by_name, b.created_at, f.name
FROM fed_bans b JOIN federations f ON f.fed_id = b.fed_id WHERE b.user_id = $1 ORDER BY f.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*FedBan
	for rows.Next() {
		var b FedBan
		var name, reason, byName *string
		var by *int64
		if err := rows.Scan(&b.FedID, &b.UserID, &name, &reason, &by, &byName, &b.BannedAt, &b.FedName); err != nil {
			return nil, err
		}
		if name != nil {
			b.Name = *name
		}
		if reason != nil {
			b.Reason = *reason
		}
		if byName != nil {
			b.ByName = *byName
		}
		if by != nil {
			b.ByID = *by
		}
		out = append(out, &b)
	}
	return out, rows.Err()
}

// EffectiveFedBan is the federation and ban that apply to the user in this chat: in its federation or one it
// subscribes to. Nil when none does.
func (a *App) EffectiveFedBan(ctx context.Context, chatID, userID int64) (*Federation, *FedBan, error) {
	id, err := a.ChatFedID(ctx, chatID)
	if err != nil || id == "" {
		return nil, nil, err
	}
	subs, err := a.FedSubs(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	for _, source := range append([]string{id}, subs...) {
		has, err := a.fedHas(ctx, source, userID)
		if err != nil {
			return nil, nil, err
		}
		if !has {
			continue
		}
		ban, err := a.GetFedBan(ctx, source, userID)
		if err != nil {
			return nil, nil, err
		}
		fed, err := a.GetFed(ctx, source)
		if err != nil {
			return nil, nil, err
		}
		if ban != nil && fed != nil {
			return fed, ban, nil
		}
	}
	return nil, nil, nil
}

// FedReach is the federation itself and every federation that subscribes to it: where its bans apply.
func (a *App) FedReach(ctx context.Context, id string) ([]string, error) {
	subs, err := a.FedSubscribers(ctx, id)
	return append([]string{id}, subs...), err
}

// --- Where the bot banned -------------------------------------------------------------------

func (a *App) RecordFedApplied(ctx context.Context, chatID, userID int64) error {
	_, err := a.DB.Exec(ctx, "INSERT INTO fed_applied (chat_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", chatID, userID)
	return err
}

func (a *App) FedAppliedIn(ctx context.Context, userID int64) ([]int64, error) {
	rows, err := a.DB.Pool.Query(ctx, "SELECT chat_id FROM fed_applied WHERE user_id = $1", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var c int64
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (a *App) ForgetFedApplied(ctx context.Context, chatID, userID int64) error {
	_, err := a.DB.Exec(ctx, "DELETE FROM fed_applied WHERE chat_id = $1 AND user_id = $2", chatID, userID)
	return err
}

// FedCount is how many federations exist.
func (a *App) FedCount(ctx context.Context) (int, error) {
	var n int
	err := a.DB.Row(ctx, "SELECT count(*) FROM federations", nil, &n)
	return n, err
}

func init() {
	ApproveHooks = append(ApproveHooks, func(c *Ctx, chatID, userID int64) {
		applied, _ := c.App.FedAppliedIn(c.Ctx, userID)
		for _, chat := range applied {
			if chat == chatID {
				_, _ = c.Bot.UnbanChatMemberWithContext(c.Ctx, chatID, userID, &gotgbot.UnbanChatMemberOpts{OnlyIfBanned: true})
				_ = c.App.ForgetFedApplied(c.Ctx, chatID, userID)
			}
		}
	})
}
