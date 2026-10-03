package documents

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"rsc.io/pdf"
	"testing"
)

func TestImageCompositionPagesAndPaper(t *testing.T) {
	var b bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 60, 40))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	png.Encode(&b, img)
	sources := [][]byte{b.Bytes(), b.Bytes(), b.Bytes(), b.Bytes(), b.Bytes()}
	for _, n := range []int{1, 2, 4, 6, 9, 12, 16} {
		for _, orientation := range []string{"portrait", "landscape"} {
			body, err := ComposeImages(sources, n, "A4", orientation, 65)
			if err != nil {
				t.Fatal(err)
			}
			pages, err := CountPages("application/pdf", body)
			if err != nil || pages != (5+n-1)/n {
				t.Fatalf("layout %d: pages %d, %v", n, pages, err)
			}
			reader, err := pdf.NewReader(bytes.NewReader(body), int64(len(body)))
			if err != nil {
				t.Fatal(err)
			}
			box := reader.Page(1).V.Key("MediaBox")
			w, h := box.Index(2).Float64(), box.Index(3).Float64()
			if (w > h) != (orientation == "landscape") {
				t.Fatal("wrong page orientation")
			}
			count := reader.Page(1).V.Key("Resources").Key("XObject").Keys()
			want := n
			if want > 5 {
				want = 5
			}
			if len(count) != want {
				t.Fatal("missing images")
			}
		}
	}
	for _, n := range []int{0, 3, 100} {
		if _, err := ComposeImages(sources, n, "A4", "portrait", 50); err == nil {
			t.Fatal("invalid layout accepted")
		}
	}
	if _, err := ComposeImages([][]byte{[]byte("bad"), b.Bytes()}, 2, "A4", "portrait", 50); err == nil {
		t.Fatal("invalid image accepted")
	}
}

func TestCompositionReflow(t *testing.T) {
	var b bytes.Buffer
	png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 40, 30)))
	original, err := ComposeImages([][]byte{b.Bytes(), b.Bytes()}, 2, "A4", "portrait", 65)
	if err != nil {
		t.Fatal(err)
	}
	landscape, err := ReflowImages(original, "A4", "landscape")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := pdf.NewReader(bytes.NewReader(landscape), int64(len(landscape)))
	if err != nil {
		t.Fatal(err)
	}
	box := reader.Page(1).V.Key("MediaBox")
	if box.Index(2).Float64() <= box.Index(3).Float64() {
		t.Fatal("landscape expected")
	}
	if reader.NumPage() != 1 {
		t.Fatal("reflow changed page count")
	}
	portrait, err := ReflowImages(landscape, "A4", "portrait")
	if err != nil {
		t.Fatal(err)
	}
	if pages, err := CountPages("application/pdf", portrait); err != nil || pages != 1 {
		t.Fatal("portrait reflow failed", err)
	}
	plain := []byte("ordinary PDF")
	unchanged, err := ReflowImages(plain, "A4", "landscape")
	if err != nil || !bytes.Equal(plain, unchanged) {
		t.Fatal("ordinary document changed")
	}
	if _, err := ReflowImages([]byte("%PrintCatalystImages {invalid}\n"), "A4", "landscape"); err == nil {
		t.Fatal("invalid recipe accepted")
	}
}
