package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	grpcadapter "github.com/notpop/hearth/internal/adapter/transport/grpc"
	"github.com/notpop/hearth/internal/security/bundle"
	"github.com/notpop/hearth/internal/security/pki"
	"github.com/notpop/hearth/pkg/job"
)

// stringSlice is a flag.Value that accepts repeated `--blob <path>` flags
// and accumulates them into a slice.
type stringSlice []string

func (s *stringSlice) String() string     { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error { *s = append(*s, v); return nil }

func runSubmit(args []string) error {
	fs := flag.NewFlagSet("submit", flag.ExitOnError)
	bundlePath := fs.String("bundle", "", "path to a .hearth bundle for mTLS auth")
	addr := fs.String("coordinator", "", "coordinator address (overrides bundle)")
	kind := fs.String("kind", "", "job kind (required)")
	payload := fs.String("payload", "", "inline string payload")
	var blobs stringSlice
	fs.Var(&blobs, "blob", "path to a file to attach as an input blob (repeatable)")
	maxAttempts := fs.Int("max-attempts", 3, "maximum delivery attempts")
	leaseTTL := fs.Duration("lease-ttl", 30*time.Second, "lease TTL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *kind == "" {
		return fmt.Errorf("--kind is required")
	}

	ctx := context.Background()
	client, err := dialFromBundle(*bundlePath, *addr)
	if err != nil {
		return err
	}
	defer client.Close()

	// Stream every --blob into the coordinator's blob store first, collect
	// the resulting refs, then submit the job referencing them.
	refs, err := uploadBlobs(ctx, client, blobs)
	if err != nil {
		return fmt.Errorf("upload blobs: %w", err)
	}

	id, err := client.SubmitJob(ctx, job.Spec{
		Kind:        *kind,
		Payload:     []byte(*payload),
		Blobs:       refs,
		MaxAttempts: *maxAttempts,
		LeaseTTL:    *leaseTTL,
	})
	if err != nil {
		return err
	}
	fmt.Println(id)
	return nil
}

// uploadBlobs is a thin loop over PutBlob; it exists so the test surface
// stays small (a fake client can be exercised against it directly).
func uploadBlobs(ctx context.Context, client *grpcadapter.Client, paths []string) ([]job.BlobRef, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	refs := make([]job.BlobRef, 0, len(paths))
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", p, err)
		}
		ref, err := client.PutBlob(ctx, f)
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("put %s: %w", p, err)
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// dialFromBundle is the shared "open mTLS gRPC connection" helper used by
// every CLI client command. If bundlePath is empty it tries, in order,
// $HEARTH_BUNDLE, ./.hearth/admin.hearth, and ~/.hearth/admin.hearth.
func dialFromBundle(bundlePath, addrOverride string) (*grpcadapter.Client, error) {
	resolved, err := resolveBundlePath(bundlePath)
	if err != nil {
		return nil, err
	}
	bundlePath = resolved
	b, err := bundle.ReadFile(bundlePath)
	if err != nil {
		return nil, err
	}
	target := addrOverride
	if target == "" {
		if len(b.Manifest.CoordinatorAddrs) == 0 {
			return nil, fmt.Errorf("no coordinator address: pass --coordinator or include addrs in the bundle")
		}
		target = b.Manifest.CoordinatorAddrs[0]
	}
	host := strings.SplitN(target, ":", 2)[0]
	tlsCfg, err := pki.ClientTLSConfig(b.CACertPEM, b.ClientCertPEM, b.ClientKeyPEM, host)
	if err != nil {
		return nil, err
	}
	return grpcadapter.Dial(context.Background(), target, tlsCfg)
}
