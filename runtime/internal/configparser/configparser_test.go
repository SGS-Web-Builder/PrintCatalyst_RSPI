package configparser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadFileMissingDirectory(t *testing.T) {
	values, err := Load(filepath.Join(t.TempDir(), "missing"), os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if values == nil {
		t.Fatal("Load returned nil map")
	}
}

func TestLoadParsesConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.env")
	contents := []string{
		"# header",
		"PC_INSTALLATION_ID = 01J9ZQ1M4P2X7D6K8V3N5R0TWA",
		"PC_PUBLIC_ORIGIN=https://shop.example.com",
		"",
		"PC_PORT = 9090 ",
	}
	if err := os.WriteFile(path, []byte(strings.Join(contents, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	values, err := Load(dir, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if values["PC_INSTALLATION_ID"] != "01J9ZQ1M4P2X7D6K8V3N5R0TWA" {
		t.Fatalf("installation id = %q", values["PC_INSTALLATION_ID"])
	}
	if values["PC_PUBLIC_ORIGIN"] != "https://shop.example.com" {
		t.Fatalf("public origin = %q", values["PC_PUBLIC_ORIGIN"])
	}
	if values["PC_PORT"] != "9090" {
		t.Fatalf("port = %q", values["PC_PORT"])
	}
}

func TestLoadReportsMalformedLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.env")
	if err := os.WriteFile(path, []byte("PC_PORT=9000\nNOEQUALSSIGN\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir, os.Getenv)
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("expected line 2 error, got %v", err)
	}
}

func TestLoadEnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.env"), []byte("PC_PORT=8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	values, err := Load(dir, func(key string) string {
		if key == "PC_PORT" {
			return "9090"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if values["PC_PORT"] != "9090" {
		t.Fatalf("env override did not win: %q", values["PC_PORT"])
	}
}

func TestLoadEnvEmptyDoesNotOverrideFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.env"), []byte("PC_PORT=8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	values, err := Load(dir, func(string) string { return "  " })
	if err != nil {
		t.Fatal(err)
	}
	if values["PC_PORT"] != "8080" {
		t.Fatalf("blank env overrode file: %q", values["PC_PORT"])
	}
}

func TestLoadIgnoresUnknownEnvVariables(t *testing.T) {
	dir := t.TempDir()
	values, err := Load(dir, func(key string) string {
		if key == "PATH" {
			return "/usr/bin"
		}
		if key == "RAZORPAY_KEY_SECRET" {
			return "leaked"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := values["RAZORPAY_KEY_SECRET"]; ok {
		t.Fatal("unknown env variables leaked into merged map")
	}
	if _, ok := values["PATH"]; ok {
		t.Fatal("PATH leaked into merged map")
	}
}

func TestLoadRequiresDataDirectory(t *testing.T) {
	if _, err := Load("", nil); err == nil {
		t.Fatal("empty data directory must error")
	}
}

func TestDefaultDataDirectoryRespectsOverrides(t *testing.T) {
	t.Setenv("PC_DATA_DIR", "/custom/path")
	if got := DefaultDataDirectory(); got != "/custom/path" {
		t.Fatalf("DefaultDataDirectory = %q", got)
	}
}

func TestPlaceholdersExported(t *testing.T) {
	if PlaceholderInstallationID != "replace-before-first-start" {
		t.Fatalf("PlaceholderInstallationID = %q", PlaceholderInstallationID)
	}
	if PlaceholderPublicOrigin != "https://replace-me.invalid" {
		t.Fatalf("PlaceholderPublicOrigin = %q", PlaceholderPublicOrigin)
	}
}
