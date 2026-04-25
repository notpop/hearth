// Package fs is a filesystem-backed content-addressable BlobStore.
//
// Layout under root:
//
//	<root>/objects/<sha[:2]>/<sha>
//	<root>/tmp/<random>            # in-flight uploads, atomically renamed
//
// Splitting by the first 2 hex chars avoids one giant directory and is
// enough for hundreds of millions of blobs without performance hits.
package fs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/notpop/hearth/pkg/job"
)

// ErrNotFound is returned when the blob is not present.
var ErrNotFound = errors.New("blob/fs: not found")

// Store is the filesystem BlobStore.
type Store struct {
	root string
}

// Open prepares the directory layout under root.
func Open(root string) (*Store, error) {
	for _, sub := range []string{"objects", "tmp"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			return nil, err
		}
	}
	return &Store{root: root}, nil
}

func (s *Store) objectsPath(sha string) string {
	return filepath.Join(s.root, "objects", sha[:2], sha)
}

// Put streams r into a temp file, computes SHA-256 as it goes, and atomically
// renames into place. Returns the resulting BlobRef.
func (s *Store) Put(ctx context.Context, r io.Reader) (job.BlobRef, error) {
	tmpName, err := randomName()
	if err != nil {
		return job.BlobRef{}, err
	}
	tmpPath := filepath.Join(s.root, "tmp", tmpName)
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return job.BlobRef{}, err
	}
	cleanup := func() {
		_ = f.Close()
		_ = os.Remove(tmpPath)
	}

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), ctxReader{ctx: ctx, r: r})
	if err != nil {
		cleanup()
		return job.BlobRef{}, err
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return job.BlobRef{}, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return job.BlobRef{}, err
	}

	sha := hex.EncodeToString(h.Sum(nil))
	dst := s.objectsPath(sha)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		_ = os.Remove(tmpPath)
		return job.BlobRef{}, err
	}

	if _, err := os.Stat(dst); err == nil {
		// Already present — duplicate write is a no-op.
		_ = os.Remove(tmpPath)
		return job.BlobRef{SHA256: sha, Size: n}, nil
	}

	if err := os.Rename(tmpPath, dst); err != nil {
		_ = os.Remove(tmpPath)
		return job.BlobRef{}, fmt.Errorf("rename: %w", err)
	}
	return job.BlobRef{SHA256: sha, Size: n}, nil
}

// Get opens a reader for ref. The caller must close it.
func (s *Store) Get(_ context.Context, ref job.BlobRef) (io.ReadCloser, error) {
	if len(ref.SHA256) < 2 {
		return nil, ErrNotFound
	}
	f, err := os.Open(s.objectsPath(ref.SHA256))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return f, err
}

// Has reports whether ref is locally present.
func (s *Store) Has(_ context.Context, ref job.BlobRef) (bool, error) {
	if len(ref.SHA256) < 2 {
		return false, nil
	}
	_, err := os.Stat(s.objectsPath(ref.SHA256))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// ctxReader applies context cancellation to a Reader.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (cr ctxReader) Read(p []byte) (int, error) {
	if err := cr.ctx.Err(); err != nil {
		return 0, err
	}
	return cr.r.Read(p)
}

func randomName() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
