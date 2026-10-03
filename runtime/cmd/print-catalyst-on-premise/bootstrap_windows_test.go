//go:build windows

package main

import "testing"

// The bootstrap contract is identical on Windows; the windows-only
// test stub exists so `go test ./...` works on a Windows runner
// without dragging in unix-specific file-system expectations. See
// bootstrap_unix_test.go for the actual regression suite.

func placeholderWindowsBootstrap(t *testing.T) {}
