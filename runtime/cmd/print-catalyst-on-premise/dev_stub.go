//go:build !DEV_UNLICENSED || production

package main

import (
	"context"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/owner"
)

// devAutoCreateOwner is a no-op stub for production builds. In DEV_UNLICENSED
// builds, the real implementation in dev.go creates a default admin/admin
// owner so testers can skip the token form.
func devAutoCreateOwner(ctx context.Context, accounts *owner.Service) {}
