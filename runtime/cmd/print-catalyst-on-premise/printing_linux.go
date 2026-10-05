//go:build linux

package main

import (
	"context"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/prepared"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers/discover"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers/dispatch"
	"os"
	"path/filepath"
)

func platformPrinting(dataDir string, config dispatch.DispatcherConfig) (dispatch.PrinterBackend, dispatch.DispatcherConfig, func(), error) {
	dir := filepath.Join(dataDir, "prepared")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, config, nil, err
	}
	store, err := prepared.Open(dir)
	if err != nil {
		return nil, config, nil, err
	}
	config.Prepared = store
	config.Renderer = dispatch.NewPDFRenderer()
	return dispatch.NewCUPSBackend(), config, func() { _ = store.Close() }, nil
}

func registerNativePrinting(svc *printers.Service) bool {
	svc.RegisterDiscoverer(platformDiscovererAdapter(printers.BackendIPP, func(ctx context.Context, out chan<- printers.DiscoveredRecord) {
		discover.NewCUPS().Watch(ctx, bridgeToRecordChannel(out))
	}))
	return true
}
