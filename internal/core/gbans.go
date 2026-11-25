package core

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/jackc/pgx/v5"
)

// Storage of global bans. A global ban says "this user is not welcome in any chat that uses the bot".
// Three tables back it: gbans (who and why), gban_requests (what Support users asked for and Sudo users have not
// decided yet) and gban_bans (the chats where the bot itself removed someone, so that lifting the ban undoes only
// that). The ids of the banned are kept in memory: every message is checked against them.

// RequestLifetime: an unanswered request stops blocking new ones after this long.
const RequestLifetime = 7 * 24 * time.Hour

type gbanState struct {
	mu  sync.RWMutex
	ids map[int64]bool // nil until loaded
}

// Gban is one global ban.
type Gban struct {
	UserID         int64
	Name           string
	Reason         string
	AdminID        int64 // 0 for imported bans
	AdminName      string
	ApprovedBy     int64
	ApprovedByName string
	CreatedAt      time.Time
}

// GbanRequest is what a Support user asked for and a Sudo user has yet to decide.
type GbanRequest struct {
	ID              int64
	TargetID        int64
	TargetName      string
	Kind            string // gban, dgban or ungban
	Reason          string
	RequestedBy     int64
	RequestedByName string
	ChatID          int64
	MsgID           int64
	ThreadID        int64
	TargetMsgID     int64
	OriginHTML      string
	Deliveries      []GbanDelivery // the copies sent to the Sudo users' private chats
}

// GbanDelivery is one copy of a request: the private chat it went to and its message.
type GbanDelivery struct {
	Chat int64 `json:"c"`
	Msg  int64 `json:"m"`
}

// SaveGbanDeliveries remembers where the copies of a request went, so a decision can update all of them.
func (a *App) SaveGbanDeliveries(ctx context.Context, id int64, d []GbanDelivery) error {
	raw, _ := json.Marshal(d)
	_, err := a.DB.Exec(ctx, "UPDATE gban_requests SET deliveries = $2 WHERE id = $1", id, string(raw))
	return err
}

// GbanRequestsWanted tells whether the requests come to this person's private chat (they do unless turned off).
func (a *App) GbanRequestsWanted(ctx context.Context, userID int64) bool {
	found, _ := a.DB.Found(ctx, "SELECT 1 FROM gban_request_optout WHERE user_id = $1", []any{userID}, new(int))
	return !found
}

// SetGbanRequestsWanted turns the requests in somebody's private chat on or off.
func (a *App) SetGbanRequestsWanted(ctx context.Context, userID int64, wanted bool) error {
	var err error
	if wanted {
		_, err = a.DB.Exec(ctx, "DELETE FROM gban_request_optout WHERE user_id = $1", userID)
	} else {
		_, err = a.DB.Exec(ctx, "INSERT INTO gban_request_optout (user_id) VALUES ($1) ON CONFLICT DO NOTHING", userID)
	}
	return err
}

// GbanApprovers are the people who may decide a request and want it: the owner, the developers and the Sudo users.
func (a *App) GbanApprovers(ctx context.Context) []int64 {
	ids := []int64{a.Cfg.OwnerID}
	seen := map[int64]bool{a.Cfg.OwnerID: true}
	for _, role := range []Role{Developer, Sudo} {
		members, _ := a.ListRole(ctx, role)
		for _, m := range members {
			if !seen[m.UserID] {
				seen[m.UserID] = true
				ids = append(ids, m.UserID)
			}
		}
	}
	var wanted []int64
	for _, id := range ids {
		if id != 0 && a.GbanRequestsWanted(ctx, id) {
			wanted = append(wanted, id)
		}
	}
	return wanted
}

