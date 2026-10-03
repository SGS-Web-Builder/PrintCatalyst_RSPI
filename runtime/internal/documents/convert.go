package documents

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Word conversion is optional and entirely local. PDFs/images need no Office installation.
func officeExecutable() string {
	if runtime.GOOS == "linux" {
		for _, p := range []string{"/usr/bin/libreoffice", "/usr/bin/soffice"} {
			if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0111 != 0 {
				return p
			}
		}
		return ""
	}
	for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")} {
		if root == "" {
			continue
		}
		p := filepath.Join(root, "LibreOffice", "program", "soffice.exe")
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}
func DOCXAvailable() bool { return officeExecutable() != "" }
func ConvertDOCX(ctx context.Context, body []byte) ([]byte, error) {
	exe := officeExecutable()
	if exe == "" {
		return nil, fmt.Errorf("Word conversion requires LibreOffice on the shop PC; upload a PDF instead")
	}
	dir, err := os.MkdirTemp("", "pc-word-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	src := filepath.Join(dir, "document.docx")
	if err = os.WriteFile(src, body, 0600); err != nil {
		return nil, err
	}
	profile := (&url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(filepath.Join(dir, "profile")), "/")}).String()
	convertCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(convertCtx, exe, "-env:UserInstallation="+profile, "--headless", "--nologo", "--nodefault", "--norestore", "--convert-to", "pdf:writer_pdf_Export", "--outdir", dir, src)
	hideConverter(cmd)
	if err = cmd.Run(); err != nil {
		return nil, fmt.Errorf("Word conversion failed: %w", err)
	}
	result, err := os.ReadFile(filepath.Join(dir, "document.pdf"))
	if err != nil {
		return nil, fmt.Errorf("Word conversion produced no PDF: %w", err)
	}
	if len(result) > MaxFileSize {
		return nil, fmt.Errorf("converted PDF exceeds 50 MB")
	}
	return result, nil
}
