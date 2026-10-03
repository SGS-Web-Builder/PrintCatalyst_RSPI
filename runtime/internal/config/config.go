package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/configparser"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/portalorigin"
)

// Getenv makes configuration loading deterministic and straightforward to test.
type Getenv func(string) string

// Sentinel errors. Callers can match against these to distinguish
// "configuration is missing" (the only case the bootstrap flow may
// resolve) from "configuration is malformed" or "configuration is
// correct but storage is not available", each of which must fail
// closed without invoking the bootstrap loop.
var (
	ErrMissing            = errors.New("configuration key is missing")
	ErrMalformed          = errors.New("configuration is malformed")
	ErrForbidden          = errors.New("configuration references a forbidden value")
	ErrStorageUnavailable = errors.New("data directory is unavailable")
)

// Classify inspects err and returns the sentinel that best describes
// the failure mode. Returns nil when err is nil or does not match
// any of the documented categories.
func Classify(err error) error {
	if err == nil {
		return nil
	}
	lowered := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lowered, "is required"):
		return ErrMissing
	case strings.Contains(lowered, "must be"), strings.Contains(lowered, "invalid"), strings.Contains(lowered, "must use https"), strings.Contains(lowered, "must be a"):
		return ErrMalformed
	case strings.Contains(lowered, "forbidden"), strings.Contains(lowered, "must never"), strings.Contains(lowered, "must not"):
		return ErrForbidden
	case strings.Contains(lowered, "data directory"), strings.Contains(lowered, "absolute"), strings.Contains(lowered, "inside pc_data_dir"):
		return ErrStorageUnavailable
	}
	return nil
}

// Config contains only installation-local settings and public control-plane coordinates.
// Platform payment credentials and remote application databases are deliberately excluded.
//
// Bootstrap is true when at least one of the placeholders the MSI
// installer ships with is still present in the configuration. The
// runtime uses this flag to refuse every endpoint except the
// protected setup surface so a fresh install cannot silently accept
// customer traffic with placeholder identities / origins.
type Config struct {
	InstallationID     string
	DataDirectory      string
	BindHost           string
	Port               int
	PublicOrigin       string
	ControlPlaneOrigin string
	DatabasePath       string
	Bootstrap          bool
}

var forbiddenSecrets = []string{
	"RAZORPAY_KEY_SECRET",
	"PLATFORM_RAZORPAY_KEY_SECRET",
	"RAZORPAY_WEBHOOK_SECRET",
}

