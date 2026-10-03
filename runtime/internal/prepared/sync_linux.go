//go:build linux

package prepared

import (
	"errors"
	"os"
	"syscall"
)

func privateDirectory(info os.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0077 != 0 {
		return errors.New("prepared directory must be private and owned by the service")
	}
	return nil
}

func syncDirectory(root *os.Root, path string) error {
	f, err := root.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
