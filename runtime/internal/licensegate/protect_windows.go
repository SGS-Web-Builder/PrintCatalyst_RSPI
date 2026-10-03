//go:build windows

package licensegate

import (
	"errors"
	"golang.org/x/sys/windows"
	"unsafe"
)

func protect(raw []byte) ([]byte, error)   { return crypt(raw, true) }
func unprotect(raw []byte) ([]byte, error) { return crypt(raw, false) }
func crypt(raw []byte, encrypt bool) ([]byte, error) {
	if len(raw) == 0 {
		return nil, errors.New("empty protected key")
	}
	in := windows.DataBlob{Size: uint32(len(raw)), Data: &raw[0]}
	var out windows.DataBlob
	var err error
	if encrypt {
		err = windows.CryptProtectData(&in, nil, nil, 0, nil, 1, &out)
	} else {
		err = windows.CryptUnprotectData(&in, nil, nil, 0, nil, 1, &out)
	}
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, int(out.Size))...), nil
}
