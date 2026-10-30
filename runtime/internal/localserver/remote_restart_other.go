//go:build !linux

package localserver

import "errors"

func remoteRestartAvailable() bool { return false }
func remoteRestart() error         { return errors.New("Linux systemd service required") }
