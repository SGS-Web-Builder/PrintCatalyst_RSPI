// Package reports provides revenue summaries and top-combination analytics
// computed from the existing orders and order_lines tables.
package reports

import (
	"context"
	"database/sql"
	"sort"
)

// Service queries the orders database and returns analytics.
type Service struct {
	db *sql.DB
}

// New creates a reports service backed by the given database handle.
func New(db *sql.DB) *Service {
	return &Service{db: db}
}

// Summary aggregates order statistics for the inclusive date range [from, to].
// Dates are Unix timestamps (seconds since epoch). All money values are in
// minor units of the respective currency.
func (s *Service) Summary(ctx context.Context, from, to int64) (Summary, error) {
	if from > to {
		from, to = to, from // swap so the query never returns an empty range accidentally
	}
	var sum Summary
	sum.From = from
	sum.To = to

	// Status breakdown.
	rows, err := s.db.QueryContext(ctx, `
SELECT status, COUNT(*), COALESCE(SUM(total_minor), 0)
FROM orders
WHERE created_at >= ? AND created_at <= ?
GROUP BY status`, from, to)
	if err != nil {
		return Summary{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var count int
		var total int64
		if err := rows.Scan(&status, &count, &total); err != nil {
			return Summary{}, err
		}
		sum.ByStatus = append(sum.ByStatus, StatusRow{Status: status, Count: count, TotalMinor: total})
	}
	if err := rows.Err(); err != nil {
		return Summary{}, err
	}

	// Daily breakdown (only paid/settled orders count towards revenue).
	sum.Daily = make([]DailyRow, 0)
	dailyRows, err := s.db.QueryContext(ctx, `
SELECT
    date(created_at, 'unixepoch') AS day,
    COUNT(*),
    COALESCE(SUM(total_minor), 0)
FROM orders
WHERE created_at >= ? AND created_at <= ?
  AND status IN ('paid', 'dispatched', 'completed')
GROUP BY day
ORDER BY day ASC`, from, to)
	if err != nil {
		return Summary{}, err
	}
	defer dailyRows.Close()
	for dailyRows.Next() {
		var row DailyRow
		if err := dailyRows.Scan(&row.Date, &row.OrderCount, &row.RevenueMinor); err != nil {
			return Summary{}, err
		}
		sum.Daily = append(sum.Daily, row)
	}
	if err := dailyRows.Err(); err != nil {
		return Summary{}, err
	}

	return sum, nil
}

// TopCombinations returns the most-frequently-ordered print configurations
// (paper size + colour mode + sides) by line count, limited to the top N.
func (s *Service) TopCombinations(ctx context.Context, from, to int64, limit int) ([]CombinationRow, error) {
	if from > to {
		from, to = to, from
	}
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT ol.paper_size, ol.colour_mode, ol.sides,
       COUNT(*) AS line_count,
       COALESCE(SUM(ol.line_total_minor), 0) AS revenue_minor
FROM order_lines ol
JOIN orders o ON o.id = ol.order_id
WHERE o.created_at >= ? AND o.created_at <= ?
  AND o.status IN ('paid', 'dispatched', 'completed')
GROUP BY ol.paper_size, ol.colour_mode, ol.sides
ORDER BY line_count DESC, revenue_minor DESC
LIMIT ?`, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CombinationRow
	for rows.Next() {
		var r CombinationRow
		if err := rows.Scan(&r.PaperSize, &r.ColourMode, &r.Sides, &r.LineCount, &r.RevenueMinor); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Summary is the top-level report for a date range.
type Summary struct {
	From     int64        `json:"from"`
	To       int64        `json:"to"`
	ByStatus []StatusRow  `json:"byStatus"`
	Daily    []DailyRow   `json:"daily"`
}

// StatusRow is the count and revenue for one order status.
type StatusRow struct {
	Status      string `json:"status"`
	Count       int    `json:"count"`
	TotalMinor  int64  `json:"totalMinor"`
}

// DailyRow is the per-calendar-day order count and settled revenue.
type DailyRow struct {
	Date          string `json:"date"` // ISO 8601: YYYY-MM-DD
	OrderCount    int    `json:"orderCount"`
	RevenueMinor  int64  `json:"revenueMinor"`
}

// CombinationRow is one paper/colour/sides combination with its volume.
type CombinationRow struct {
	PaperSize    string `json:"paperSize"`
	ColourMode  string `json:"colourMode"`
	Sides       string `json:"sides"`
	LineCount   int    `json:"lineCount"`
	RevenueMinor int64  `json:"revenueMinor"`
}

// SortDaily returns s.Daily sorted by date ascending. The Summary struct
// always returns daily rows sorted ascending from the query; this helper is
// provided for callers that may have mutated the slice.
func (s *Summary) SortDaily() {
	sort.Slice(s.Daily, func(i, j int) bool {
		return s.Daily[i].Date < s.Daily[j].Date
	})
}
