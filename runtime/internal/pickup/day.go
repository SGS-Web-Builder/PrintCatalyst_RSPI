package pickup

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
	_ "time/tzdata"
)

// dayEnd uses the business timezone; India is the installation fallback.
func dayEnd(ctx context.Context, tx interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, at time.Time) (int64, error) {
	zone := "Asia/Kolkata"
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT profile_json FROM business_profile WHERE singleton=1`).Scan(&raw)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	if err == nil {
		var p struct {
			TimeZone string `json:"timeZone"`
		}
		if err = json.Unmarshal([]byte(raw), &p); err != nil {
			return 0, err
		}
		if p.TimeZone != "" {
			zone = p.TimeZone
		}
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return 0, err
	}
	local := at.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, loc).Unix(), nil
}
