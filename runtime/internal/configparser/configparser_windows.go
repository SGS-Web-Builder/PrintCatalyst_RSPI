//go:build windows

package configparser

func init() { IsWindows = func() bool { return true } }
