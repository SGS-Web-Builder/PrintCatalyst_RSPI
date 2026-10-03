package localserver_test

import (
	"testing"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localfiles"
)

// mustNewFiles creates a localfiles.Files rooted at dir and fails the test on
// any error. Tests use a temp directory because the protected data directory
// must be writable.
func mustNewFiles(t *testing.T, dir string) *localfiles.Files {
	t.Helper()
	files, err := localfiles.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	return files
}
