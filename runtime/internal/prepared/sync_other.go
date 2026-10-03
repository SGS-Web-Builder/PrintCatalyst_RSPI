//go:build !linux

package prepared

import "os"

// Non-Linux builds support developer tests, not production durability claims.
func syncDirectory(root *os.Root, path string) error { return nil }

func privateDirectory(info os.FileInfo) error { return nil }