// Load validates an installation configuration before any listener or database is opened.
//
// The runtime reads its configuration from two sources, merged in
// priority order:
//
//  1. Process environment variables.
//  2. The config.env file installed alongside the data directory
//     ([data directory]/config.env). This is the authoritative
//     source — environment variables exist for operator debugging.
//
// Placeholder values shipped by the MSI installer
// (replace-before-first-start, https://replace-me.invalid) do NOT
// cause Load to fail — that would create a bootstrap loop where the
// runtime refuses to start before the operator has a chance to use
// the local setup wizard. Instead Load returns Config.Bootstrap=true
// so the HTTP layer can refuse every endpoint except the setup
// surface.
func Load(getenv Getenv, dataDirectory string) (Config, error) {
	if strings.TrimSpace(dataDirectory) == "" {
		return Config{}, fmt.Errorf("data directory is required")
	}
	if err := assertNoForbiddenSecrets(getenv); err != nil {
		return Config{}, err
	}
	if !strings.ContainsAny(dataDirectory, `/\`) {
		return Config{}, fmt.Errorf("PC_DATA_DIR must be an absolute Windows or POSIX path")
	}

	// Load the file from the resolved data directory. File values
	// are written under the data directory by the runtime (or the
	// installer) and represent the canonical source. Environment
	// variables override the file values per the documented merge
	// order so a developer can run the binary with `--PC_PORT=9090`
	// without editing the file.
	fileValues, err := configparser.Load(dataDirectory, getenv)
	if err != nil {
		return Config{}, err
	}
	env := map[string]string{}
	for _, key := range []string{
		"PC_INSTALLATION_ID", "PC_DATA_DIR", "PC_DATABASE_PATH",
		"PC_PUBLIC_ORIGIN", "PC_CONTROL_PLANE_ORIGIN", "PC_BIND_HOST", "PC_PORT",
	} {
		if v, ok := fileValues[key]; ok {
			env[key] = v
		}
		if getenv != nil {
			if override := strings.TrimSpace(getenv(key)); override != "" {
				env[key] = override
			}
		}
	}
	env["PC_DATA_DIR"] = dataDirectory

	installationID := strings.TrimSpace(env["PC_INSTALLATION_ID"])
	if installationID == "" {
		return Config{}, fmt.Errorf("PC_INSTALLATION_ID is required")
	}
	if strings.EqualFold(installationID, configparser.PlaceholderInstallationID) {
		// Placeholder is OK during bootstrap; we will flag Config.Bootstrap
		// below so the server can refuse customer traffic.
	} else if !looksLikeIdentifier(installationID) {
		return Config{}, fmt.Errorf("PC_INSTALLATION_ID must be a 26-character ULID or a 64-character hex fingerprint generated by the runtime")
	}
	databasePath := strings.TrimSpace(env["PC_DATABASE_PATH"])
	if databasePath == "" {
		return Config{}, fmt.Errorf("PC_DATABASE_PATH is required")
	}
	if !containedPath(dataDirectory, databasePath) {
		return Config{}, fmt.Errorf("PC_DATABASE_PATH must be inside PC_DATA_DIR")
	}

	bindHost := strings.TrimSpace(env["PC_BIND_HOST"])
	if bindHost == "" {
		bindHost = "127.0.0.1"
	}
	if net.ParseIP(bindHost) == nil {
		return Config{}, fmt.Errorf("bind host must be an IP address")
	}

	portText := strings.TrimSpace(env["PC_PORT"])
	if portText == "" {
		portText = "8080"
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1024 || port > 65535 {
		return Config{}, fmt.Errorf("PC_PORT must be an integer from 1024 through 65535")
	}

	publicOrigin := strings.TrimSpace(env["PC_PUBLIC_ORIGIN"])
	if publicOrigin == "" {
		return Config{}, fmt.Errorf("PC_PUBLIC_ORIGIN is required")
	}
	if !strings.EqualFold(publicOrigin, configparser.PlaceholderPublicOrigin) {
		if _, err := portalorigin.Normalize(publicOrigin); err != nil {
			return Config{}, err
		}
	}
	controlPlaneOrigin := strings.TrimSpace(env["PC_CONTROL_PLANE_ORIGIN"])
	if controlPlaneOrigin == "" {
		return Config{}, fmt.Errorf("PC_CONTROL_PLANE_ORIGIN is required")
	}
	if _, err := httpsOrigin(controlPlaneOrigin, "PC_CONTROL_PLANE_ORIGIN", "Control-plane origin"); err != nil {
		return Config{}, err
	}

	bootstrap := strings.EqualFold(publicOrigin, configparser.PlaceholderPublicOrigin) ||
		strings.EqualFold(installationID, configparser.PlaceholderInstallationID)

	return Config{
		InstallationID: installationID, DataDirectory: dataDirectory,
		BindHost: bindHost, Port: port,
		PublicOrigin:       canonicalHttpsOrigin(publicOrigin),
		ControlPlaneOrigin: canonicalHttpsOrigin(controlPlaneOrigin),
		DatabasePath:       databasePath,
		Bootstrap:          bootstrap,
	}, nil
}

// Validate re-checks the configuration values against the same
// rules Load applies. It is used by the /api/v1/owner/config/reload
// endpoint so a freshly written config.env cannot move the runtime
// into an unsafe state without the operator seeing a 422 from the
// dashboard.
//
// Unlike Load, Validate also refuses placeholder values: a reload
// is by definition an attempt to leave bootstrap mode, so silently
// keeping the placeholder as a "valid" choice would defeat the
// purpose of the reload flow.
func Validate(getenv Getenv, dataDirectory string) (Config, error) {
	cfg, err := Load(getenv, dataDirectory)
	if err != nil {
		return Config{}, err
	}
	if cfg.Bootstrap {
		return Config{}, fmt.Errorf("configuration still references placeholder values; replace PC_INSTALLATION_ID and PC_PUBLIC_ORIGIN before reloading")
	}
	return cfg, nil
}

// canonicalHttpsOrigin normalises an HTTPS URL to a
// scheme://host origin string. When Load accepts a placeholder
// value it must still return a usable string for internal logging,
// so we pass the placeholder through unchanged rather than
// returning a zero value.
func canonicalHttpsOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return raw
	}
	return u.Scheme + "://" + u.Host
}

// looksLikeIdentifier accepts the two canonical installation-id
// shapes the runtime has ever emitted: the 26-character Crockford
// Base32 ULID, and the 64-character lowercase hex fingerprint the
// licensing service derives from the device public key. A hand-edited
// value that matches neither shape is rejected so an operator typo
// cannot quietly rot the installation identity.
func looksLikeIdentifier(candidate string) bool {
	if len(candidate) == 26 {
		for _, char := range candidate {
			if !((char >= '0' && char <= '9') || (char >= 'A' && char <= 'Z')) {
				return false
			}
		}
		return true
	}
	if len(candidate) == 64 {
		for _, char := range candidate {
			if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
				return false
			}
		}
		return true
	}
	return false
}

// ResolveDataDirectory returns the configured data directory or a
// platform-appropriate default. Splitting this out of Load keeps the
// bootstrap flow able to ask the same resolver for the canonical
// path before calling Load itself.
func ResolveDataDirectory(getenv Getenv) string {
	if getenv != nil {
		if explicit := strings.TrimSpace(getenv("PC_DATA_DIR")); explicit != "" {
			return explicit
		}
	}
	return configparser.DefaultDataDirectory()
}

func assertNoForbiddenSecrets(getenv Getenv) error {
	if getenv == nil {
		return nil
	}
	for _, key := range forbiddenSecrets {
		if strings.TrimSpace(getenv(key)) != "" {
			return fmt.Errorf("platform Razorpay secrets must never be present in an On-Premise installation")
		}
	}
	if strings.TrimSpace(getenv("PC_DATABASE_URL")) != "" || strings.TrimSpace(getenv("DATABASE_URL")) != "" {
		return fmt.Errorf("On-Premise must use its embedded local database; remote/SaaS database URLs are forbidden")
	}
	return nil
}

func httpsOrigin(raw, key, label string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("%s must be a valid URL", label)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("%s must use HTTPS", label)
	}
	if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("%s must be an HTTPS origin without credentials, path, query or fragment", label)
	}
	return "https://" + u.Host, nil
}

// containedPath performs a lexical containment check for both Windows and POSIX
// paths, regardless of which operating system validates the configuration.
func containedPath(directory, file string) bool {
	normalize := func(raw string) string {
		cleaned := path.Clean(strings.ReplaceAll(strings.TrimSpace(raw), `\`, "/"))
		return strings.TrimSuffix(strings.ToLower(cleaned), "/")
	}
	dir, candidate := normalize(directory), normalize(file)
	if dir == "." || candidate == "." || candidate == dir {
		return false
	}
	return strings.HasPrefix(candidate, dir+"/")
}
