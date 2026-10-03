package localfiles

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// PrepareDatabase protects the dedicated database directory before SQLite can
// create credentials or WAL sidecars. Existing files are hardened on upgrades.
// On Windows, children inherit access only for SYSTEM, Administrators and the
// runtime identity. This must not be called on a general-purpose shared folder.
func (f *Files) PrepareDatabase(name string) (string, error) {
	if name != filepath.Base(name) {
		return "", ErrUnsafePath
	}
	path, err := f.resolve(name)
	if err != nil {
		return "", err
	}
	if err := protectDirectory(f.root); err != nil {
		return "", fmt.Errorf("protect database directory: %w", err)
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		target := path + suffix
		info, err := os.Lstat(target)
		if err == nil && !info.Mode().IsRegular() {
			return "", ErrUnsafePath
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if errors.Is(err, os.ErrNotExist) && suffix != "" {
			continue
		}
		flags := os.O_RDWR
		if errors.Is(err, os.ErrNotExist) {
			flags |= os.O_CREATE | os.O_EXCL
		}
		file, err := os.OpenFile(target, flags, 0600)
		if err != nil {
			return "", err
		}
		protectErr := protectFile(target, file)
		closeErr := file.Close()
		if protectErr != nil {
			return "", protectErr
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	return path, nil
}
