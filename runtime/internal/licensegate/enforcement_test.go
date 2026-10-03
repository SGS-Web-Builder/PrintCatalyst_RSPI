package licensegate

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
)

func TestRequiredGate(t *testing.T) {
	err := CheckRequired(context.Background(), nil)
	if (err != nil) != Required {
		t.Fatal("incorrect missing-gate policy")
	}
	sentinel := errors.New("revoked")
	if !errors.Is(CheckRequired(context.Background(), func(context.Context) error { return sentinel }), sentinel) {
		t.Fatal("ignored rejection")
	}
	if CheckRequired(context.Background(), func(context.Context) error { return nil }) != nil {
		t.Fatal("valid gate rejected")
	}
}
func TestReleaseConfiguration(t *testing.T) {
	if !Required {
		t.Skip("release-only policy")
	}
	oldURL, oldKey := ReleaseURL, ReleasePublicKey
	defer func() { ReleaseURL, ReleasePublicKey = oldURL, oldKey }()
	ReleaseURL = "https://licenses.printcatalyst.in"
	ReleasePublicKey = ""
	if ValidateReleaseConfig() == nil {
		t.Fatal("missing verification key accepted")
	}
	ReleasePublicKey = base64.StdEncoding.EncodeToString(make([]byte, 32))
	ReleaseURL = "https://other.example"
	if ValidateReleaseConfig() == nil {
		t.Fatal("wrong publisher accepted")
	}
	ReleaseURL = "https://licenses.printcatalyst.in"
	if ValidateReleaseConfig() != nil {
		t.Fatal("configured release rejected")
	}
}
