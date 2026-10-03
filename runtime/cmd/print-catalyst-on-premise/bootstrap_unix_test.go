//go:build !windows

// Package main_test exercises the runtime bootstrap path that lives
// in cmd/print-catalyst-on-premise/main.go. The tests do not run
// main itself; they import the bootstrap helpers to verify the
// generation / persistence / restart-survival contract described in
// the brief.

package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/licensing"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localfiles"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
)

// openBootstrapDB opens the SQLite database inside the supplied
// data directory using the same path the runtime would use. Tests
// use a single per-temp-dir database so the bootstrap helper sees a
// realistic state.
func openBootstrapDB(t *testing.T, dir string) *sql.DB {
	t.Helper()
	database, err := store.Open(context.Background(), filepath.Join(dir, "print-catalyst.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database.DB()
}

// writeConfigFile writes a config.env with the supplied PC_PUBLIC_ORIGIN
// value (PC_INSTALLATION_ID is left at the placeholder so the
// bootstrap helper has something to replace).
func writeConfigFile(t *testing.T, dir, publicOrigin string) {
	t.Helper()
	contents := strings.Join([]string{
		"# internal",
		"PC_INSTALLATION_ID=replace-before-first-start",
		"PC_DATA_DIR=" + dir,
		"PC_DATABASE_PATH=" + filepath.Join(dir, "print-catalyst.db"),
		"PC_PUBLIC_ORIGIN=" + publicOrigin,
		"PC_CONTROL_PLANE_ORIGIN=https://control.printcatalyst.in",
		"PC_BIND_HOST=127.0.0.1",
		"PC_PORT=8080",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "config.env"), []byte(contents), 0o600); err != nil {
		t.Fatalf("write config.env: %v", err)
	}
}

// readConfigFile reads back the canonical PC_* keys after a bootstrap.
// We deliberately only check the keys that the bootstrap helper
// owns; the rest of the file is the installer's responsibility.
func readConfigFile(t *testing.T, dir string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "config.env"))
	if err != nil {
		t.Fatalf("read config.env: %v", err)
	}
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		separator := strings.IndexByte(line, '=')
		if separator <= 0 {
			continue
		}
		values[strings.TrimSpace(line[:separator])] = strings.TrimSpace(line[separator+1:])
	}
	return values
}

// TestBootstrapReplacesPlaceholderOnFreshInstall is the regression for
// the bug where a fresh install would leave PC_INSTALLATION_ID at the
// placeholder value, forcing the operator to edit the file by hand.
// The runtime must generate a fresh installation identity the very
// first time it starts and write it into config.env.
func TestBootstrapReplacesPlaceholderOnFreshInstall(t *testing.T) {
	dir := t.TempDir()
	files, err := localfiles.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	db := openBootstrapDB(t, dir)
	writeConfigFile(t, dir, "https://shop.example.com")

	if err := bootstrapInstallationIdentity(context.Background(), dir, db, files); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	values := readConfigFile(t, dir)
	id := values["PC_INSTALLATION_ID"]
	if id == "replace-before-first-start" || id == "" {
		t.Fatalf("PC_INSTALLATION_ID still placeholder after bootstrap: %q", id)
	}
	if len(id) != 26 && len(id) != 64 {
		t.Fatalf("PC_INSTALLATION_ID has unexpected length %d", len(id))
	}
	// The operator-tuned value must survive the bootstrap.
	if values["PC_PUBLIC_ORIGIN"] != "https://shop.example.com" {
		t.Fatalf("bootstrap overwrote operator value: %q", values["PC_PUBLIC_ORIGIN"])
	}
}

