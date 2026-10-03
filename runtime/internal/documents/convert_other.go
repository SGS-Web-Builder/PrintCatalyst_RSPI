//go:build !windows

package documents

import "os/exec"

func hideConverter(c *exec.Cmd) {}
