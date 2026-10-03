//go:build windows

package documents

import (
	"os/exec"
	"syscall"
)

func hideConverter(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