func (a *App) gbanIDs(ctx context.Context) (map[int64]bool, error) {
	a.gbans.mu.RLock()
	ids := a.gbans.ids
	a.gbans.mu.RUnlock()
	if ids != nil {
		return ids, nil
	}
	rows, err := a.DB.Pool.Query(ctx, "SELECT user_id FROM gbans")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	loaded := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		loaded[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	a.gbans.mu.Lock()
	defer a.gbans.mu.Unlock()
	if a.gbans.ids == nil {
		a.gbans.ids = loaded
		slog.Info("Loaded global bans", "count", len(loaded))
	}
	return a.gbans.ids, nil
}

func (a *App) gbanSet(ctx context.Context, id int64, banned bool) {
	if _, err := a.gbanIDs(ctx); err != nil {
		return
	}
	a.gbans.mu.Lock()
	defer a.gbans.mu.Unlock()
	if banned {
		a.gbans.ids[id] = true
	} else {
		delete(a.gbans.ids, id)
	}
}

// IsGbanned tells whether the user is globally banned.
func (a *App) IsGbanned(ctx context.Context, userID int64) (bool, error) {
	ids, err := a.gbanIDs(ctx)
	if err != nil {
		return false, err
	}
	a.gbans.mu.RLock()
	defer a.gbans.mu.RUnlock()
	return ids[userID], nil
}

// GbanCount is how many users are globally banned.
func (a *App) GbanCount(ctx context.Context) (int, error) {
	ids, err := a.gbanIDs(ctx)
	if err != nil {
		return 0, err
	}
	a.gbans.mu.RLock()
	defer a.gbans.mu.RUnlock()
	return len(ids), nil
}

const gbanColumns = "user_id, name, reason, admin_id, admin_name, approved_by, approved_by_name, created_at"

func scanGban(scan func(...any) error) (*Gban, error) {
	var g Gban
	var name, adminName, approvedName *string
	var admin, approved *int64
	if err := scan(&g.UserID, &name, &g.Reason, &admin, &adminName, &approved, &approvedName, &g.CreatedAt); err != nil {
		return nil, err
	}
	if name != nil {
		g.Name = *name
	}
	if adminName != nil {
		g.AdminName = *adminName
	}
	if approvedName != nil {
		g.ApprovedByName = *approvedName
	}
	if admin != nil {
		g.AdminID = *admin
	}
	if approved != nil {
		g.ApprovedBy = *approved
	}
	return &g, nil
}

// GetGban is nil when the user is not banned.
func (a *App) GetGban(ctx context.Context, userID int64) (*Gban, error) {
	banned, err := a.IsGbanned(ctx, userID)
	if err != nil || !banned {
		return nil, err
	}
	row := a.DB.Pool.QueryRow(ctx, "SELECT "+gbanColumns+" FROM gbans WHERE user_id = $1", userID)
	g, err := scanGban(row.Scan)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return g, nil
}

// AddGban bans a user globally, or changes the reason of an existing ban (the time and the admin too). It returns the
// previous record, if there was one.
func (a *App) AddGban(ctx context.Context, userID int64, name, reason string, adminID int64, adminName string, approvedBy int64, approvedByName string) (*Gban, error) {
	old, err := a.GetGban(ctx, userID)
	if err != nil {
		return nil, err
	}
	if _, err := a.DB.Exec(ctx, `INSERT INTO gbans (user_id, name, reason, admin_id, admin_name, approved_by, approved_by_name)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (user_id) DO UPDATE SET name = EXCLUDED.name, reason = EXCLUDED.reason, admin_id = EXCLUDED.admin_id,
    admin_name = EXCLUDED.admin_name, approved_by = EXCLUDED.approved_by, approved_by_name = EXCLUDED.approved_by_name,
    created_at = now()`, userID, nullable(name), reason, nullID(adminID), nullable(adminName), nullID(approvedBy), nullable(approvedByName)); err != nil {
		return nil, err
	}
	a.gbanSet(ctx, userID, true)
	return old, nil
}

// RemoveGban lifts a ban. False when there was none.
func (a *App) RemoveGban(ctx context.Context, userID int64) (bool, error) {
	found, err := a.DB.Found(ctx, "DELETE FROM gbans WHERE user_id = $1 RETURNING user_id", []any{userID}, new(int64))
	if err != nil {
		return false, err
	}
	a.gbanSet(ctx, userID, false)
	return found, nil
}

// GbanRow is a (user, reason, name) triple to import.
type GbanRow struct {
	UserID int64
	Reason string
	Name   string
}

// ImportGbans adds bans that do not exist yet. Imported bans have no admin. It returns how many were added.
func (a *App) ImportGbans(ctx context.Context, rows []GbanRow) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	ids := make([]int64, len(rows))
	reasons := make([]string, len(rows))
	names := make([]*string, len(rows))
	for i, r := range rows {
		ids[i], reasons[i] = r.UserID, r.Reason
		if r.Name != "" {
			n := r.Name
			names[i] = &n
		}
	}
	added, err := a.DB.Pool.Query(ctx, `INSERT INTO gbans (user_id, reason, name)
SELECT * FROM unnest($1::bigint[], $2::text[], $3::text[]) ON CONFLICT (user_id) DO NOTHING RETURNING user_id`, ids, reasons, names)
	if err != nil {
		return 0, err
	}
	defer added.Close()
	var got []int64
	for added.Next() {
		var id int64
		if err := added.Scan(&id); err != nil {
			return 0, err
		}
		got = append(got, id)
	}
	if err := added.Err(); err != nil {
		return 0, err
	}
	if _, err := a.gbanIDs(ctx); err == nil {
		a.gbans.mu.Lock()
		for _, id := range got {
			a.gbans.ids[id] = true
		}
		a.gbans.mu.Unlock()
	}
	return len(got), nil
}

