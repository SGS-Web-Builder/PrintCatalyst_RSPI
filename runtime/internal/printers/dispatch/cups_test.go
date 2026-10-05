package dispatch

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type cupsTransport func(*http.Request) (*http.Response, error)

func (f cupsTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func cupsReply(r *http.Request, attrs ...ippAttribute) *http.Response {
	request, _ := io.ReadAll(r.Body)
	var b bytes.Buffer
	b.Write([]byte{2, 0, 0, 0})
	b.Write(request[4:8])
	b.WriteByte(2)
	for _, a := range attrs {
		putAttribute(&b, a)
	}
	b.WriteByte(3)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/ipp"}}, Body: io.NopCloser(bytes.NewReader(b.Bytes()))}
}
func cupsRef() DocumentRef {
	return DocumentRef{Prepared: true, OrderID: "order", LineID: "line", MIMEType: "application/pdf", PaperSize: "A4", ColourMode: "monochrome", Sides: "two-sided-long-edge", Copies: 2, PagesPerSheet: 1}
}
func TestCUPSPreparedSubmissionContractAndNoRetry(t *testing.T) {
	calls := 0
	b := NewCUPSBackend()
	b.client.Transport = cupsTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "http://127.0.0.1:631/printers/shop" {
			t.Fatal(r.URL)
		}
		if r.GetBody != nil {
			t.Fatal("replayable request")
		}
		data, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(data))
		for _, expected := range []string{"application/pdf", "two-sided-long-edge", "iso_a4_210x297mm", "print-color-mode", "ipp-attribute-fidelity", "%PDF-fixture"} {
			if !bytes.Contains(data, []byte(expected)) {
				t.Fatal("missing option", expected)
			}
		}
		if bytes.Contains(data, []byte("page-ranges")) {
			t.Fatal("page selection applied twice")
		}
		return cupsReply(r, ippNumber("job-id", 42)), nil
	})
	id, err := b.Submit(context.Background(), "shop", []byte("%PDF-fixture"), cupsRef())
	if err != nil || !strings.HasPrefix(id, "cups:42:") {
		t.Fatal(id, err)
	}
	bad := cupsRef()
	bad.Prepared = false
	if _, err = b.Submit(context.Background(), "shop", []byte("%PDF-fixture"), bad); err == nil {
		t.Fatal("raw source allowed")
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	b.client.Transport = cupsTransport(func(r *http.Request) (*http.Response, error) { calls++; return nil, errors.New("response lost") })
	if _, err = b.Submit(context.Background(), "shop", []byte("%PDF-fixture"), cupsRef()); err == nil {
		t.Fatal("lost response accepted")
	}
	if calls != 2 {
		t.Fatal("submission retried")
	}
}
func TestCUPSStatusIdentityAndStates(t *testing.T) {
	for _, tc := range []struct {
		state int
		want  string
	}{{3, "pending"}, {4, "blocked"}, {5, "processing"}, {6, "blocked"}, {7, "review"}, {8, "review"}, {9, "completed"}, {99, "review"}} {
		t.Run(tc.want+string(rune(tc.state+'0')), func(t *testing.T) {
			b := NewCUPSBackend()
			title := jobTitle("order", "line")
			b.client.Transport = cupsTransport(func(r *http.Request) (*http.Response, error) {
				state := ippNumber("job-state", tc.state)
				state.tag = 0x23
				return cupsReply(r, state, ippText("job-name", 0x42, title), ippText("job-printer-uri", 0x45, "ipp://localhost:631/printers/shop")), nil
			})
			state, _, err := b.JobProgress(context.Background(), "shop", "cups:42:"+title, "order")
			if err != nil || state != tc.want {
				t.Fatal(state, err)
			}
			state, _, err = b.JobProgress(context.Background(), "shop", "cups:42:"+title, "other")
			if err != nil || state != "review" {
				t.Fatal("foreign job accepted")
			}
		})
	}
}
func TestCUPSRejectMalformedResponse(t *testing.T) {
	for _, data := range [][]byte{nil, {2, 0, 0, 0, 0, 0, 0, 1}, {2, 0, 0, 0, 0, 0, 0, 2, 3}, {2, 0, 0, 1, 0, 0, 0, 1, 3}, {2, 0, 0, 0, 0, 0, 0, 1, 2, 0x21, 255, 255}} {
		if _, err := parseCUPS(data, 1); err == nil {
			t.Fatal("malformed response accepted")
		}
	}
	b := NewCUPSBackend()
	b.client.Transport = cupsTransport(func(r *http.Request) (*http.Response, error) {
		reply := cupsReply(r, ippNumber("job-id", 42))
		data, _ := io.ReadAll(reply.Body)
		binary.BigEndian.PutUint32(data[4:8], 999)
		reply.Body = io.NopCloser(bytes.NewReader(data))
		return reply, nil
	})
	if _, err := b.Submit(context.Background(), "shop", []byte("%PDF-fixture"), cupsRef()); err == nil {
		t.Fatal("mismatched response accepted")
	}
}

func TestCUPSTrayIsEncodedAsMediaCollection(t *testing.T) {
	b := NewCUPSBackend()
	ref := cupsRef()
	ref.Tray = "tray-2"
	ref.PaperSize = "A6"
	b.client.Transport = cupsTransport(func(r *http.Request) (*http.Response, error) {
		data, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(data))
		var expected bytes.Buffer
		for _, a := range []ippAttribute{{name: "media-col", tag: 0x34}, ippText("", 0x4a, "media-size-name"), ippText("", 0x44, "iso_a6_105x148mm"), ippText("", 0x4a, "media-source"), ippText("", 0x44, "tray-2"), {tag: 0x37}} {
			putAttribute(&expected, a)
		}
		if !bytes.Contains(data, expected.Bytes()) {
			t.Fatal("tray collection not encoded")
		}
		return cupsReply(r, ippNumber("job-id", 43)), nil
	})
	if _, err := b.Submit(context.Background(), "shop", []byte("%PDF-fixture"), ref); err != nil {
		t.Fatal(err)
	}
}