// TestBootstrapPreservesOperatorValue guards against the regression
// where a MajorUpgrade would land on a live installation and silently
// overwrite a real installation id. The bootstrap helper owns the
// PC_INSTALLATION_ID line but only when it is still the placeholder
// or empty — a hand-edited value MUST be preserved.
func TestBootstrapPreservesOperatorValue(t *testing.T) {
	dir := t.TempDir()
	files, err := localfiles.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	db := openBootstrapDB(t, dir)

	// Step 1: bootstrap on a placeholder file to install the
	// encryption key + key pair.
	writeConfigFile(t, dir, "https://shop.example.com")
	if err := bootstrapInstallationIdentity(context.Background(), dir, db, files); err != nil {
		t.Fatalf("first bootstrap: %v", err)
	}
	first := readConfigFile(t, dir)["PC_INSTALLATION_ID"]
	if first == "" || first == "replace-before-first-start" {
		t.Fatal("first bootstrap did not install an id")
	}

	// Step 2: simulate a MajorUpgrade that overwrites the config file
	// with placeholder values. The bootstrap helper must restore the
	// id WITHOUT regenerating the underlying key pair.
	contents := strings.Join([]string{
		"PC_INSTALLATION_ID=replace-before-first-start",
		"PC_DATA_DIR=" + dir,
		"PC_DATABASE_PATH=" + filepath.Join(dir, "print-catalyst.db"),
		"PC_PUBLIC_ORIGIN=https://shop.example.com",
		"PC_CONTROL_PLANE_ORIGIN=https://control.printcatalyst.in",
		"PC_BIND_HOST=127.0.0.1",
		"PC_PORT=8080",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "config.env"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := bootstrapInstallationIdentity(context.Background(), dir, db, files); err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}
	values := readConfigFile(t, dir)
	if values["PC_INSTALLATION_ID"] != first {
		t.Fatalf("PC_INSTALLATION_ID changed after upgrade: was %q, now %q", first, values["PC_INSTALLATION_ID"])
	}
	if values["PC_PUBLIC_ORIGIN"] != "https://shop.example.com" {
		t.Fatalf("PC_PUBLIC_ORIGIN changed: %q", values["PC_PUBLIC_ORIGIN"])
	}
}

// TestBootstrapIsStableAcrossRestarts locks the rule that the
// installation identity does not change when the runtime restarts.
// The same data directory and database should produce the same id
// because the underlying key pair is stored encrypted in the database
// and reloaded on the next start.
func TestBootstrapIsStableAcrossRestarts(t *testing.T) {
	dir := t.TempDir()
	files, err := localfiles.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	db := openBootstrapDB(t, dir)
	writeConfigFile(t, dir, "https://shop.example.com")

	if err := bootstrapInstallationIdentity(context.Background(), dir, db, files); err != nil {
		t.Fatalf("first bootstrap: %v", err)
	}
	first := readConfigFile(t, dir)["PC_INSTALLATION_ID"]

	// Drop the config file to simulate an upgrade that landed a
	// pristine template — the bootstrap helper must NOT generate a
	// new id; it must regenerate the file using the persisted key
	// pair.
	if err := os.Remove(filepath.Join(dir, "config.env")); err != nil {
		t.Fatal(err)
	}

	if err := bootstrapInstallationIdentity(context.Background(), dir, db, files); err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}
	values := readConfigFile(t, dir)
	if values["PC_INSTALLATION_ID"] != first {
		t.Fatalf("PC_INSTALLATION_ID changed across restarts: was %q, now %q", first, values["PC_INSTALLATION_ID"])
	}
}

