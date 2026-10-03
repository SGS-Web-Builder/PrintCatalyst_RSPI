//go:build windows

package dispatch

import (
	"context"
	"fmt"
	"golang.org/x/sys/windows"
	"runtime"
	"strconv"
	"strings"
	"unsafe"
)

type jobInfo1 struct {
	ID                                                     uint32
	Printer, Machine, User, Document, Datatype, StatusText *uint16
	Status, Priority, Position, TotalPages, PagesPrinted   uint32
	Submitted                                              windows.Systemtime
}

func (b *WinspoolBackend) JobProgress(ctx context.Context, queue, jobID, orderID string) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	id, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimPrefix(jobID, "winspool-retained-job-"), "winspool-job-"), 10, 32)
	if err != nil {
		return "review", "Job identifier unavailable; confirm output at the counter.", nil
	}
	name, err := windows.UTF16PtrFromString(queue)
	if err != nil {
		return "", "", err
	}
	var handle windows.Handle
	ok, _, err := spool.NewProc("OpenPrinterW").Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&handle)), 0)
	if ok == 0 {
		return "", "", fmt.Errorf("open printer: %w", err)
	}
	defer spool.NewProc("ClosePrinter").Call(uintptr(handle))
	get := spool.NewProc("GetJobW")
	var needed uint32
	ok, _, err = get.Call(uintptr(handle), uintptr(id), 1, 0, 0, uintptr(unsafe.Pointer(&needed)))
	if ok == 0 && err == windows.ERROR_INVALID_PARAMETER {
		return "review", "Job has left the Windows queue. Confirm printed output, then mark done.", nil
	}
	if ok == 0 && err != windows.ERROR_INSUFFICIENT_BUFFER {
		return "", "", fmt.Errorf("read print job: %w", err)
	}
	if needed < uint32(unsafe.Sizeof(jobInfo1{})) {
		return "", "", fmt.Errorf("invalid print job response")
	}
	buf := make([]byte, needed)
	ok, _, err = get.Call(uintptr(handle), uintptr(id), 1, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&needed)))
	if ok == 0 {
		if err == windows.ERROR_INVALID_PARAMETER {
			return "review", "Job has left the Windows queue. Confirm printed output, then mark done.", nil
		}
		return "", "", fmt.Errorf("read print job: %w", err)
	}
	info := (*jobInfo1)(unsafe.Pointer(&buf[0]))
	if windows.UTF16PtrToString(info.Document) != "Print Catalyst "+orderID {
		return "review", "Windows job identifier was reused. Confirm the original output at the counter.", nil
	}
	state, detail := windowsJobState(info.Status)
	if info.StatusText != nil && state != "completed" {
		detail = windows.UTF16PtrToString(info.StatusText)
	}
	runtime.KeepAlive(buf)
	return state, detail, nil
}

func windowsJobState(flags uint32) (string, string) {
	switch {
	case flags&(0x4|0x100) != 0:
		return "review", "Job deleted from Windows. Check output before marking done or printing again."
	case flags&(0x2|0x20|0x40|0x200|0x400) != 0:
		return "blocked", "Printer needs attention: check paper, connection and Windows queue."
	case flags&0x80 != 0:
		return "completed", "Windows reports the job printed."
	case flags&0x1 != 0:
		return "pending", "Print job is paused in Windows."
	case flags&0x8 != 0:
		return "processing", "Windows is spooling the document."
	case flags&(0x10|0x1000) != 0:
		return "printing", "Windows is printing or delivering the job to the printer."
	default:
		return "pending", "Waiting in the Windows printer queue."
	}
}

// ReleaseCompleted removes only our retention flag after completion is saved.
// Releasing a job does not restart or reprint it.
func (b *WinspoolBackend) ReleaseCompleted(ctx context.Context, queue, jobID, orderID string) error {
	if !strings.HasPrefix(jobID, "winspool-retained-job-") {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	id, err := strconv.ParseUint(strings.TrimPrefix(jobID, "winspool-retained-job-"), 10, 32)
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(queue)
	if err != nil {
		return err
	}
	var handle windows.Handle
	ok, _, err := spool.NewProc("OpenPrinterW").Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&handle)), 0)
	if ok == 0 {
		return err
	}
	defer spool.NewProc("ClosePrinter").Call(uintptr(handle))
	var needed uint32
	get := spool.NewProc("GetJobW")
	_, _, err = get.Call(uintptr(handle), uintptr(id), 1, 0, 0, uintptr(unsafe.Pointer(&needed)))
	if err == windows.ERROR_INVALID_PARAMETER {
		return nil
	}
	if err != windows.ERROR_INSUFFICIENT_BUFFER || needed < uint32(unsafe.Sizeof(jobInfo1{})) {
		return fmt.Errorf("read retained job: %v", err)
	}
	buf := make([]byte, needed)
	ok, _, err = get.Call(uintptr(handle), uintptr(id), 1, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&needed)))
	if ok == 0 {
		if err == windows.ERROR_INVALID_PARAMETER {
			return nil
		}
		return err
	}
	info := (*jobInfo1)(unsafe.Pointer(&buf[0]))
	if windows.UTF16PtrToString(info.Document) != "Print Catalyst "+orderID {
		return nil
	}
	runtime.KeepAlive(buf)
	ok, _, err = spool.NewProc("SetJobW").Call(uintptr(handle), uintptr(id), 0, 0, 9) // JOB_CONTROL_RELEASE
	if ok == 0 {
		return err
	}
	return nil
}
