//go:build windows

package dispatch

import (
	"context"
	"fmt"
	"golang.org/x/sys/windows"
	"runtime"
	"unsafe"
)

type queueInfo4 struct {
	Name, Server *uint16
	Attributes   uint32
}

func (b *WinspoolBackend) Queues(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	enum := spool.NewProc("EnumPrintersW")
	var needed, count uint32
	ok, _, err := enum.Call(6, 0, 4, 0, 0, uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&count)))
	if ok == 0 && err != windows.ERROR_INSUFFICIENT_BUFFER {
		return nil, fmt.Errorf("enumerate Windows printers: %w", err)
	}
	if needed == 0 {
		return []string{}, nil
	}
	for attempt := 0; attempt < 3; attempt++ {
		buf := make([]byte, needed)
		ok, _, err = enum.Call(6, 0, 4, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&count)))
		if ok == 0 {
			if err == windows.ERROR_INSUFFICIENT_BUFFER {
				continue
			}
			return nil, fmt.Errorf("enumerate Windows printers: %w", err)
		}
		size := unsafe.Sizeof(queueInfo4{})
		if uintptr(count)*size > uintptr(len(buf)) {
			return nil, fmt.Errorf("invalid printer enumeration response")
		}
		names := make([]string, 0, count)
		for i := uint32(0); i < count; i++ {
			info := (*queueInfo4)(unsafe.Pointer(&buf[uintptr(i)*size]))
			if info.Name != nil {
				names = append(names, windows.UTF16PtrToString(info.Name))
			}
		}
		runtime.KeepAlive(buf)
		return names, nil
	}
	return nil, fmt.Errorf("printer list changed repeatedly; refresh and try again")
}
