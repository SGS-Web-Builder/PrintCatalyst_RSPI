//go:build !windows && !linux

package licensegate

import "errors"

func protect([]byte) ([]byte, error) {
	return nil, errors.New("licence device protection is unsupported on this platform")
}
func unprotect([]byte) ([]byte, error) {
	return nil, errors.New("licence device protection is unsupported on this platform")
}
