package fs_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"testing"

	blobfs "github.com/notpop/hearth/internal/adapter/blob/fs"
	"github.com/notpop/hearth/pkg/job"
)

func TestPutGetHas(t *testing.T) {
	s, err := blobfs.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	ctx := context.Background()
	data := bytes.Repeat([]byte("abc"), 4096)
	want := sha256.Sum256(data)

	ref, err := s.Put(ctx, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if ref.SHA256 != hex.EncodeToString(want[:]) {
		t.Errorf("sha = %q", ref.SHA256)
	}
	if ref.Size != int64(len(data)) {
		t.Errorf("size = %d", ref.Size)
	}

	ok, err := s.Has(ctx, ref)
	if err != nil || !ok {
		t.Errorf("Has: ok=%v err=%v", ok, err)
	}

	rc, err := s.Get(ctx, ref)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, data) {
		t.Errorf("payload mismatch")
	}
}

func TestGetMissing(t *testing.T) {
	s, _ := blobfs.Open(t.TempDir())
	_, err := s.Get(context.Background(), job.BlobRef{SHA256: "deadbeef"})
	if !errors.Is(err, blobfs.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestPutDuplicate(t *testing.T) {
	s, _ := blobfs.Open(t.TempDir())
	data := []byte("hello world")

	r1, err := s.Put(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Put 1: %v", err)
	}
	r2, err := s.Put(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Put 2: %v", err)
	}
	if r1 != r2 {
		t.Errorf("duplicate Put returned different refs: %+v vs %+v", r1, r2)
	}
}
