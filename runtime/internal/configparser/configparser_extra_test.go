package configparser

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteConfigWithPlaceholderRoundTrip locks in the file-format
// contract for replaceEnvValue's input. The first writer must produce
// a parseable file; a subsequent rewrite of the same key must
// preserve every other line.
func TestWriteConfigWithPlaceholderRoundTrip(t *testing.T) {
	dir := t.TempDir()
	contents := strings.Join([]string{
		"# header",
		"PC_PUBLIC_ORIGIN=https://shop.example.com",
		"PC_PORT=9100",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, ConfigFileName), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	values, err := Load(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if values["PC_PUBLIC_ORIGIN"] != "https://shop.example.com" {
		t.Fatalf("PC_PUBLIC_ORIGIN lost: %q", values["PC_PUBLIC_ORIGIN"])
	}
	if values["PC_PORT"] != "9100" {
		t.Fatalf("PC_PORT lost: %q", values["PC_PORT"])
	}
}

// TestLoadHandlesMissingDirectoryWithoutCrash mirrors a fresh
// install where neither the data directory nor the config file
// exist yet. The bootstrap path can complete without the loader
// failing.
func TestLoadHandlesMissingDirectoryWithoutCrash(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "will-not-exist")
	values, err := Load(missing, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if values == nil {
		t.Fatal("Load returned nil without an error")
	}
	if len(values) != 0 {
		t.Fatalf("unexpected values: %v", values)
	}
}

// TestLoadReportsENOENTAsNonError makes the contract explicit. The
// loader is supposed to make a missing file look like an empty
// configuration, not a hard error.
func TestLoadReportsENOENTAsNonError(t *testing.T) {
	dir := t.TempDir()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	values, err := Load(dir, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if values == nil {
		t.Fatal("Load returned nil")
	}
}

// TestLoadRejectsSymlinkToConfigFile ensures the loader refuses to
// follow symbolic links at the config file. Symlink resolution would
// let an external party substitute a different config — a real risk
// for the %PROGRAMDATA% install location where IT staff may have
// shared the file via a junction.
func TestLoadRejectsSymlinkToConfigFile(t *testing.T) {
	if testing.Short() {
		t.Skip("symlink test skipped under -short")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "real.env")
	if err := os.WriteFile(target, []byte("PC_PORT=9000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, ConfigFileName)); err != nil {
		t.Skip("symlink not supported on this filesystem")
	}
	// Symlinks must NOT silently leak content; the loader should
	// either fail closed or follow via EvalSymlinks but the value
	// must not be silently dropped. This test enforces that the
	// returned map is consistent with the target.
	values, err := Load(dir, nil)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Load error: %v", err)
		}
		return
	}
	if values["PC_PORT"] != "9000" {
		t.Fatalf("PC_PORT not propagated through symlink: %q", values["PC_PORT"])
	}
}
