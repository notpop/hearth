// Package bundle defines the single-file enrollment artifact a worker
// needs to join a coordinator: a tar.gz containing the CA cert, the
// worker's signed certificate + private key, and a manifest with the
// coordinator endpoint(s).
//
// The format is intentionally simple. No encryption: the bundle is a
// secret and must be moved over a trusted medium (USB stick, SD card).
// Adding passphrase encryption is a one-line gpg invocation away if the
// user wants belt-and-braces.
package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// FileExt is the customary extension. Bundles are valid tar.gz regardless.
const FileExt = ".hearth"

// FormatVersion is the on-disk schema version. Bumped on incompatible
// changes; readers reject newer than they understand.
const FormatVersion = "1"

// Manifest is the JSON-encoded metadata file inside the bundle.
type Manifest struct {
	FormatVersion    string    `json:"format_version"`
	HearthVersion    string    `json:"hearth_version"`
	WorkerID         string    `json:"worker_id"`
	CoordinatorAddrs []string  `json:"coordinator_addrs,omitempty"`
	DiscoveryName    string    `json:"discovery_name,omitempty"`
	IssuedAt         time.Time `json:"issued_at"`
}

// Bundle is the in-memory representation of an enrollment artifact.
type Bundle struct {
	Manifest      Manifest
	CACertPEM     []byte
	ClientCertPEM []byte
	ClientKeyPEM  []byte
}

// Validate performs the structural checks Read also does.
func (b Bundle) Validate() error {
	if b.Manifest.FormatVersion != FormatVersion {
		return fmt.Errorf("bundle: unsupported format_version %q (want %q)", b.Manifest.FormatVersion, FormatVersion)
	}
	if b.Manifest.WorkerID == "" {
		return errors.New("bundle: empty worker_id")
	}
	if len(b.CACertPEM) == 0 {
		return errors.New("bundle: missing ca.crt")
	}
	if len(b.ClientCertPEM) == 0 {
		return errors.New("bundle: missing client.crt")
	}
	if len(b.ClientKeyPEM) == 0 {
		return errors.New("bundle: missing client.key")
	}
	return nil
}

// Write serialises b as gzipped tar to w.
func Write(w io.Writer, b Bundle) error {
	if err := b.Validate(); err != nil {
		return err
	}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	manifestJSON, err := json.MarshalIndent(b.Manifest, "", "  ")
	if err != nil {
		_ = tw.Close()
		_ = gz.Close()
		return err
	}

	files := []struct {
		name string
		data []byte
		mode int64
	}{
		{"manifest.json", manifestJSON, 0o644},
		{"ca.crt", b.CACertPEM, 0o644},
		{"client.crt", b.ClientCertPEM, 0o644},
		{"client.key", b.ClientKeyPEM, 0o600},
	}
	for _, f := range files {
		hdr := &tar.Header{
			Name:    f.name,
			Size:    int64(len(f.data)),
			Mode:    f.mode,
			ModTime: b.Manifest.IssuedAt,
			Format:  tar.FormatPAX,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			_ = tw.Close()
			_ = gz.Close()
			return err
		}
		if _, err := tw.Write(f.data); err != nil {
			_ = tw.Close()
			_ = gz.Close()
			return err
		}
	}
	if err := tw.Close(); err != nil {
		_ = gz.Close()
		return err
	}
	return gz.Close()
}

// WriteFile is a convenience wrapper that writes b to path with mode 0600
// (the bundle contains a private key).
func WriteFile(path string, b Bundle) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := Write(f, b); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	return f.Close()
}

// Read parses a bundle from r.
func Read(r io.Reader) (Bundle, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return Bundle{}, fmt.Errorf("bundle: gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var b Bundle
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Bundle{}, fmt.Errorf("bundle: tar: %w", err)
		}
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, tr); err != nil {
			return Bundle{}, err
		}
		switch hdr.Name {
		case "manifest.json":
			if err := json.Unmarshal(buf.Bytes(), &b.Manifest); err != nil {
				return Bundle{}, fmt.Errorf("bundle: manifest: %w", err)
			}
		case "ca.crt":
			b.CACertPEM = buf.Bytes()
		case "client.crt":
			b.ClientCertPEM = buf.Bytes()
		case "client.key":
			b.ClientKeyPEM = buf.Bytes()
		default:
			// Forward-compat: unknown entries are ignored. Concrete schema
			// changes will live behind a higher FormatVersion.
		}
	}
	if err := b.Validate(); err != nil {
		return Bundle{}, err
	}
	return b, nil
}

// ReadFile is the file-path companion to Read.
func ReadFile(path string) (Bundle, error) {
	f, err := os.Open(path)
	if err != nil {
		return Bundle{}, err
	}
	defer f.Close()
	return Read(f)
}
