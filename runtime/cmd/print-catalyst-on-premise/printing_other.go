//go:build !linux

package main

import (
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/printers/dispatch"
)

func platformPrinting(_ string, config dispatch.DispatcherConfig) (dispatch.PrinterBackend, dispatch.DispatcherConfig, func(), error) {
	var backend dispatch.PrinterBackend
	if dispatch.WinspoolAvailable() {
		backend = dispatch.NewWinspoolBackend()
	}
	return backend, config, func() {}, nil
}

func registerNativePrinting(*printers.Service) bool { return false }
