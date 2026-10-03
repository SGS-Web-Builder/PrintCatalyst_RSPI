// Package prepared stores already-rendered PDFs and their frozen print plan.
// It never renders, marks a pickup ready, or submits work to a printer.
package prepared

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const MaxPDFBytes = 50 << 20
const MaxOrderBytes = 512 << 20
const maxManifest = 1 << 20

// Settings describe the final PDF: source page selection, rotation and N-up
// composition must already be baked into it. Copies and sides remain spool options.
type Settings struct {
	Paper  string `json:"paper"`
	Tray   string `json:"tray"`
	Colour string `json:"colour"`
	Sides  string `json:"sides"`
	Copies int    `json:"copies"`
}
type Input struct {
	LineID   string
	Queue    string
	Invoice  bool
	Settings Settings
	PDF      []byte
}
type Job struct {
	LineID   string   `json:"line_id"`
	Queue    string   `json:"queue"`
	Invoice  bool     `json:"invoice"`
	Settings Settings `json:"settings"`
	File     string   `json:"file"`
	SHA256   string   `json:"sha256"`
	Size     int      `json:"size"`
}
type Manifest struct {
	Version int    `json:"version"`
	OrderID string `json:"order_id"`
	Jobs    []Job  `json:"jobs"`
}
type Store struct{ root *os.Root }

// Open requires a dedicated existing private service-owned directory.
func Open(path string) (*Store, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("prepared directory must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("prepared directory must not be a symlink")
	}
	if err = privateDirectory(info); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}
func (s *Store) Close() error { return s.root.Close() }
func digest(b []byte) string  { v := sha256.Sum256(b); return hex.EncodeToString(v[:]) }
func validDigest(v string) bool {
	b, e := hex.DecodeString(v)
	return e == nil && len(b) == 32 && strings.ToLower(v) == v
}
func identifier(v string) bool {
	return len(v) > 0 && len(v) <= 256 && !strings.ContainsAny(v, "\x00\r\n")
}
func validSettings(v Settings) bool {
	return identifier(v.Paper) && len(v.Tray) <= 256 && !strings.ContainsAny(v.Tray, "\x00\r\n") && (v.Colour == "monochrome" || v.Colour == "color") && (v.Sides == "one-sided" || v.Sides == "two-sided-long-edge" || v.Sides == "two-sided-short-edge") && v.Copies >= 1 && v.Copies <= 999
}
func pdf(b []byte) bool {
	return len(b) >= 5 && len(b) <= MaxPDFBytes && bytes.HasPrefix(b, []byte("%PDF-"))
}

// Publish atomically names a complete bundle by its canonical manifest digest.
// Repeating an identical preparation reuses the bundle only after verification.
// PDF syntax/render validation belongs to the renderer, not this storage layer.
func (s *Store) Publish(order string, inputs []Input) (string, error) {
	if !identifier(order) || len(inputs) == 0 || len(inputs) > 100 {
		return "", errors.New("invalid prepared order")
	}
	manifest := Manifest{Version: 1, OrderID: order}
	total := 0
	seen := map[string]bool{}
	for i, in := range inputs {
		if !identifier(in.LineID) || seen[in.LineID] || !identifier(in.Queue) || !validSettings(in.Settings) || !pdf(in.PDF) {
			return "", errors.New("invalid prepared job")
		}
		seen[in.LineID] = true
		total += len(in.PDF)
		if total > MaxOrderBytes {
			return "", errors.New("prepared order exceeds size limit")
		}
		manifest.Jobs = append(manifest.Jobs, Job{in.LineID, in.Queue, in.Invoice, in.Settings, fmt.Sprintf("%03d.pdf", i), digest(in.PDF), len(in.PDF)})
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	if len(encoded) > maxManifest {
		return "", errors.New("manifest exceeds size limit")
	}
	id := digest(encoded)
	if _, err = s.root.Lstat(id); err == nil {
		_, _, err = s.Load(order, id)
		return id, err
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	tmp := ".preparing-" + hex.EncodeToString(nonce)
	if err = s.root.Mkdir(tmp, 0700); err != nil {
		return "", err
	}
	defer s.root.RemoveAll(tmp)
	for i, job := range manifest.Jobs {
		// Verify again after writing: a caller changing its buffer cannot publish
		// bytes that disagree with the manifest computed above.
		if err = s.write(filepath.Join(tmp, job.File), inputs[i].PDF); err != nil {
			return "", err
		}
		b, e := s.read(filepath.Join(tmp, job.File), MaxPDFBytes)
		if e != nil {
			return "", e
		}
		if digest(b) != job.SHA256 {
			return "", errors.New("PDF changed during preparation")
		}
	}
	if err = s.write(filepath.Join(tmp, "manifest.json"), encoded); err != nil {
		return "", err
	}
	if err = syncDirectory(s.root, tmp); err != nil {
		return "", err
	}
	if err = s.root.Rename(tmp, id); err != nil {
		// A concurrent identical publisher may have won the atomic rename.
		if _, _, verifyErr := s.Load(order, id); verifyErr != nil {
			return "", err
		}
	}
	if err = syncDirectory(s.root, "."); err != nil {
		return "", err
	}
	return id, nil
}
func (s *Store) write(name string, b []byte) error {
	f, err := s.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func (s *Store) read(name string, limit int) ([]byte, error) {
	info, err := s.root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > int64(limit) {
		return nil, errors.New("invalid prepared file")
	}
	f, err := s.root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > limit {
		return nil, errors.New("prepared file exceeds limit")
	}
	return b, nil
}

// Load verifies the entire order before returning any bytes. Submit these exact
// returned bytes, never reopen original upload paths after verification.
func (s *Store) Load(order, id string) (Manifest, [][]byte, error) {
	fail := func() (Manifest, [][]byte, error) {
		return Manifest{}, nil, errors.New("prepared bundle is missing or invalid")
	}
	if !identifier(order) || !validDigest(id) {
		return fail()
	}
	info, err := s.root.Lstat(id)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fail()
	}
	encoded, err := s.read(filepath.Join(id, "manifest.json"), maxManifest)
	if err != nil || digest(encoded) != id {
		return fail()
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(encoded))
	dec.DisallowUnknownFields()
	if dec.Decode(&m) != nil || dec.Decode(new(any)) != io.EOF || m.Version != 1 || m.OrderID != order || len(m.Jobs) == 0 || len(m.Jobs) > 100 {
		return fail()
	}
	canonical, err := json.Marshal(m)
	if err != nil || !bytes.Equal(canonical, encoded) {
		return fail()
	}
	total := 0
	seen := map[string]bool{}
	data := make([][]byte, 0, len(m.Jobs))
	for i, j := range m.Jobs {
		if !identifier(j.LineID) || seen[j.LineID] || !identifier(j.Queue) || !validSettings(j.Settings) || j.File != fmt.Sprintf("%03d.pdf", i) || !validDigest(j.SHA256) || j.Size < 5 || j.Size > MaxPDFBytes {
			return fail()
		}
		total += j.Size
		if total > MaxOrderBytes {
			return fail()
		}
		seen[j.LineID] = true
		b, err := s.read(filepath.Join(id, j.File), MaxPDFBytes)
		if err != nil || len(b) != j.Size || digest(b) != j.SHA256 || !pdf(b) {
			return fail()
		}
		data = append(data, b)
	}
	return m, data, nil
}
