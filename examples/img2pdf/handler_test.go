package img2pdf_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"strings"
	"testing"

	"github.com/notpop/hearth/examples/img2pdf"
	"github.com/notpop/hearth/pkg/job"
	"github.com/notpop/hearth/pkg/worker"
)

// makePNG returns a tiny solid-colour PNG.
func makePNG(t *testing.T, w, h int, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func TestHandlerProducesPDF(t *testing.T) {
	pages := [][]byte{
		makePNG(t, 64, 64, color.RGBA{R: 200, A: 255}),
		makePNG(t, 64, 64, color.RGBA{G: 200, A: 255}),
	}

	in := worker.Input{
		JobID:   "j1",
		Kind:    img2pdf.Kind,
		Payload: nil,
	}
	for i, b := range pages {
		b := b
		in.Blobs = append(in.Blobs, worker.InputBlob{
			Ref:  job.BlobRef{SHA256: byteIndex(i)},
			Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil },
		})
	}

	out, err := (img2pdf.Handler{}).Handle(context.Background(), in)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(out.Blobs) != 1 {
		t.Fatalf("blobs = %d, want 1", len(out.Blobs))
	}
	data, err := io.ReadAll(out.Blobs[0].Reader)
	if err != nil {
		t.Fatalf("read pdf: %v", err)
	}
	if !bytes.HasPrefix(data, []byte("%PDF-")) {
		t.Errorf("output does not look like a PDF: %q", data[:min(8, len(data))])
	}
}

func TestHandlerKind(t *testing.T) {
	if k := (img2pdf.Handler{}).Kind(); k != img2pdf.Kind {
		t.Errorf("Kind = %q", k)
	}
}

func TestHandlerErrorsOnNoBlobs(t *testing.T) {
	_, err := (img2pdf.Handler{}).Handle(context.Background(), worker.Input{})
	if err == nil || !strings.Contains(err.Error(), "no input blobs") {
		t.Errorf("err = %v", err)
	}
}

func TestHandlerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (img2pdf.Handler{}).Handle(ctx, worker.Input{
		Blobs: []worker.InputBlob{{
			Ref:  job.BlobRef{SHA256: "x"},
			Open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("")), nil },
		}},
	})
	if err == nil {
		t.Errorf("expected ctx error")
	}
}

func byteIndex(i int) string {
	return string(rune('a' + i))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
