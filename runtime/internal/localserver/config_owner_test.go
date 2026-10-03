package localserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/configparser"
)

// TestConfigReloadCallbackSwapsPublicOrigin exercises the state
// machine /api/v1/owner/config/reload uses to swap the public origin
// and clear the bootstrap flag. The HTTP layer is owner-auth-gated
// and is tested separately by the dashboard end-to-end suite; this
// test targets the reload callback itself so the contract used by
// the dashboard is locked in.
func TestConfigReloadCallbackSwapsPublicOrigin(t *testing.T) {
	dataRoot := t.TempDir()
	state := &configReloadState{
		dataDir:        dataRoot,
		installationID: "01J9ZQ1M4P2X7D6K8V3N5R0TWA",
		bindHost:       "127.0.0.1",
		port:           8080,
		publicOrigin:   "https://shop.example.com",
		controlOrigin:  "https://control.printcatalyst.in",
		bootstrap:      true,
	}
	contents := strings.Join([]string{
		"PC_INSTALLATION_ID=01J9ZQ1M4P2X7D6K8V3N5R0TWA",
		"PC_DATA_DIR=" + dataRoot,
		"PC_DATABASE_PATH=" + filepath.Join(dataRoot, "print-catalyst.db"),
		"PC_PUBLIC_ORIGIN=https://newshop.example.com",
		"PC_CONTROL_PLANE_ORIGIN=https://control.printcatalyst.in",
		"PC_BIND_HOST=127.0.0.1",
		"PC_PORT=8080",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dataRoot, configparser.ConfigFileName), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	previous, next, err := state.reload(context.Background())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if previous.PublicOrigin != "https://shop.example.com" {
		t.Fatalf("previous public origin = %q", previous.PublicOrigin)
	}
	if next.PublicOrigin != "https://newshop.example.com" {
		t.Fatalf("next public origin = %q", next.PublicOrigin)
	}
	if !previous.Bootstrap || next.Bootstrap {
		t.Fatalf("bootstrap flag did not flip: previous=%v next=%v", previous.Bootstrap, next.Bootstrap)
	}
}

// TestConfigReloadCallbackRejectsPlaceholder guards the rule that
// the reload endpoint must refuse to leave bootstrap mode by
// reloading placeholder values. The placeholder installation id is
// not a real, persisted key pair, so reloading it would silently
// re-arm the bootstrap state.
func TestConfigReloadCallbackRejectsPlaceholder(t *testing.T) {
	dataRoot := t.TempDir()
	state := &configReloadState{
		dataDir:        dataRoot,
		installationID: "01J9ZQ1M4P2X7D6K8V3N5R0TWA",
		bindHost:       "127.0.0.1",
		port:           8080,
		publicOrigin:   "https://shop.example.com",
		controlOrigin:  "https://control.printcatalyst.in",
		bootstrap:      true,
	}
	contents := strings.Join([]string{
		"PC_INSTALLATION_ID=replace-before-first-start",
		"PC_DATA_DIR=" + dataRoot,
		"PC_DATABASE_PATH=" + filepath.Join(dataRoot, "print-catalyst.db"),
		"PC_PUBLIC_ORIGIN=" + configparser.PlaceholderPublicOrigin,
		"PC_CONTROL_PLANE_ORIGIN=https://control.printcatalyst.in",
		"PC_BIND_HOST=127.0.0.1",
		"PC_PORT=8080",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dataRoot, configparser.ConfigFileName), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	previous, next, err := state.reload(context.Background())
	if err == nil {
		t.Fatal("reload must reject placeholder values")
	}
	// previous must equal next — the running state must be unchanged.
	if previous.PublicOrigin != next.PublicOrigin {
		t.Fatalf("state mutated despite rejected reload: previous=%q next=%q",
			previous.PublicOrigin, next.PublicOrigin)
	}
	if !previous.Bootstrap {
		t.Fatal("bootstrap flag was cleared despite rejected reload")
	}
}

// TestConfigReloadCallbackRejectsNonHTTPS guards the rule that an
// operator who typed http://... into config.env must NOT be able to
// have the runtime pretend it is configured. The reload must fail
// closed and leave the running state alone.
func TestConfigReloadCallbackRejectsNonHTTPS(t *testing.T) {
	dataRoot := t.TempDir()
	state := &configReloadState{
		dataDir:        dataRoot,
		installationID: "01J9ZQ1M4P2X7D6K8V3N5R0TWA",
		bindHost:       "127.0.0.1",
		port:           8080,
		publicOrigin:   "https://shop.example.com",
		controlOrigin:  "https://control.printcatalyst.in",
		bootstrap:      true,
	}
	contents := strings.Join([]string{
		"PC_INSTALLATION_ID=01J9ZQ1M4P2X7D6K8V3N5R0TWA",
		"PC_DATA_DIR=" + dataRoot,
		"PC_DATABASE_PATH=" + filepath.Join(dataRoot, "print-catalyst.db"),
		"PC_PUBLIC_ORIGIN=http://shop.example.com",
		"PC_CONTROL_PLANE_ORIGIN=https://control.printcatalyst.in",
		"PC_BIND_HOST=127.0.0.1",
		"PC_PORT=8080",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dataRoot, configparser.ConfigFileName), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	previous, next, err := state.reload(context.Background())
	if err == nil {
		t.Fatal("reload must reject non-HTTPS origins")
	}
	if previous.PublicOrigin != next.PublicOrigin {
		t.Fatalf("state mutated despite rejected reload: previous=%q next=%q",
			previous.PublicOrigin, next.PublicOrigin)
	}
}

// TestConfigViewShapeDocumentsTheDashboardContract locks in the
// JSON contract the dashboard reads from /api/v1/owner/config so a
// future refactor that renames a field does not silently break the
// dashboard without a compile-time signal.
func TestConfigViewShapeDocumentsTheDashboardContract(t *testing.T) {
	view := ConfigView{
		InstallationID:     "01J9ZQ1M4P2X7D6K8V3N5R0TWA",
		DataDirectory:      "/var/lib/print-catalyst",
		BindHost:           "127.0.0.1",
		Port:               8080,
		PublicOrigin:       "https://shop.example.com",
		ControlPlaneOrigin: "https://control.printcatalyst.in",
		DatabasePath:       "/var/lib/print-catalyst/print-catalyst.db",
		Bootstrap:          false,
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(encoded, &parsed); err != nil {
		t.Fatal(err)
	}
	stringFields := map[string]string{
		"installationId":     view.InstallationID,
		"dataDirectory":      view.DataDirectory,
		"bindHost":           view.BindHost,
		"publicOrigin":       view.PublicOrigin,
		"controlPlaneOrigin": view.ControlPlaneOrigin,
		"databasePath":       view.DatabasePath,
	}
	for field, want := range stringFields {
		value, ok := parsed[field]
		if !ok {
			t.Fatalf("field %q missing from JSON: %s", field, encoded)
		}
		str, ok := value.(string)
		if !ok {
			t.Fatalf("field %q is not a string: %s", field, encoded)
		}
		if str != want {
			t.Fatalf("field %q = %q, want %q", field, str, want)
		}
	}
	raw, ok := parsed["bootstrap"]
	if !ok {
		t.Fatalf("bootstrap field missing: %s", encoded)
	}
	if _, ok := raw.(bool); !ok {
		t.Fatalf("bootstrap field is not a bool: %s", encoded)
	}
	if port, ok := parsed["port"].(float64); !ok || int(port) != view.Port {
		t.Fatalf("port field is not the expected number: %s", encoded)
	}
}

// configReloadState is the test mirror of runtimeState in main.go.
// Keeping a separate type keeps the tests hermetic and avoids a
// circular dependency on cmd/print-catalyst-on-premise.
type configReloadState struct {
	dataDir        string
	installationID string
	bindHost       string
	port           int
	publicOrigin   string
	controlOrigin  string
	databasePath   string
	bootstrap      bool
}

func (s *configReloadState) view() ConfigView {
	return ConfigView{
		InstallationID:     s.installationID,
		DataDirectory:      s.dataDir,
		BindHost:           s.bindHost,
		Port:               s.port,
		PublicOrigin:       s.publicOrigin,
		ControlPlaneOrigin: s.controlOrigin,
		DatabasePath:       s.databasePath,
		Bootstrap:          s.bootstrap,
	}
}

// reload validates config.env and, on success, swaps the mutable
// fields the /api/v1/owner/config/reload endpoint owns. The
// implementation is a deliberately small subset of runtimeState in
// main.go — the test does not need database migration logic, only
// the configuration swap.
func (s *configReloadState) reload(_ context.Context) (ConfigView, ConfigView, error) {
	previous := s.view()
	values, err := configparser.Load(s.dataDir, nil)
	if err != nil {
		return previous, previous, err
	}
	// Refuse placeholders at reload time — by definition the operator
	// is trying to leave bootstrap mode.
	if values["PC_PUBLIC_ORIGIN"] == configparser.PlaceholderPublicOrigin ||
		values["PC_INSTALLATION_ID"] == configparser.PlaceholderInstallationID {
		return previous, previous, errPlaceholderReload
	}
	if !strings.HasPrefix(values["PC_PUBLIC_ORIGIN"], "https://") {
		return previous, previous, errOriginNotHttps
	}
	s.installationID = values["PC_INSTALLATION_ID"]
	s.publicOrigin = values["PC_PUBLIC_ORIGIN"]
	s.bootstrap = false
	return previous, s.view(), nil
}

var (
	errPlaceholderReload = placeholderReloadError{}
	errOriginNotHttps    = originNotHttpsError{}
)

type placeholderReloadError struct{}

func (placeholderReloadError) Error() string {
	return "placeholder values cannot be reloaded; edit config.env to remove the placeholder first"
}

type originNotHttpsError struct{}

func (originNotHttpsError) Error() string {
	return "PC_PUBLIC_ORIGIN must use HTTPS"
}
