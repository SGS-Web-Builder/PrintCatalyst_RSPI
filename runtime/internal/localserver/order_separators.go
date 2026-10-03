package localserver

import "context"

type queueSeparator struct {
	Queue      string `json:"queue"`
	Paper      string `json:"paper"`
	Tray       string `json:"tray"`
	PrintState string `json:"printState"`
	PrintError string `json:"printError"`
}

func (s *Server) enrichSeparators(ctx context.Context, views []orderListView, indexes map[string]int) error {
	rows, err := s.db.QueryContext(ctx, `SELECT order_id,queue_name,paper,tray,COALESCE(NULLIF(progress,''),state),COALESCE(NULLIF(progress_detail,''),error) FROM separator_invoices WHERE state<>'skipped' ORDER BY order_id,queue_name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var v queueSeparator
		if err = rows.Scan(&id, &v.Queue, &v.Paper, &v.Tray, &v.PrintState, &v.PrintError); err != nil {
			return err
		}
		if i, ok := indexes[id]; ok {
			views[i].Separators = append(views[i].Separators, v)
		}
	}
	return rows.Err()
}
