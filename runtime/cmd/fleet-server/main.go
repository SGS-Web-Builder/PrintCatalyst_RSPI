package main

import (
	"encoding/json"
	"flag"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/fleet"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	configPath := flag.String("config", "fleet-config.json", "private fleet configuration file")
	state := flag.String("state", "fleet-state.json", "heartbeat state path")
	flag.Parse()
	raw, err := os.ReadFile(*configPath)
	if err != nil {
		log.Fatal("Cannot read fleet configuration")
	}
	var c fleet.Config
	if json.Unmarshal(raw, &c) != nil {
		log.Fatal("Invalid fleet configuration")
	}
	s, err := fleet.New(c, *state)
	if err != nil {
		log.Fatal(err)
	}
	srv := http.Server{Addr: "127.0.0.1:8090", Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	log.Print("Fleet listening on loopback :8090; publish only through an HTTPS reverse proxy")
	log.Fatal(srv.ListenAndServe())
}
