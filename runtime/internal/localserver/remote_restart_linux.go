//go:build linux

package localserver

import (
	"errors"
	"os"
	"syscall"
)

func remoteRestartAvailable() bool { return os.Getenv("INVOCATION_ID") != "" }
func remoteRestart() error {
	if !remoteRestartAvailable() {
		return errors.New("not managed by systemd")
	}
	return syscall.Kill(os.Getpid(), syscall.SIGTERM)
}
