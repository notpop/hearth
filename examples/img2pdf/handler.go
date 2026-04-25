// Package img2pdf is a reference Hearth Handler that converts a set of
// input images (PNG/JPEG) into a single PDF, emitted as an output blob.
//
// It is intentionally small and dependency-light; the heavy lifting lives
// in github.com/go-pdf/fpdf. Use this as a template for your own handlers:
//
//   1. Implement Kind() returning a unique routing key.
//   2. Implement Handle(ctx, in) which:
//        - reads input blobs via in.Blobs[i].Open()
//        - does the work (cancel on ctx.Done())
//        - returns one or more OutputBlobs
//   3. Register the handler with workerrt.New(...) in your main package.
//
// See examples/img2pdf/cmd/img2pdf-worker for the runnable binary.
package img2pdf

import (
	"context"
	"fmt"
	"image"
	_ "image/jpeg" // register decoder
	_ "image/png"  // register decoder
	"io"
	"os"
	"path/filepath"

	"github.com/go-pdf/fpdf"

	"github.com/notpop/hearth/pkg/worker"
)

// Kind is the routing key. Workers advertise this via Handler.Kind().
const Kind = "img2pdf"

// Handler implements worker.Handler.
type Handler struct{}

// Kind returns the routing key for this handler.
func (Handler) Kind() string { return Kind }

// Handle reads each input blob, decodes it as an image, and writes a PDF
// where each image occupies its own A4-portrait page (auto-fit). The PDF is
// returned as in.Output.Blobs[0].
//
// The handler honours ctx cancellation between pages but cannot interrupt
// a single Decode call — image decoding for typical photos completes in
// milliseconds, so this is acceptable.
func (Handler) Handle(ctx context.Context, in worker.Input) (worker.Output, error) {
	if len(in.Blobs) == 0 {
		return worker.Output{}, fmt.Errorf("img2pdf: no input blobs")
	}

	pdf := fpdf.New("P", "mm", "A4", "")
	pageW, pageH := pdf.GetPageSize()
	const margin = 10.0

	for i, b := range in.Blobs {
		if err := ctx.Err(); err != nil {
			return worker.Output{}, err
		}
		if err := addImagePage(pdf, b, i, pageW-2*margin, pageH-2*margin, margin); err != nil {
			return worker.Output{}, fmt.Errorf("page %d: %w", i, err)
		}
	}

	tmp, err := os.CreateTemp("", "hearth-img2pdf-*.pdf")
	if err != nil {
		return worker.Output{}, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := pdf.OutputAndClose(tmp); err != nil {
		return worker.Output{}, err
	}
	if err := pdf.Error(); err != nil {
		return worker.Output{}, err
	}

	out, err := os.Open(tmpPath)
	if err != nil {
		return worker.Output{}, err
	}
	stat, _ := out.Stat()
	return worker.Output{
		Blobs: []worker.OutputBlob{{Reader: closingReader{File: out}, Size: stat.Size()}},
	}, nil
}

// addImagePage downloads one input blob, decodes it, registers it with the
// PDF (using a per-call alias to prevent collisions), and emits a fitted page.
func addImagePage(pdf *fpdf.Fpdf, b worker.InputBlob, index int, fitW, fitH, margin float64) error {
	rc, err := b.Open()
	if err != nil {
		return fmt.Errorf("open input: %w", err)
	}
	defer rc.Close()

	tmp, err := os.CreateTemp("", "hearth-img-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmp, rc); err != nil {
		_ = tmp.Close()
		return err
	}
	_ = tmp.Close()

	imgType, err := detectImageType(tmpPath)
	if err != nil {
		return err
	}

	alias := fmt.Sprintf("img%d", index)
	opts := fpdf.ImageOptions{ImageType: imgType, ReadDpi: true}
	pdf.RegisterImageOptionsReader(alias, opts, mustOpen(tmpPath))

	pdf.AddPage()
	info := pdf.GetImageInfo(alias)
	if info == nil {
		return fmt.Errorf("image %d: register failed", index)
	}
	w, h := info.Width(), info.Height()
	scale := fitW / w
	if h*scale > fitH {
		scale = fitH / h
	}
	dw, dh := w*scale, h*scale
	pdf.ImageOptions(alias, margin, margin, dw, dh, false, opts, 0, "")
	return nil
}

func detectImageType(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	_, format, err := image.DecodeConfig(f)
	if err != nil {
		// Fall back to extension if Decode can't guess (rare).
		switch ext := filepath.Ext(path); ext {
		case ".png":
			return "png", nil
		case ".jpg", ".jpeg":
			return "jpg", nil
		default:
			return "", fmt.Errorf("unknown image format")
		}
	}
	if format == "jpeg" {
		return "jpg", nil
	}
	return format, nil
}

func mustOpen(path string) *os.File {
	f, err := os.Open(path)
	if err != nil {
		panic(err)
	}
	return f
}

// closingReader makes *os.File satisfy io.Reader while letting the runtime
// take ownership of closing.
type closingReader struct{ *os.File }

func (c closingReader) Read(p []byte) (int, error) { return c.File.Read(p) }
