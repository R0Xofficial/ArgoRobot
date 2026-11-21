package core

import "context"

// Approved users: people a chat's admins trust without making them admins (core/approvals.py). An approved user is
// exempt from the automatic measures of the bot in that chat only. A module that adds such a measure only has to ask
// IsApproved. Approval never stops an admin from acting by hand.

// Approval is one approved user.
type Approval struct {
	UserID     int64
	Name       string
	ApprovedBy int64 // 0 when unknown
	ByName     string
}

// Approve approves a user in a chat. False if they already were.
func (a *App) Approve(ctx context.Context, chatID, userID int64, name string, by int64, byName string) (bool, error) {
	var byV any
	if by != 0 {
		byV = by
	}
	return a.DB.Found(ctx, `INSERT INTO approvals (chat_id, user_id, name, approved_by, approved_by_name)
VALUES ($1, $2, $3, $4, $5) ON CONFLICT (chat_id, user_id) DO NOTHING RETURNING user_id`,
		[]any{chatID, userID, nullable(name), byV, nullable(byName)}, new(int64))
}

func (a *App) Unapprove(ctx context.Context, chatID, userID int64) (bool, error) {
	return a.DB.Found(ctx, `DELETE FROM approvals WHERE chat_id = $1 AND user_id = $2 RETURNING user_id`,
		[]any{chatID, userID}, new(int64))
}

func (a *App) IsApproved(ctx context.Context, chatID, userID int64) (bool, error) {
	return a.DB.Found(ctx, `SELECT 1 FROM approvals WHERE chat_id = $1 AND user_id = $2`, []any{chatID, userID}, new(int))
}

// GetApproval is nil when the user is not approved.
func (a *App) GetApproval(ctx context.Context, chatID, userID int64) (*Approval, error) {
	var name, byName *string
	var by *int64
	var ap Approval
	found, err := a.DB.Found(ctx, `SELECT user_id, name, approved_by, approved_by_name FROM approvals WHERE chat_id = $1 AND user_id = $2`,
		[]any{chatID, userID}, &ap.UserID, &name, &by, &byName)
	if err != nil || !found {
		return nil, err
	}
	if name != nil {
		ap.Name = *name
	}
	if byName != nil {
		ap.ByName = *byName
	}
	if by != nil {
		ap.ApprovedBy = *by
	}
	return &ap, nil
}

func (a *App) ListApprovals(ctx context.Context, chatID int64) ([]Approval, error) {
	rows, err := a.DB.Pool.Query(ctx, `SELECT user_id, name FROM approvals WHERE chat_id = $1 ORDER BY approved_at, user_id`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Approval
	for rows.Next() {
		var ap Approval
		var name *string
		if err := rows.Scan(&ap.UserID, &name); err != nil {
			return nil, err
		}
		if name != nil {
			ap.Name = *name
		}
		out = append(out, ap)
	}
	return out, rows.Err()
}

// ClearApprovals removes every approval of a chat and says how many there were.
func (a *App) ClearApprovals(ctx context.Context, chatID int64) (int64, error) {
	return a.DB.Exec(ctx, `DELETE FROM approvals WHERE chat_id = $1`, chatID)
}

// ApproveHooks run after someone is approved (the global-ban and federation modules lift their bans there).
var ApproveHooks []func(c *Ctx, chatID, userID int64)
