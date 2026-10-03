//go:build !windows

package localfiles

import (
	"os"
	"syscall"
)

func availableBytes(path string) (uint64, error) {
	var statistics syscall.Statfs_t
	if err := syscall.Statfs(path, &statistics); err != nil {
		return 0, err
	}
	return uint64(statistics.Bavail) * uint64(statistics.Bsize), nil
}

func atomicReplace(source, target string) error { return os.Rename(source, target) }

func protectFile(_ string, file *os.File) error { return file.Chmod(0o600) }

func protectDirectory(path string) error { return os.Chmod(path, 0o700) }

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
