//go:build linux

package devicekeys

import (
	"errors"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
)

func Load() (*Vault, error) {
	dir := os.Getenv("CREDENTIALS_DIRECTORY")
	if !filepath.IsAbs(dir) {
		return nil, errors.New("systemd device-sealing-key credential is required")
	}
	seed, err := readCredential(filepath.Join(dir, "device-sealing-key"))
	if err != nil {
		return nil, err
	}
	defer clear(seed)
	// Fixed kernel-provided identity. Do not accept an environment substitute.
	serial, err := os.ReadFile("/sys/firmware/devicetree/base/serial-number")
	if err != nil {
		return nil, errors.New("Raspberry Pi board serial is unavailable")
	}
	return New(seed, string(serial))
}

// LoadKioskCredential is a separate credential, never the installation master key.
// A future trusted touchscreen launcher may receive this credential without
// gaining access to licence or pickup encryption keys.
func LoadKioskCredential() ([]byte, error) {
	dir := os.Getenv("CREDENTIALS_DIRECTORY")
	if !filepath.IsAbs(dir) {
		return nil, errors.New("systemd kiosk-client-key credential is required")
	}
	return readCredential(filepath.Join(dir, "kiosk-client-key"))
}

func readCredential(path string) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("cannot open systemd device-sealing-key credential")
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		return nil, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0077 != 0 || (st.Uid != 0 && st.Uid != uint32(os.Geteuid())) || st.Size != 32 {
		return nil, errors.New("device credential must be a private regular 32-byte file owned by root or the service")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 33))
	if err != nil || len(raw) != 32 {
		clear(raw)
		return nil, errors.New("invalid device credential size")
	}
	return raw, nil
}
