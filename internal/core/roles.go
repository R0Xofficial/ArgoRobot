package core

import (
	"context"
	"log/slog"
	"sync"
)

// Staff roles live in the database and are mirrored in memory, so looking up a role on every update costs no
// query. The Owner is only ever OWNER_ID from the configuration: it is never stored, so no command can change it.
type rolesCache struct {
	mu    sync.RWMutex
	staff map[int64]Role // nil until loaded
}

func (a *App) loadRoles(ctx context.Context) error {
	rows, err := a.DB.Pool.Query(ctx, "SELECT user_id, role FROM roles")
	if err != nil {
		return err
	}
	defer rows.Close()
	staff := map[int64]Role{}
	for rows.Next() {
		var id int64
		var role int16
		if err := rows.Scan(&id, &role); err != nil {
			return err
		}
		staff[id] = Role(role)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	a.roles.mu.Lock()
	a.roles.staff = staff
	a.roles.mu.Unlock()
	slog.Info("Loaded staff roles", "count", len(staff))
	return nil
}

// GetRole is the user's role, or NoRole for regular users.
func (a *App) GetRole(ctx context.Context, userID int64) Role {
	if userID == a.Cfg.OwnerID {
		return Owner
	}
	a.roles.mu.RLock()
	loaded := a.roles.staff != nil
	a.roles.mu.RUnlock()
	if !loaded {
		if err := a.loadRoles(ctx); err != nil {
			logErr("Could not load the roles", err)
			return NoRole
		}
	}
	a.roles.mu.RLock()
	defer a.roles.mu.RUnlock()
	return a.roles.staff[userID]
}
