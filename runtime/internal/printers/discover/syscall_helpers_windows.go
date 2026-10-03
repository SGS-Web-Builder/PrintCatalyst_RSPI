//go:build windows

package discover

import (
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// utf16Ptr allocates a UTF-16 (LPCWSTR) equivalent of the supplied Go
// string and returns it as a *uint16 suitable for passing into Win32
// spooler calls. The caller is responsible for releasing the buffer
// with freeUTF16 once the call returns.
func utf16Ptr(s string) (*uint16, error) {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		return nil, err
	}
	return (*uint16)(unsafe.Pointer(p)), nil
}

// freeUTF16 releases a UTF-16 pointer allocated via utf16Ptr. The
// implementation calls LocalFree because that is the underlying
// allocator syscall.UTF16PtrFromString uses on Windows; calling
// syscall.FreeString (which only exists on POSIX) would leak.
func freeUTF16(p *uint16) {
	if p == nil {
		return
	}
	_, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(p)))
}

// utf16ZeroToString reads a NUL-terminated UTF-16 buffer starting at
// p and returns the corresponding Go string with the terminator
// stripped. p may be nil; an empty string is returned in that case.
func utf16ZeroToString(p *uint16) string {
	if p == nil {
		return ""
	}
	// Locate the terminator.
	n := 0
	for ptr := p; *ptr != 0; ptr = (*uint16)(unsafe.Pointer(uintptr(unsafe.Pointer(ptr)) + 2)) {
		n++
		if n > 4096 {
			break // defensive: avoid runaway on corrupt pointers
		}
	}
	if n == 0 {
		return ""
	}
	buf := make([]uint16, n)
	src := unsafe.Pointer(p)
	for i := 0; i < n; i++ {
		buf[i] = *(*uint16)(unsafe.Pointer(uintptr(src) + uintptr(i*2)))
	}
	return string(utf16.Decode(buf))
}
