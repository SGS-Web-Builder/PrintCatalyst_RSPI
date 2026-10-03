package licensegate

import (
	"context"
	"encoding/base64"
	"errors"
)

// CheckRequired fails closed if production wiring accidentally omits the gate.
func CheckRequired(ctx context.Context, check func(context.Context) error) error {
	if check != nil {
		return check(ctx)
	}
	if Required {
		return ErrRequired
	}
	return nil
}

// ValidateReleaseConfig rejects incomplete or misdirected merchant builds at startup.
func ValidateReleaseConfig() error {
	if !Required {
		return nil
	}
	key, err := base64.StdEncoding.DecodeString(ReleasePublicKey)
	if err != nil || len(key) != 32 || ReleaseURL != "https://licenses.printcatalyst.in" {
		return errors.New("invalid publisher licence configuration; install an official release")
	}
	return nil
}
