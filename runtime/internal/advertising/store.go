package advertising

import (
	"context"
	"database/sql"
	"encoding/json"
)

type Campaign struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Filename string   `json:"filename"`
	Type     string   `json:"type"`
	Size     int64    `json:"size"`
	Schedule Schedule `json:"schedule"`
}

func List(ctx context.Context, db *sql.DB) ([]Campaign, error) {
	rows, e := db.QueryContext(ctx, "SELECT id,name,filename,media_type,size,schedule FROM ad_campaigns ORDER BY created_at,id")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	list := []Campaign{}
	for rows.Next() {
		var c Campaign
		var raw string
		if e = rows.Scan(&c.ID, &c.Name, &c.Filename, &c.Type, &c.Size, &raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(raw), &c.Schedule); e != nil {
			return nil, e
		}
		list = append(list, c)
	}
	return list, rows.Err()
}
