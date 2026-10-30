package documents_test

import (
	"bytes"
	"context"
	"errors"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/documents"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localfiles"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/store"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDocumentIntakeHonorsDiskReserve(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(root, "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.DB().Exec(`INSERT INTO orders(id,share_token,status,currency,currency_minor_units,total_minor,customer_name,customer_phone,created_at,updated_at) VALUES('draft','token','pending_payment','INR',2,0,'','',1,1)`)
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="files"; filename="test.pdf"`)
	header.Set("Content-Type", "application/pdf")
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	part.Write(minimalPDF(1))
	writer.Close()
	request := httptest.NewRequest("POST", "/", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if err = request.ParseMultipartForm(1 << 20); err != nil {
		t.Fatal(err)
	}
	defer request.MultipartForm.RemoveAll()
	files, err := localfiles.New(filepath.Join(root, "files"), localfiles.WithMinimumFreeBytes(^uint64(0)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = documents.New(files, db.DB()).Save(ctx, "draft", request.MultipartForm.File["files"][0])
	var capacity *localfiles.CapacityError
	if !errors.As(err, &capacity) {
		t.Fatalf("expected disk reserve failure, got %v", err)
	}
	var count int
	if err = db.DB().QueryRow("SELECT COUNT(*) FROM documents").Scan(&count); err != nil || count != 0 {
		t.Fatal("metadata survived failed write", count, err)
	}
	files, err = localfiles.New(filepath.Join(root, "files"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := documents.New(files, db.DB()).Save(ctx, "draft", request.MultipartForm.File["files"][0])
	if err != nil {
		t.Fatal(err)
	}
	f, err := files.Open(doc.StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := io.ReadAll(f)
	f.Close()
	if err != nil || !bytes.Equal(saved, minimalPDF(1)) {
		t.Fatal("saved document differs", err)
	}
	_ = filepath.Walk(filepath.Join(root, "files"), func(path string, info os.FileInfo, e error) error {
		if e == nil && !info.IsDir() && strings.HasPrefix(info.Name(), ".pc-write-") {
			t.Errorf("orphan temporary file: %s", path)
		}
		return e
	})
}