// AllGbans is every ban, oldest first, as rows for ban files.
func (a *App) AllGbans(ctx context.Context) ([]BanFileRow, error) {
	rows, err := a.DB.Pool.Query(ctx, "SELECT user_id, name, reason, admin_name, created_at FROM gbans ORDER BY created_at, user_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BanFileRow
	for rows.Next() {
		var r BanFileRow
		var name, by *string
		if err := rows.Scan(&r.UserID, &name, &r.Reason, &by, &r.CreatedAt); err != nil {
			return nil, err
		}
		if name != nil {
			r.Name = *name
		}
		if by != nil {
			r.BannedBy = *by
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// --- Requests of Support users ----------------------------------------------------------

func (a *App) expireGbanRequests(ctx context.Context, targetID int64) error {
	_, err := a.DB.Exec(ctx, "DELETE FROM gban_requests WHERE target_id = $1 AND created_at < now() - $2::interval",
		targetID, RequestLifetime)
	return err
}

// HasPendingGban tells whether somebody already asked for something about this user.
func (a *App) HasPendingGban(ctx context.Context, targetID int64) (bool, error) {
	if err := a.expireGbanRequests(ctx, targetID); err != nil {
		return false, err
	}
	return a.DB.Found(ctx, "SELECT 1 FROM gban_requests WHERE target_id = $1", []any{targetID}, new(int))
}

// CreateGbanRequest saves a request and returns its id; 0 when the user already has a pending one.
func (a *App) CreateGbanRequest(ctx context.Context, r GbanRequest) (int64, error) {
	if err := a.expireGbanRequests(ctx, r.TargetID); err != nil {
		return 0, err
	}
	var id int64
	found, err := a.DB.Found(ctx, `INSERT INTO gban_requests (target_id, target_name, kind, reason, requested_by, requested_by_name,
chat_id, msg_id, thread_id, target_msg_id, origin_html) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (target_id) DO NOTHING RETURNING id`, []any{r.TargetID, nullable(r.TargetName), r.Kind, r.Reason, r.RequestedBy,
		nullable(r.RequestedByName), r.ChatID, nullID(r.MsgID), nullID(r.ThreadID), nullID(r.TargetMsgID), r.OriginHTML}, &id)
	if err != nil || !found {
		return 0, err
	}
	return id, nil
}

// ClaimGbanRequest takes a request out of the queue. Whoever gets it decides it; a later press gets nil.
func (a *App) ClaimGbanRequest(ctx context.Context, id int64) (*GbanRequest, error) {
	var r GbanRequest
	var name, byName *string
	var deliveries string
	var msg, thread, tmsg *int64
	found, err := a.DB.Found(ctx, `DELETE FROM gban_requests WHERE id = $1
RETURNING id, target_id, target_name, kind, reason, requested_by, requested_by_name, chat_id, msg_id, thread_id, target_msg_id, origin_html, deliveries`,
		[]any{id}, &r.ID, &r.TargetID, &name, &r.Kind, &r.Reason, &r.RequestedBy, &byName, &r.ChatID, &msg, &thread, &tmsg, &r.OriginHTML, &deliveries)
	if err != nil || !found {
		return nil, err
	}
	_ = json.Unmarshal([]byte(deliveries), &r.Deliveries)
	if name != nil {
		r.TargetName = *name
	}
	if byName != nil {
		r.RequestedByName = *byName
	}
	if msg != nil {
		r.MsgID = *msg
	}
	if thread != nil {
		r.ThreadID = *thread
	}
	if tmsg != nil {
		r.TargetMsgID = *tmsg
	}
	return &r, nil
}

func (a *App) DropGbanRequest(ctx context.Context, id int64) error {
	_, err := a.DB.Exec(ctx, "DELETE FROM gban_requests WHERE id = $1", id)
	return err
}

// --- Where the bot removed someone ----------------------------------------------------------

func (a *App) RecordGbanBan(ctx context.Context, userID, chatID int64) error {
	_, err := a.DB.Exec(ctx, "INSERT INTO gban_bans (user_id, chat_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", userID, chatID)
	return err
}

func (a *App) GbanBannedChats(ctx context.Context, userID int64) ([]int64, error) {
	rows, err := a.DB.Pool.Query(ctx, "SELECT chat_id FROM gban_bans WHERE user_id = $1", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (a *App) ForgetGbanBans(ctx context.Context, userID int64) error {
	_, err := a.DB.Exec(ctx, "DELETE FROM gban_bans WHERE user_id = $1", userID)
	return err
}

func (a *App) ForgetGbanBan(ctx context.Context, userID, chatID int64) error {
	_, err := a.DB.Exec(ctx, "DELETE FROM gban_bans WHERE user_id = $1 AND chat_id = $2", userID, chatID)
	return err
}

// ChatsOfUser lists the group chats the bot has seen the user in (chats_of_user).
func (a *App) ChatsOfUser(ctx context.Context, userID int64) ([]ChatRef, error) {
	rows, err := a.DB.Pool.Query(ctx, `SELECT c.chat_id, c.type, c.bot_admin FROM user_chats u JOIN chats c USING (chat_id)
WHERE u.user_id = $1 ORDER BY c.chat_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChatRef
	for rows.Next() {
		var r ChatRef
		if err := rows.Scan(&r.ID, &r.Type, &r.BotAdmin); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ChatRef is a chat the bot knows of. BotAdmin is nil when it has not been checked.
type ChatRef struct {
	ID       int64
	Type     string
	BotAdmin *bool
}

func init() {
	// Approving somebody lifts the ban the global ban put on them in this chat, nothing else.
	ApproveHooks = append(ApproveHooks, func(c *Ctx, chatID, userID int64) {
		if banned, _ := c.App.IsGbanned(c.Ctx, userID); banned {
			_, _ = c.Bot.UnbanChatMemberWithContext(c.Ctx, chatID, userID, &gotgbot.UnbanChatMemberOpts{OnlyIfBanned: true})
			_ = c.App.ForgetGbanBan(c.Ctx, userID, chatID)
		}
	})
}

// InstallBanLookups connects the lookups that the rest of the bot uses (whitelist checks, /stats, role changes) to
// the stores of global bans and federations.
func (a *App) InstallBanLookups() {
	BanLookups.Gbanned = a.IsGbanned
	BanLookups.GbanCount = a.GbanCount
	BanLookups.FedCount = a.FedCount
	BanLookups.FedBans = func(ctx context.Context, userID int64) ([]string, error) {
		list, err := a.FedsBanning(ctx, userID)
		names := make([]string, len(list))
		for i, b := range list {
			names[i] = b.FedName
		}
		return names, err
	}
	BanLookups.ChatFedBan = func(ctx context.Context, chatID, userID int64) (bool, error) {
		_, ban, err := a.EffectiveFedBan(ctx, chatID, userID)
		return ban != nil, err
	}
}
