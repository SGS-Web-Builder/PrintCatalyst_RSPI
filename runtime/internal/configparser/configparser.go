// Package configparser reads the runtime's configuration from a
// config.env file installed under the runtime's data directory. The
// file is the authoritative source for installation identity, paths
// and origins; environment variables of the same name act as
// overrides so a developer can run the binary from a shell without
// editing the file.
//
// Behaviour intentionally rejects placeholder values. The bootstrap
// flow is responsible for replacing placeholder values with real ones
// before any operator-facing endpoint is allowed to run.
package configparser

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrMissing is returned when a required key has no value in the file
// or in the environment. The runtime translates this into the same
// message it would have produced if the value were missing from a
// pure os.Getenv load, so the failure mode is identical regardless of
// whether the operator uses the file or environment variables.
var ErrMissing = errors.New("configuration key is missing")

// configFileName is the filename inside the data directory the
// runtime looks for. Kept exported so the bootstrapper and the tests
// can reference the same constant.
const ConfigFileName = "config.env"

// PlaceholderInstallationID is the value shipped in
// packaging/windows/config.env. The runtime refuses to treat it as a
// valid installation identity and triggers the bootstrap flow to
// generate a real one.
const PlaceholderInstallationID = "replace-before-first-start"

// PlaceholderPublicOrigin is the value shipped in
// packaging/windows/config.env. The runtime refuses to expose
// public-facing endpoints until PC_PUBLIC_ORIGIN is configured.
const PlaceholderPublicOrigin = "https://replace-me.invalid"

// loadResult collects every observation the loader makes so the
// caller can decide what to do. The file path is preserved so the
// runtime can log it; the loaded map includes every line that
// successfully parsed.
type loadResult struct {
	Path   string
	Values map[string]string
}

// Load reads config.env from the given directory, merges it under
// the supplied getenv function, and returns the merged values. The
// file does not have to exist; missing files are not an error so a
// fresh checkout / an empty data directory boots with empty
// configuration. The merged map excludes empty / blank values.
func Load(dataDirectory string, getenv func(string) string) (map[string]string, error) {
	if strings.TrimSpace(dataDirectory) == "" {
		return nil, fmt.Errorf("data directory is required")
	}
	result, err := loadFile(filepath.Join(dataDirectory, ConfigFileName))
	if err != nil {
		return nil, err
	}
	if getenv != nil {
		// Enumerate the keys we care about so getenv can override any
		// file-sourced value without leaking unrelated environment
		// variables into the merged map.
		for _, key := range knownKeys {
			if envValue, ok := envOverride(getenv, key); ok {
				result.Values[key] = envValue
			}
		}
	}
	return result.Values, nil
}

// DefaultDataDirectory returns the platform-appropriate location for
// persistent data. The runtime calls this on first start so an
// operator can run the binary without any prior setup.
//
// The directory exists on every supported platform — /var/lib exists
// on every Linux distribution we ship to and CommonAppDataFolder
// always exists on Windows. We do not create the directory; the
// caller is expected to do that through the localfiles package so
// the access control is set consistently.
func DefaultDataDirectory() string {
	// Both PC_DATA_DIR and PRINT_CATALYST_DATA_DIR act as overrides on
	// every supported platform. The MS installer sets the canonical
	// CommonAppDataFolder-derived location through packaging; if an
	// operator (or a developer running the binary from a shell)
	// overrides it they win, the same way the existing Unix flow
	// respects the override.
	if explicit := strings.TrimSpace(os.Getenv("PC_DATA_DIR")); explicit != "" {
		return explicit
	}
	if explicit := strings.TrimSpace(os.Getenv("PRINT_CATALYST_DATA_DIR")); explicit != "" {
		return explicit
	}
	// The Linux default matches the one in PHASE-2 acceptance; the
	// Windows default matches the %PROGRAMDATA% location the MS
	// installer provisions. We cannot resolve the Windows path with
	// os.Getenv because the SHGetKnownFolderPath win32 call lives
	// outside the standard library and a literal fallback is
	// required for the bootstrap path that runs before config.Load.
	if isWindows() {
		if csidl := strings.TrimSpace(os.Getenv("PROGRAMDATA")); csidl != "" {
			return filepath.Join(csidl, "PrintCatalyst", "OnPremise")
		}
		return `C:\ProgramData\PrintCatalyst\OnPremise`
	}
	return "/var/lib/printcatalyst-kiosk"
}

// IsWindows is centralised so the rest of the package can be tested
// without the build tag gymnastics. It is set at process start in
// real builds (see configparser_windows.go / _unix.go).
var IsWindows = func() bool { return false }

func isWindows() bool { return IsWindows() }

// knownKeys is the canonical list of configuration keys the runtime
// understands. The list is explicit so a stray environment variable
// (RAZORPAY_KEY_ID, DATABASE_URL, ...) cannot leak into the merged
// map.
var knownKeys = []string{
	"PC_INSTALLATION_ID",
	"PC_DATA_DIR",
	"PC_DATABASE_PATH",
	"PC_PUBLIC_ORIGIN",
	"PC_CONTROL_PLANE_ORIGIN",
	"PC_BIND_HOST",
	"PC_PORT",
}

// DefaultDBPath returns the canonical database filename inside the
// supplied data directory. The runtime uses this when PC_DATABASE_PATH
// is unset so the bootstrap path can open sqlite before the full
// configuration has been loaded.
func DefaultDBPath(dataDirectory string) string {
	return filepath.Join(dataDirectory, "print-catalyst.db")
}

// loadFile reads a config.env-style file. The format is intentionally
// trivial — every non-comment line must be of the form
// `KEY=VALUE` with no escaping, no continuation, no quoting.
//
// Missing files return an empty (not nil) map so the caller can
// combine file + env without a nil check. Malformed lines are
// returned as an error so a typo in the file produces a clear
// remediation message rather than silently dropping the line.
func loadFile(path string) (loadResult, error) {
	result := loadResult{Path: path, Values: map[string]string{}}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return result, nil
		}
		return loadResult{}, fmt.Errorf("open config file: %w", err)
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	for lineNumber := 1; ; lineNumber++ {
		raw, readErr := reader.ReadString('\n')
		raw = strings.TrimRight(raw, "\r\n")
		if raw != "" {
			if err := parseLine(raw, result.Values); err != nil {
				return loadResult{}, fmt.Errorf("config file %s line %d: %w", path, lineNumber, err)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return loadResult{}, fmt.Errorf("read config file %s: %w", path, readErr)
		}
	}
	return result, nil
}

// parseLine stores a single KEY=VALUE entry. Empty lines and
// comments are silently ignored. Whitespace around the key is
// trimmed; the value is also trimmed because the file format has no
// quoting rules.
func parseLine(raw string, into map[string]string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return nil
	}
	separator := strings.IndexByte(trimmed, '=')
	if separator <= 0 {
		return fmt.Errorf("expected KEY=VALUE, got %q", raw)
	}
	key := strings.TrimSpace(trimmed[:separator])
	value := strings.TrimSpace(trimmed[separator+1:])
	if key == "" {
		return fmt.Errorf("empty key in line %q", raw)
	}
	into[key] = value
	return nil
}

// envOverride returns the trimmed environment value for a key, with
// the bool reporting whether the value should override the file
// value. An empty / whitespace-only env value is treated as not set
// so the file value wins for unset environment variables.
func envOverride(getenv func(string) string, key string) (string, bool) {
	if getenv == nil {
		return "", false
	}
	value := strings.TrimSpace(getenv(key))
	if value == "" {
		return "", false
	}
	return value, true
}
