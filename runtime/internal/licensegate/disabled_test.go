//go:build licensefree

package licensegate

import (
	"context"
	"testing"
)

func TestLicenseFreeStartupAndRelease(t *testing.T) {
	if Required {
		t.Fatal("licence required")
	}
	c, err := New(nil, "", "invalid", "invalid")
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateReleaseConfig(); err != nil {
		t.Fatal(err)
	}
	if err = CheckRequired(context.Background(), c.Check); err != nil {
		t.Fatal(err)
	}
	if err = c.Activate(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if s := c.Status(context.Background()); !s.Active || !s.Configured {
		t.Fatal(s)
	}
}
