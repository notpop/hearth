package fs_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	blobfs "github.com/notpop/hearth/internal/adapter/blob/fs"
	"github.com/notpop/hearth/pkg/job"
)

func TestOpenCreatesDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), "deep", "nested", "root")
	if _, err := blobfs.Open(root); err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, sub := range []string{"objects", "tmp"} {
		if _, err := os.Stat(filepath.Join(root, sub)); err != nil {
			t.Errorf("missing %s: %v", sub, err)
		}
	}
}

func TestHasShortRefReturnsFalse(t *testing.T) {
	s, _ := blobfs.Open(t.TempDir())
	ok, err := s.Has(context.Background(), job.BlobRef{SHA256: "x"})
	if err != nil || ok {
		t.Errorf("ok=%v err=%v", ok, err)
	}
}

func TestGetShortRefReturnsNotFound(t *testing.T) {
	s, _ := blobfs.Open(t.TempDir())
	_, err := s.Get(context.Background(), job.BlobRef{SHA256: ""})
	if !errors.Is(err, blobfs.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("io failure") }

func TestPutPropagatesReadError(t *testing.T) {
	s, _ := blobfs.Open(t.TempDir())
	if _, err := s.Put(context.Background(), errReader{}); err == nil {
		t.Errorf("expected error from broken reader")
	}
}

func TestPutRespectsContext(t *testing.T) {
	s, _ := blobfs.Open(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Put(ctx, bytes.NewReader([]byte("data"))); err == nil {
		t.Errorf("expected ctx.Err()")
	}
}

func TestPutDuplicateLeavesObjectIntact(t *testing.T) {
	s, _ := blobfs.Open(t.TempDir())
	data := []byte("payload")

	r1, err := s.Put(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Put 1: %v", err)
	}

	rc, err := s.Get(context.Background(), r1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, data) {
		t.Errorf("first read mismatch")
	}

	if _, err := s.Put(context.Background(), bytes.NewReader(data)); err != nil {
		t.Fatalf("Put 2: %v", err)
	}

	rc, err = s.Get(context.Background(), r1)
	if err != nil {
		t.Fatalf("Get after dup: %v", err)
	}
	got, _ = io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, data) {
		t.Errorf("post-dup read mismatch")
	}
}
