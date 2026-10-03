package provisioning

import (
	"context"
	"database/sql"
)

// LocalReadiness checks actual shop configuration. A tunnel, online payment
// provider and external licence server are not prerequisites for LAN/cash use.
type LocalReadiness struct{ DB *sql.DB }

func (s LocalReadiness) Status(ctx context.Context) (StatusResult, error) {
	result := StatusResult{}
	checks := []struct {
		gate  Gate
		query string
	}{
		{GateOwner, "SELECT EXISTS(SELECT 1 FROM local_owner)"},
		{GateBusiness, "SELECT EXISTS(SELECT 1 FROM business_profile)"},
		{GatePricing, "SELECT EXISTS(SELECT 1 FROM pricing_rules)"},
		{GatePrinter, `SELECT EXISTS(SELECT 1 FROM printers p JOIN printer_verifications v ON v.printer_id=p.id WHERE p.enabled=1 AND p.removed_at IS NULL AND p.queue_name<>'' AND v.capability_type='paper_size' AND v.invalidated_at IS NULL AND v.status IN ('verified','confirmed','tested'))`},
	}
	for _, c := range checks {
		var ready bool
		if err := s.DB.QueryRowContext(ctx, c.query).Scan(&ready); err != nil {
			return result, err
		}
		result.Gates = append(result.Gates, GateStatus{Gate: c.gate, Complete: ready})
		if ready {
			result.Completed++
		} else if result.Next == "" {
			result.Next = c.gate
		}
	}
	result.ProductionReady = result.Completed == len(checks)
	return result, nil
}
func (s LocalReadiness) ProductionReady(ctx context.Context) (bool, error) {
	v, e := s.Status(ctx)
	return v.ProductionReady, e
}
