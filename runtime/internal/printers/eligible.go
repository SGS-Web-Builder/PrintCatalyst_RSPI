package printers

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

// EligiblePrinter keeps combinations tied to one enabled, verified queue.
type EligiblePrinter struct {
	ID      string   `json:"id"`
	Queue   string   `json:"-"`
	Papers  []string `json:"paperSizes"`
	Colours []string `json:"colourModes"`
	Sides   []string `json:"sidesModes"`
}

func Eligible(ctx context.Context, db *sql.DB, service, selected string) ([]EligiblePrinter, error) {
	rows, err := db.QueryContext(ctx, `SELECT p.id,p.queue_name,c.normalized_json,ps.paper_key
 FROM printers p
 JOIN printer_capabilities c ON c.id=(SELECT c2.id FROM printer_capabilities c2 WHERE c2.printer_id=p.id ORDER BY c2.captured_at DESC,c2.id DESC LIMIT 1)
 JOIN printer_paper_sizes ps ON ps.printer_id=p.id AND ps.removed_at IS NULL
 JOIN printer_verifications v ON v.printer_id=p.id AND v.capability_type='paper_size' AND v.capability_key=ps.paper_key
 WHERE p.enabled=1 AND p.removed_at IS NULL AND p.queue_name<>''
 AND v.invalidated_at IS NULL AND v.status IN ('verified','confirmed','tested') AND (?='' OR p.id=?)
 AND (?='' OR EXISTS(SELECT 1 FROM service_printers sp JOIN services s ON s.id=sp.service_id WHERE sp.printer_id=p.id AND s.id=? AND s.enabled=1))
 ORDER BY p.is_default DESC,p.id,ps.paper_key`, selected, selected, service, service)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []EligiblePrinter{}
	indexes := map[string]int{}
	for rows.Next() {
		var id, queue, raw, paper string
		if err := rows.Scan(&id, &queue, &raw, &paper); err != nil {
			return nil, err
		}
		if i, ok := indexes[id]; ok {
			result[i].Papers = append(result[i].Papers, paper)
			continue
		}
		var snap Snapshot
		if json.Unmarshal([]byte(raw), &snap) != nil {
			continue
		}
		indexes[id] = len(result)
		result = append(result, EligiblePrinter{ID: id, Queue: queue, Papers: []string{paper}, Colours: snap.ColourModes, Sides: snap.SidesModes})
	}
	return result, rows.Err()
}

func (p EligiblePrinter) Supports(paper, colour, sides string) bool {
	has := func(values []string, value string) bool {
		for _, v := range values {
			if strings.EqualFold(strings.TrimSpace(v), strings.TrimSpace(value)) {
				return true
			}
		}
		return false
	}
	return has(p.Papers, paper) && has(p.Colours, colour) && has(p.Sides, sides)
}