// TestBootstrapKeepsOtherKeysUntouched ensures the bootstrap helper
// does not rewrite the rest of the file with placeholders. An operator
// who hand-tunes PC_BIND_HOST or PC_PORT must keep those values after
// an upgrade.
func TestBootstrapKeepsOtherKeysUntouched(t *testing.T) {
	dir := t.TempDir()
	files, err := localfiles.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	db := openBootstrapDB(t, dir)
	contents := strings.Join([]string{
		"PC_INSTALLATION_ID=replace-before-first-start",
		"PC_DATA_DIR=" + dir,
		"PC_DATABASE_PATH=" + filepath.Join(dir, "print-catalyst.db"),
		"PC_PUBLIC_ORIGIN=https://shop.example.com",
		"PC_CONTROL_PLANE_ORIGIN=https://control.printcatalyst.in",
		"PC_BIND_HOST=127.0.0.1",
		"PC_PORT=9100",
		"# operator note",
		"# custom comment survives",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "config.env"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := bootstrapInstallationIdentity(context.Background(), dir, db, files); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.env"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, key := range []string{"PC_PORT=9100", "PC_PUBLIC_ORIGIN=https://shop.example.com", "# operator note", "# custom comment survives"} {
		if !strings.Contains(text, key) {
			t.Fatalf("bootstrap dropped %q", key)
		}
	}
}

// TestReplaceEnvValueIsConservative keeps replaceEnvValue honest.
// It must only rewrite the line when the existing value matches the
// placeholder (or is empty) and must never clobber a real value.
func TestReplaceEnvValueIsConservative(t *testing.T) {
	dir := t.TempDir()
	const real = "01J9ZQ1M4P2X7D6K8V3N5R0TWA"
	contents := "PC_INSTALLATION_ID=" + real + "\nPC_PUBLIC_ORIGIN=https://shop.example.com\n"
	if err := os.WriteFile(filepath.Join(dir, "config.env"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := replaceEnvValue(dir, "config.env", "PC_INSTALLATION_ID", "replace-before-first-start", "new-id"); err != nil {
		t.Fatal(err)
	}
	values := readConfigFile(t, dir)
	if values["PC_INSTALLATION_ID"] != real {
		t.Fatalf("real id was overwritten: was %q, now %q", real, values["PC_INSTALLATION_ID"])
	}
}

// TestBootstrapGeneratesValidULID validates the generated id shape
// — the runtime must emit a value the config loader accepts, not an
// arbitrary 32-byte blob. The shape test mirrors config.Load's
// validator.
func TestBootstrapGeneratesValidIdentifier(t *testing.T) {
	dir := t.TempDir()
	files, err := localfiles.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	db := openBootstrapDB(t, dir)
	writeConfigFile(t, dir, "https://shop.example.com")
	if err := bootstrapInstallationIdentity(context.Background(), dir, db, files); err != nil {
		t.Fatal(err)
	}
	id := readConfigFile(t, dir)["PC_INSTALLATION_ID"]
	switch {
	case len(id) == 26:
		for _, c := range id {
			if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z')) {
				t.Fatalf("id is not ULID-shaped: %q", id)
			}
		}
	case len(id) == 64:
		for _, c := range id {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				t.Fatalf("id is not hex-shaped: %q", id)
			}
		}
	default:
		t.Fatalf("unexpected id length %d for %q", len(id), id)
	}
	// The id must equal the licensing.InstallationID(publicKey) that
	// the key pair is bound to. The bootstrap helper writes the same
	// value both places so the live installation id matches the
	// license's wire-level id without the dashboard having to
	// reconcile two distinct identifiers.
	svc, err := licensing.New(db, dir)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := svc.Ensure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if licensing.InstallationID(publicKey) != id {
		t.Fatalf("config id %q != licence installation id", id)
	}
}

// TestBootstrapRefusesIdentityMismatch guards the rule that the
// bootstrap helper MUST refuse to silently overwrite a non-placeholder
// installation id that disagrees with the persisted key pair. A
// config drift would otherwise let an attacker (or a careless
// operator) rotate the device identity behind the merchant's back,
// which the encrypted-on-disk key pair exists to prevent.
func TestBootstrapRefusesIdentityMismatch(t *testing.T) {
	dir := t.TempDir()
	files, err := localfiles.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	db := openBootstrapDB(t, dir)

	// Step 1: bootstrap with the placeholder so the key pair is
	// generated and persisted.
	writeConfigFile(t, dir, "https://shop.example.com")
	if err := bootstrapInstallationIdentity(context.Background(), dir, db, files); err != nil {
		t.Fatalf("first bootstrap: %v", err)
	}
	realID := readConfigFile(t, dir)["PC_INSTALLATION_ID"]
	if realID == "" || realID == "replace-before-first-start" {
		t.Fatal("first bootstrap did not install an id")
	}

	// Step 2: rewrite config.env with a forged non-placeholder id.
	// The bootstrap helper must refuse to start because the new id
	// does not match the encrypted key pair on disk.
	forged := strings.Repeat("a", 64) // looks like a hex fingerprint
	contents := strings.Join([]string{
		"PC_INSTALLATION_ID=" + forged,
		"PC_DATA_DIR=" + dir,
		"PC_DATABASE_PATH=" + filepath.Join(dir, "print-catalyst.db"),
		"PC_PUBLIC_ORIGIN=https://shop.example.com",
		"PC_CONTROL_PLANE_ORIGIN=https://control.printcatalyst.in",
		"PC_BIND_HOST=127.0.0.1",
		"PC_PORT=8080",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "config.env"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	err = bootstrapInstallationIdentity(context.Background(), dir, db, files)
	if err == nil {
		t.Fatal("expected bootstrap to refuse mismatched id")
	}
	if !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error %v does not describe the mismatch", err)
	}
	// The file must be untouched — the bootstrap helper must
	// refuse the whole call, not silently patch the file.
	if got := readConfigFile(t, dir)["PC_INSTALLATION_ID"]; got != forged {
		t.Fatalf("forged id was changed to %q", got)
	}
}

// TestAtomicWriteFileSurvivesInterruptedWrite ensures the temp
// file is cleaned up when the rename fails, so a partially-written
// config.env never appears alongside the original. We force the
// rename to fail by making the destination a non-empty directory
// (POSIX renames refuse to clobber an existing non-empty
// directory).
func TestAtomicWriteFileSurvivesInterruptedWrite(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.env")
	if err := os.WriteFile(target, []byte("PC_INSTALLATION_ID=keep-me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Make `target` a directory so os.Rename refuses to overwrite
	// it. The temp file still lands inside `dir`; the cleanup path
	// must remove it without touching the original.
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "occupied"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteFile(target, []byte("PC_INSTALLATION_ID=new-id\n"), 0o600); err == nil {
		t.Fatal("expected error from atomicWriteFile when destination is a non-empty directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".config.env.") {
			t.Fatalf("atomicWriteFile left temp file %q behind", entry.Name())
		}
	}
	// The original mock directory must still exist (the helper must
	// not have side-effected the destination on the failure path).
	if _, err := os.Stat(filepath.Join(target, "occupied")); err != nil {
		t.Fatalf("original mock directory was disturbed: %v", err)
	}
}

// TestReadInstallationIDFromConfigEmptyFile exercises the
// helper's missing-file branch.
func TestReadInstallationIDFromConfigEmptyFile(t *testing.T) {
	dir := t.TempDir()
	got, err := readInstallationIDFromConfig(dir)
	if err != nil {
		t.Fatalf("read missing file: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty id, got %q", got)
	}
}
