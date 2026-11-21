package core

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

// Staff roles: granting, removing, listing (core/roles.py) and the whitelist (core/whitelist.py).

// ManagedBy is the minimum role needed to grant or revoke each role.
var ManagedBy = map[Role]Role{Developer: Owner, Sudo: Developer, Support: Developer}

// RoleLogTags are the role log tags: granted, revoked.
var RoleLogTags = map[Role][2]string{
	Developer: {"#DEVELOPER", "#UNDEVELOPER"},
	Sudo:      {"#SUDO", "#UNSUDO"},
	Support:   {"#SUPPORT", "#UNSUPPORT"},
}

// RoleLabel is the human readable name: "Sudo", "Developers".
func RoleLabel(r Role, plural bool) string {
	s := r.String()
	if plural {
		s += "s"
	}
	return s
}

// CanManage tells whether actor may grant or revoke target.
func CanManage(actor, target Role) bool {
	needed, ok := ManagedBy[target]
	return ok && actor != NoRole && actor >= needed
}

// SetRole stores a staff role.
func (a *App) SetRole(ctx context.Context, userID int64, role Role, addedBy int64, name, addedByName string) error {
	if role == Owner {
		return errors.New("the Owner role is set in the configuration, it can't be granted")
	}
	if userID == a.Cfg.OwnerID {
		return errors.New("the Owner's role can't be changed")
	}
	_, err := a.DB.Exec(ctx, `INSERT INTO roles (user_id, role, added_by, name, added_by_name)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (user_id) DO UPDATE SET role = EXCLUDED.role, added_by = EXCLUDED.added_by, name = EXCLUDED.name,
added_by_name = EXCLUDED.added_by_name, added_at = now()`, userID, int16(role), nullID(addedBy), nullable(name), nullable(addedByName))
	if err != nil {
		return err
	}
	return a.loadRoles(ctx)
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// RemoveRole takes a staff role away.
func (a *App) RemoveRole(ctx context.Context, userID int64) error {
	if userID == a.Cfg.OwnerID {
		return errors.New("the Owner can't be demoted")
	}
	if _, err := a.DB.Exec(ctx, "DELETE FROM roles WHERE user_id = $1", userID); err != nil {
		return err
	}
	return a.loadRoles(ctx)
}

// StaffMember is one holder of a staff role.
type StaffMember struct {
	UserID      int64
	Name        string
	AddedAt     *time.Time
	AddedBy     int64
	AddedByName string
}

// ListRole is the staff members holding the role, oldest first.
func (a *App) ListRole(ctx context.Context, role Role) ([]StaffMember, error) {
	rows, err := a.DB.Pool.Query(ctx, `SELECT user_id, name, added_at, added_by, added_by_name
FROM roles WHERE role = $1 ORDER BY added_at, user_id`, int16(role))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StaffMember
	for rows.Next() {
		var m StaffMember
		var name, byName *string
		var at time.Time
		var by *int64
		if err := rows.Scan(&m.UserID, &name, &at, &by, &byName); err != nil {
			return nil, err
		}
		m.AddedAt = &at
		if name != nil {
			m.Name = *name
		}
		if by != nil {
			m.AddedBy = *by
		}
		if byName != nil {
			m.AddedByName = *byName
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DisplayName is the current name of a user: from Telegram, else the phone book, else stored.
func (c *Ctx) DisplayName(userID int64, stored string) string {
	if chat, err := c.Bot.GetChatWithContext(c.Ctx, userID, nil); err == nil {
		name := strings.TrimSpace(chat.FirstName + " " + chat.LastName)
		if name != "" {
			return name
		}
	}
	if row, err := c.App.UserByID(c.Ctx, userID); err == nil && row != nil && row.Name != "" {
		return row.Name
	}
	return stored
}

// --- Whitelist ---------------------------------------------------------------

// WhitelistEntry is one protected user.
type WhitelistEntry struct {
	UserID      int64
	Name        string
	AddedBy     int64
	AddedByName string
}

// The list is small and read on every global action, so it is kept in memory and reloaded after each change.
type whitelistCache struct {
	mu      sync.RWMutex
	entries map[int64]WhitelistEntry // nil until loaded
}

func (a *App) loadWhitelist(ctx context.Context) error {
	rows, err := a.DB.Pool.Query(ctx, "SELECT user_id, name, added_by, added_by_name FROM whitelist ORDER BY added_at, user_id")
	if err != nil {
		return err
	}
	defer rows.Close()
	entries := map[int64]WhitelistEntry{}
	for rows.Next() {
		var e WhitelistEntry
		var name, byName *string
		var by *int64
		if err := rows.Scan(&e.UserID, &name, &by, &byName); err != nil {
			return err
		}
		if name != nil {
			e.Name = *name
		}
		if by != nil {
			e.AddedBy = *by
		}
		if byName != nil {
			e.AddedByName = *byName
		}
		entries[e.UserID] = e
	}
	if err := rows.Err(); err != nil {
		return err
	}
	a.whitelist.mu.Lock()
	a.whitelist.entries = entries
	a.whitelist.mu.Unlock()
	return nil
}

func (a *App) whitelisted(ctx context.Context) (map[int64]WhitelistEntry, error) {
	a.whitelist.mu.RLock()
	e := a.whitelist.entries
	a.whitelist.mu.RUnlock()
	if e != nil {
		return e, nil
	}
	if err := a.loadWhitelist(ctx); err != nil {
		return nil, err
	}
	a.whitelist.mu.RLock()
	defer a.whitelist.mu.RUnlock()
	return a.whitelist.entries, nil
}

// IsWhitelisted tells whether the user is on the whitelist.
func (a *App) IsWhitelisted(ctx context.Context, userID int64) (bool, error) {
	e, err := a.whitelisted(ctx)
	_, ok := e[userID]
	return ok, err
}

// IsProtected: immune or whitelisted, so no global ban may touch the user.
func (a *App) IsProtected(ctx context.Context, userID int64) (bool, error) {
	if IsImmune(userID) {
		return true, nil
	}
	return a.IsWhitelisted(ctx, userID)
}

// WhitelistAdd whitelists a user. False if they already were.
func (a *App) WhitelistAdd(ctx context.Context, userID, addedBy int64, name, addedByName string) (bool, error) {
	if ok, err := a.IsWhitelisted(ctx, userID); err != nil || ok {
		return false, err
	}
	if _, err := a.DB.Exec(ctx, `INSERT INTO whitelist (user_id, added_by, name, added_by_name) VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id) DO NOTHING`, userID, nullID(addedBy), nullable(name), nullable(addedByName)); err != nil {
		return false, err
	}
	return true, a.loadWhitelist(ctx)
}

// WhitelistRemove takes a user off the whitelist. False if they were not on it.
func (a *App) WhitelistRemove(ctx context.Context, userID int64) (bool, error) {
	if ok, err := a.IsWhitelisted(ctx, userID); err != nil || !ok {
		return false, err
	}
	if _, err := a.DB.Exec(ctx, "DELETE FROM whitelist WHERE user_id = $1", userID); err != nil {
		return false, err
	}
	return true, a.loadWhitelist(ctx)
}

// WhitelistAll lists the whitelist.
func (a *App) WhitelistAll(ctx context.Context) ([]WhitelistEntry, error) {
	e, err := a.whitelisted(ctx)
	out := make([]WhitelistEntry, 0, len(e))
	for _, v := range e {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	return out, err
}

// --- Hooks for the modules that arrive later (global bans, federations) ---------------

// BanLookups are filled in by the modules that keep global bans and federation bans. Until they exist nobody is
// banned anywhere.
var BanLookups struct {
	Gbanned   func(ctx context.Context, userID int64) (bool, error)
	FedBans   func(ctx context.Context, userID int64) ([]string, error) // names of the federations that ban the user
	GbanCount func(ctx context.Context) (int, error)
	FedCount  func(ctx context.Context) (int, error)
	// ChatFedBan tells whether the user is banned in the federation of the chat (or one it subscribes to).
	ChatFedBan func(ctx context.Context, chatID, userID int64) (bool, error)
}
