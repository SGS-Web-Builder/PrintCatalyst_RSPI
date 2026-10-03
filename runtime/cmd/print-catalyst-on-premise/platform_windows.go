//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/eventlog"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/supervisor"
	"golang.org/x/sys/windows/svc"
)

const windowsServiceName = "PrintCatalystOnPremise"

type serviceHandler struct {
	service *supervisor.Supervisor
	logger  eventlog.Writer
}

func (handler serviceHandler) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	if handler.logger != nil {
		_ = handler.logger.Info(fmt.Sprintf("%s service starting", windowsServiceName))
	}
	if err := handler.service.Start(context.Background()); err != nil {
		if handler.logger != nil {
			_ = handler.logger.Error(fmt.Sprintf("%s service start failed: %v", windowsServiceName, err))
		}
		return true, 1
	}
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for request := range requests {
		switch request.Cmd {
		case svc.Interrogate:
			status <- request.CurrentStatus
		case svc.Stop, svc.Shutdown:
			status <- svc.Status{State: svc.StopPending}
			if handler.logger != nil {
				_ = handler.logger.Info(fmt.Sprintf("%s service received stop request", windowsServiceName))
			}
			shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			err := handler.service.Stop(shutdown)
			cancel()
			if err != nil {
				if handler.logger != nil {
					_ = handler.logger.Error(fmt.Sprintf("%s service stop failed: %v", windowsServiceName, err))
				}
				return true, 1
			}
			if handler.logger != nil {
				_ = handler.logger.Info(fmt.Sprintf("%s service stopped", windowsServiceName))
			}
			return false, 0
		}
	}
	return false, 0
}

func runPlatform(service *supervisor.Supervisor, _ string, consoleMode bool) error {
	if consoleMode {
		// Console mode: run directly without the Windows Service wrapper.
		// All startup output goes to stdout/stderr so the operator can
		// read errors in a regular Command Prompt window.
		if err := service.Start(context.Background()); err != nil {
			return err
		}
		// Block until the operator sends Ctrl+C or the process is killed.
		// Catch both SIGINT (Ctrl+C) and SIGTERM (kill) so graceful shutdown
		// works in all hosting scenarios.
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		<-sigCh
		fmt.Println("shutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return service.Stop(ctx)
	}
	isService, err := svc.IsWindowsService()
	if err != nil {
		return fmt.Errorf("detect Windows service: %w", err)
	}
	if !isService {
		return fmt.Errorf("run this build through the installed %s service, or pass --console", windowsServiceName)
	}
	logger, err := eventlog.Open("")
	if err != nil {
		// Run the service even if the Event Log source is missing;
		// the supervisor still surfaces errors through the regular
		// service control manager and we do not want a missing log
		// source to be the reason the service refuses to start.
		logger = nil
	}
	defer func() {
		if logger != nil {
			_ = logger.Close()
		}
	}()
	return svc.Run(windowsServiceName, serviceHandler{service: service, logger: logger})
}
