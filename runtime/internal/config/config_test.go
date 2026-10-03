package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func environment(values map[string]string) Getenv {
	return func(key string) string { return values[key] }
}

func validEnvironment() map[string]string {
	return map[string]string{
		"PC_INSTALLATION_ID":      "01J9ZQ1M4P2X7D6K8V3N5R0TWA",
		"PC_DATA_DIR":             "/var/lib/print-catalyst",
		"PC_PUBLIC_ORIGIN":        "https://print.example-shop.com",
		"PC_CONTROL_PLANE_ORIGIN": "https://control.printcatalyst.in",
		"PC_DATABASE_PATH":        "/var/lib/print-catalyst/print-catalyst.db",
	}
}

func TestLoadUsesSafeLocalDefaults(t *testing.T) {
	dir := t.TempDir()
	values := map[string]string{
		"PC_INSTALLATION_ID":      "01J9ZQ1M4P2X7D6K8V3N5R0TWA",
		"PC_DATA_DIR":             dir,
		"PC_PUBLIC_ORIGIN":        "https://print.example-shop.com",
		"PC_CONTROL_PLANE_ORIGIN": "https://control.printcatalyst.in",
		"PC_DATABASE_PATH":        filepath.Join(dir, "print-catalyst.db"),
	}
	got, err := Load(environment(values), dir)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.BindHost != "127.0.0.1" || got.Port != 8080 {
		t.Fatalf("unsafe defaults: host=%q port=%d", got.BindHost, got.Port)
	}
	if got.DatabasePath != filepath.Join(dir, "print-catalyst.db") {
		t.Fatalf("database path = %q", got.DatabasePath)
	}
	if got.Bootstrap {
		t.Fatal("valid environment reported as bootstrap")
	}
}

func TestLoadReportsBootstrapForPlaceholderValues(t *testing.T) {
	dir := t.TempDir()
	values := map[string]string{
		"PC_INSTALLATION_ID":      "replace-before-first-start",
		"PC_DATA_DIR":             dir,
		"PC_PUBLIC_ORIGIN":        "https://replace-me.invalid",
		"PC_CONTROL_PLANE_ORIGIN": "https://control.printcatalyst.in",
		"PC_DATABASE_PATH":        filepath.Join(dir, "print-catalyst.db"),
	}
	got, err := Load(environment(values), dir)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !got.Bootstrap {
		t.Fatal("placeholder values should report Bootstrap=true")
	}
}

func TestLoadRejectsUnsafeConfiguration(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name    string
		key     string
		value   string
		message string
	}{
		{"invalid binding", "PC_BIND_HOST", "not-an-ip", "IP address"},
		{"remote database", "DATABASE_URL", "postgresql://db/app", "embedded local database"},
		{"platform payment secret", "RAZORPAY_KEY_SECRET", "secret", "platform Razorpay secrets"},
		{"insecure public URL", "PC_PUBLIC_ORIGIN", "http://shop.example", "must use HTTPS"},
		{"URL containing path", "PC_PUBLIC_ORIGIN", "https://shop.example/orders", "without credentials, path"},
		{"privileged port", "PC_PORT", "80", "1024 through 65535"},
		{"database escape", "PC_DATABASE_PATH", filepath.Join(dir, "..", "other.db"), "inside PC_DATA_DIR"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := map[string]string{
				"PC_INSTALLATION_ID":      "01J9ZQ1M4P2X7D6K8V3N5R0TWA",
				"PC_DATA_DIR":             dir,
				"PC_PUBLIC_ORIGIN":        "https://print.example-shop.com",
				"PC_CONTROL_PLANE_ORIGIN": "https://control.printcatalyst.in",
				"PC_DATABASE_PATH":        filepath.Join(dir, "print-catalyst.db"),
			}
			values[test.key] = test.value
			_, err := Load(environment(values), dir)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.message)) {
				t.Fatalf("Load() error = %v, want message containing %q", err, test.message)
			}
		})
	}
}

func TestLoadAcceptsDatabaseInsidePOSIXDataDirectory(t *testing.T) {
	values := map[string]string{
		"PC_INSTALLATION_ID":      "01J9ZQ1M4P2X7D6K8V3N5R0TWA",
		"PC_DATA_DIR":             "/var/lib/print-catalyst",
		"PC_PUBLIC_ORIGIN":        "https://shop.example.com",
		"PC_CONTROL_PLANE_ORIGIN": "https://control.printcatalyst.in",
		"PC_DATABASE_PATH":        "/var/lib/print-catalyst/db/runtime.sqlite",
	}
	if _, err := Load(environment(values), "/var/lib/print-catalyst"); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadReadsConfigFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.env"), []byte(
		"PC_INSTALLATION_ID=01J9ZQ1M4P2X7D6K8V3N5R0TWA\n"+
			"PC_PUBLIC_ORIGIN=https://shop.example.com\n"+
			"PC_CONTROL_PLANE_ORIGIN=https://control.printcatalyst.in\n"+
			"PC_PORT=9090\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		"PC_DATABASE_PATH": filepath.Join(dir, "print-catalyst.db"),
	}
	got, err := Load(environment(values), dir)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Port != 9090 {
		t.Fatalf("port = %d, want 9090", got.Port)
	}
}

func TestLoadRejectsNonAbsoluteDataDirectory(t *testing.T) {
	if _, err := Load(environment(map[string]string{"PC_DATA_DIR": "rel", "PC_DATABASE_PATH": "/tmp/x.db"}), "/home/user"); err == nil {
		t.Fatal("expected error for non-absolute data directory")
	}
	if _, err := Load(environment(map[string]string{}), "  "); err == nil {
		t.Fatal("expected error for missing data directory")
	}
}

func TestLoadAcceptsHexFingerprintInstallationID(t *testing.T) {
	values := map[string]string{
		"PC_INSTALLATION_ID":      "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"PC_PUBLIC_ORIGIN":        "https://shop.example.com",
		"PC_CONTROL_PLANE_ORIGIN": "https://control.printcatalyst.in",
		"PC_DATABASE_PATH":        "/var/lib/print-catalyst/print-catalyst.db",
	}
	got, err := Load(environment(values), "/var/lib/print-catalyst")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Bootstrap {
		t.Fatal("valid fingerprint should not report bootstrap")
	}
}

func TestLoadRejectsMalformedInstallationID(t *testing.T) {
	values := map[string]string{
		"PC_INSTALLATION_ID":      "not-a-real-id",
		"PC_PUBLIC_ORIGIN":        "https://shop.example.com",
		"PC_CONTROL_PLANE_ORIGIN": "https://control.printcatalyst.in",
		"PC_DATABASE_PATH":        "/var/lib/print-catalyst/print-catalyst.db",
	}
	_, err := Load(environment(values), "/var/lib/print-catalyst")
	if err == nil || !strings.Contains(err.Error(), "PC_INSTALLATION_ID") {
		t.Fatalf("expected PC_INSTALLATION_ID error, got %v", err)
	}
}

func TestResolveDataDirectoryHonoursExplicitEnv(t *testing.T) {
	if got := ResolveDataDirectory(func(string) string { return "/custom/path" }); got != "/custom/path" {
		t.Fatalf("ResolveDataDirectory = %q", got)
	}
}
