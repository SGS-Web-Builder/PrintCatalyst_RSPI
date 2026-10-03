//go:build DEV_UNLICENSED && !production

package main

import (
	"context"
	"log"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/owner"
)

// devAutoCreateOwner creates a default "admin"/"admin" owner if none exists.
// It is called only in builds with the DEV_UNLICENSED tag so that
// testing a fresh install does not require the operator to read the
// owner-setup-token.txt file and fill in the setup wizard form.
// Build with:  go build -tags DEV_UNLICENSED ./cmd/print-catalyst-on-premise
func devAutoCreateOwner(ctx context.Context, accounts *owner.Service) {
	exists, err := accounts.Exists(ctx)
	if err != nil {
		log.Printf("devAutoCreateOwner: Exists check failed: %v", err)
		return
	}
	if exists {
		log.Printf("devAutoCreateOwner: owner already exists — skipping")
		return
	}
	// Use a fixed dev credential so testers can log in immediately.
	if err := accounts.Create(ctx, "admin", "admin-dev-password-12chars!"); err != nil {
		log.Printf("devAutoCreateOwner: Create failed: %v", err)
		return
	}
	log.Printf("devAutoCreateOwner: created admin — open http://127.0.0.1:8080/ and sign in with admin / admin-dev-password-12chars!")
}
