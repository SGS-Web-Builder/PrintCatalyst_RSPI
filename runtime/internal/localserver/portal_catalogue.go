package localserver

import (
	"context"
	"fmt"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/business"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/documents"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/orders"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/payments"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/pricing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers"
)

func (s *Server) portalFleet(ctx context.Context, service, selected string) ([]printers.EligiblePrinter, error) {
	if service == "" && selected == "" {
		if err := s.db.QueryRowContext(ctx, "SELECT primary_printer_id FROM business_settings WHERE singleton=1").Scan(&selected); err != nil {
			return nil, err
		}
	}
	return printers.Eligible(ctx, s.db, service, selected)
}

func (s *Server) validatePortalLines(ctx context.Context, service, selected string, lines []orders.QuoteLineRequest) error {
	fleet, err := s.portalFleet(ctx, service, selected)
	if err != nil {
		return err
	}
	for _, line := range lines {
		supported := false
		for _, p := range fleet {
			if p.Supports(line.PaperSize, line.ColourMode, line.Sides) {
				supported = true
				break
			}
		}
		if !supported {
			return fmt.Errorf("no eligible printer supports %s / %s / %s for this service", line.PaperSize, line.ColourMode, line.Sides)
		}
	}
	return nil
}

func (s *Server) portalOptions(ctx context.Context, service string, selection ...string) (map[string]any, error) {
	selected := ""
	if len(selection) > 0 {
		selected = selection[0]
	}
	allFleet, err := s.portalFleet(ctx, service, "")
	if err != nil {
		return nil, err
	}
	hasOverrides, err := s.pricing.HasPrinterPrices(ctx)
	if err != nil {
		return nil, err
	}
	if selected == "" && hasOverrides && len(allFleet) > 0 {
		selected = allFleet[0].ID
	}
	if selected != "" {
		found := false
		for _, p := range allFleet {
			if p.ID == selected {
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("selected printer is unavailable for this service")
		}
	}
	book, err := s.pricing.Effective(ctx, selected)
	if err != nil {
		return nil, err
	}
	services, err := business.New(s.db).ListServices(ctx)
	if err != nil {
		return nil, err
	}
	entries := book.Entries
	available := []business.ServiceRecord{}
	found := service == ""
	for _, svc := range services {
		fleet, err := s.portalFleet(ctx, svc.ID, "")
		if err != nil {
			return nil, err
		}
		if !svc.Enabled || len(fleet) == 0 {
			continue
		}
		available = append(available, svc)
		if svc.ID == service {
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("selected service is unavailable")
	}
	fleet, err := s.portalFleet(ctx, service, selected)
	if err != nil {
		return nil, err
	}
	if selected != "" {
		overrides, e := s.pricing.GridBook(ctx, selected)
		if e != nil {
			return nil, e
		}
		for _, override := range overrides.Entries {
			replaced := false
			for i, e := range entries {
				if e.PaperSize == override.PaperSize && e.ColourMode == override.ColourMode && e.Sides == override.Sides {
					entries[i] = override
					replaced = true
					break
				}
			}
			if !replaced {
				entries = append(entries, override)
			}
		}
	}
	names := []map[string]string{}
	for _, p := range allFleet {
		record, e := s.printers.Get(ctx, p.ID)
		if e != nil {
			return nil, e
		}
		names = append(names, map[string]string{"id": p.ID, "name": record.DisplayName})
	}
	combos := []pricing.Entry{}
	papers, colours, sides := map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}
	for _, entry := range entries {
		for _, p := range fleet {
			if p.Supports(entry.PaperSize, entry.ColourMode, entry.Sides) {
				combos = append(combos, entry)
				papers[entry.PaperSize] = struct{}{}
				colours[entry.ColourMode] = struct{}{}
				sides[entry.Sides] = struct{}{}
				break
			}
		}
	}
	buttons, err := s.readPortalPaymentButtons(ctx)
	if s.pickup != nil {
		buttons.CashEnabled = false
	}
	if err != nil {
		return nil, err
	}
	online := false
	if s.payments != nil {
		providers, err := s.payments.ListProviders(ctx)
		if err != nil {
			return nil, err
		}
		for _, p := range providers {
			if p.Enabled && p.Kind == payments.KindRazorpayMerchant {
				online = true
			}
		}
	}
	details, err := s.readCustomerDetails(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"customerDetails": details, "printers": names, "selectedPrinterId": selected, "printerSelectionRequired": hasOverrides, "services": available, "combinations": combos, "paperSizes": sortedKeys(papers), "colourModes": sortedKeys(colours), "sidesModes": sortedKeys(sides), "razorpayReady": online && buttons.OnlineEnabled, "cashOnCounterReady": buttons.CashEnabled, "docxAvailable": documents.DOCXAvailable(), "currency": book.Currency, "currencyMinorUnits": book.CurrencyMinorUnits}, nil
}
