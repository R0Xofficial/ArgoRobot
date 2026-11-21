package core

import "context"

// Report alerts: which admins want to be told about reports in their private chat with the bot.
// Everyone does until they turn it off, so only the opt-outs are stored.

// ReportOptedOut is the subset of ids who turned alerts off.
func (a *App) ReportOptedOut(ctx context.Context, ids []int64) (map[int64]bool, error) {
	out := map[int64]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := a.DB.Pool.Query(ctx, "SELECT user_id FROM report_optout WHERE user_id = ANY($1::bigint[])", ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func (a *App) ReportAlertsOn(ctx context.Context, userID int64) (bool, error) {
	off, err := a.ReportOptedOut(ctx, []int64{userID})
	return !off[userID], err
}

func (a *App) SetReportAlerts(ctx context.Context, userID int64, on bool) error {
	var err error
	if on {
		_, err = a.DB.Exec(ctx, "DELETE FROM report_optout WHERE user_id = $1", userID)
	} else {
		_, err = a.DB.Exec(ctx, "INSERT INTO report_optout (user_id) VALUES ($1) ON CONFLICT DO NOTHING", userID)
	}
	return err
}
