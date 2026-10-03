//go:build !windows

package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/supervisor"
)

func runPlatform(service *supervisor.Supervisor, address string, _ bool) error {
	if err := service.Start(context.Background()); err != nil {
		return err
	}
	log.Printf("Print Catalyst Pi service listening on %s", address)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	<-signals
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return service.Stop(shutdown)
}
