//go:build windows

package localfiles

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func availableBytes(path string) (uint64, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var available uint64
	if err := windows.GetDiskFreeSpaceEx(pointer, &available, nil, nil); err != nil {
		return 0, err
	}
	return available, nil
}

func atomicReplace(source, target string) error {
	sourcePointer, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	targetPointer, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(sourcePointer, targetPointer, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func protectFile(path string, _ *os.File) error {
	return protectPath(path, "")
}

func protectDirectory(path string) error {
	return protectPath(path, "OICI")
}

func protectPath(path, inheritance string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("read current Windows identity: %w", err)
	}
	currentSID := user.User.Sid.String()
	descriptor, err := windows.SecurityDescriptorFromString(
		"D:P(A;" + inheritance + ";FA;;;SY)(A;" + inheritance + ";FA;;;BA)(A;" + inheritance + ";FA;;;" + currentSID + ")",
	)
	if err != nil {
		return fmt.Errorf("create protected Windows descriptor: %w", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read protected Windows DACL: %w", err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	); err != nil {
		return fmt.Errorf("apply protected Windows DACL: %w", err)
	}
	return nil
}

func syncDirectory(string) error { return nil }
