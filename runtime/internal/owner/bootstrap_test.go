package owner

import (
	"context"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localfiles"
	"testing"
)

func TestBootstrapTokenPersistsButCannotReopenOwnerSetup(t *testing.T) {
	db, s, _ := fixture(t)
	files, err := localfiles.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.EnsureBootstrapToken(context.Background(), files)
	if err != nil || len(token) != 64 {
		t.Fatalf("bootstrap: %v", err)
	}
	again, err := s.EnsureBootstrapToken(context.Background(), files)
	if err != nil || again != token {
		t.Fatal("bootstrap changed on restart")
	}
	allowLicence(t, db)
	if err := s.Create(context.Background(), "owner", "a sufficiently long password"); err != nil {
		t.Fatal(err)
	}
	closed, err := s.EnsureBootstrapToken(context.Background(), files)
	if err != nil || closed != "" {
		t.Fatal("bootstrap stayed available after owner creation")
	}
}
