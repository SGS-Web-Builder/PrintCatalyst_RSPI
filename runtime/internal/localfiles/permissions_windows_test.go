//go:build windows

package localfiles

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestDatabaseDirectoryRestrictsNewSQLiteSidecars(t *testing.T) {
	files, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path, err := files.PrepareDatabase("data.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	root, err := windows.GetNamedSecurityInfo(filepath.Dir(path), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(root.String(), "OICI") {
		t.Fatalf("directory lacks child inheritance: %s", root.String())
	}
	if err := os.WriteFile(path+"-wal", []byte("sidecar"), 0666); err != nil {
		t.Fatal(err)
	}
	child, err := windows.GetNamedSecurityInfo(path+"-wal", windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := child.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if dacl == nil || dacl.AceCount != 3 {
		t.Fatalf("sidecar inherited unexpected access: %s", child.String())
	}
	for _, forbidden := range []string{";;;WD)", ";;;BU)", ";;;AU)"} {
		if strings.Contains(child.String(), forbidden) {
			t.Fatalf("broad sidecar access: %s", child.String())
		}
	}
}

func assertRestrictedFile(t *testing.T, path string) {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("file DACL inherits permissions instead of remaining protected")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if dacl == nil || dacl.AceCount != 3 {
		t.Fatalf("protected DACL ACE count = %v, want 3", dacl)
	}
}
