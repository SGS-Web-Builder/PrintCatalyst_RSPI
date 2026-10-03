package localfiles

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var ErrUnsafePath = errors.New("path escapes the protected data directory")

type CapacityError struct {
	Available uint64
	Required  uint64
}

func (e *CapacityError) Error() string {
	return fmt.Sprintf("insufficient local storage: %d bytes available, %d required", e.Available, e.Required)
}

type capacityProbe func(string) (uint64, error)
type Option func(*Files)

func WithMinimumFreeBytes(bytes uint64) Option {
	return func(files *Files) { files.minimumFreeBytes = bytes }
}

func withCapacityProbe(probe capacityProbe) Option {
	return func(files *Files) { files.capacity = probe }
}

type Files struct {
	root             string
	minimumFreeBytes uint64
	capacity         capacityProbe
}

func New(root string, options ...Option) (*Files, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("data root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve data root: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("create data root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("resolve data root links: %w", err)
	}
	files := &Files{root: filepath.Clean(resolved), minimumFreeBytes: 64 << 20, capacity: availableBytes}
	for _, option := range options {
		option(files)
	}
	return files, nil
}

func (f *Files) WriteAtomic(relative string, source io.Reader) (returnError error) {
	target, err := f.resolve(relative)
	if err != nil {
		return err
	}
	available, err := f.capacity(f.root)
	if err != nil {
		return fmt.Errorf("check local storage capacity: %w", err)
	}
	if available < f.minimumFreeBytes {
		return &CapacityError{Available: available, Required: f.minimumFreeBytes}
	}
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create document directory: %w", err)
	}
	if err := f.rejectSymlinks(parent); err != nil {
		return err
	}
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return ErrUnsafePath
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect document target: %w", err)
	}

	temporary, err := os.CreateTemp(parent, ".pc-write-*")
	if err != nil {
		return fmt.Errorf("create temporary document: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() {
		_ = temporary.Close()
		if returnError != nil {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := protectFile(temporaryName, temporary); err != nil {
		return fmt.Errorf("protect temporary document: %w", err)
	}
	if _, err := io.Copy(temporary, source); err != nil {
		return fmt.Errorf("write temporary document: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary document: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary document: %w", err)
	}
	if err := atomicReplace(temporaryName, target); err != nil {
		return fmt.Errorf("commit document: %w", err)
	}
	if err := syncDirectory(parent); err != nil {
		return fmt.Errorf("sync document directory: %w", err)
	}
	return nil
}

func (f *Files) Open(relative string) (*os.File, error) {
	target, err := f.resolve(relative)
	if err != nil {
		return nil, err
	}
	if err := f.rejectSymlinks(filepath.Dir(target)); err != nil {
		return nil, err
	}
	info, err := os.Lstat(target)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrUnsafePath
	}
	return os.Open(target)
}

func (f *Files) Remove(relative string) error {
	target, err := f.resolve(relative)
	if err != nil {
		return err
	}
	if err := f.rejectSymlinks(filepath.Dir(target)); err != nil {
		return err
	}
	return os.Remove(target)
}

// MkdirAll creates a directory hierarchy under the protected data root, creating
// parent directories as needed. The relative path is validated through resolve so
// it cannot escape the data root. Directories are created with 0755 permissions.
func (f *Files) MkdirAll(relative string) error {
	target, err := f.resolve(relative)
	if err != nil {
		return err
	}
	return os.MkdirAll(target, 0755)
}

// DataRoot returns the absolute path to the protected data directory. Callers that
// need to construct absolute paths for use with os.WriteFile, os.ReadFile or
// similar stdlib functions must use this method and not hard-code paths.
func (f *Files) DataRoot() string { return f.root }

func (f *Files) resolve(relative string) (string, error) {
	normalized := strings.ReplaceAll(strings.TrimSpace(relative), `\`, "/")
	if normalized == "" || filepath.IsAbs(normalized) || strings.HasPrefix(normalized, "/") || (len(normalized) >= 2 && normalized[1] == ':') {
		return "", ErrUnsafePath
	}
	target := filepath.Clean(filepath.Join(f.root, filepath.FromSlash(normalized)))
	relativeToRoot, err := filepath.Rel(f.root, target)
	if err != nil || relativeToRoot == ".." || strings.HasPrefix(relativeToRoot, ".."+string(filepath.Separator)) {
		return "", ErrUnsafePath
	}
	return target, nil
}

func (f *Files) rejectSymlinks(targetDirectory string) error {
	relative, err := filepath.Rel(f.root, targetDirectory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ErrUnsafePath
	}
	current := f.root
	if relative == "." {
		return nil
	}
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspect protected path: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrUnsafePath
		}
	}
	return nil
}
