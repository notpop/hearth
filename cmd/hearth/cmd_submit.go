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

func runSubmit(args []string) error {
	fs := flag.NewFlagSet("submit", flag.ExitOnError)
	bundlePath := fs.String("bundle", "", "path to a .hearth bundle for mTLS auth")
	addr := fs.String("coordinator", "", "coordinator address (overrides bundle)")
	kind := fs.String("kind", "", "job kind (required)")
	payload := fs.String("payload", "", "inline string payload")
	maxAttempts := fs.Int("max-attempts", 3, "maximum delivery attempts")
	leaseTTL := fs.Duration("lease-ttl", 30*time.Second, "lease TTL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *kind == "" {
		return fmt.Errorf("--kind is required")
	}
	if *bundlePath == "" {
		return fmt.Errorf("--bundle is required (run 'hearth enroll' to issue one)")
	}

	client, err := dialFromBundle(*bundlePath, *addr)
	if err != nil {
		return err
	}
	defer client.Close()

	id, err := client.SubmitJob(context.Background(), job.Spec{
		Kind:        *kind,
		Payload:     []byte(*payload),
		MaxAttempts: *maxAttempts,
		LeaseTTL:    *leaseTTL,
	})
	if err != nil {
		return err
	}
	fmt.Println(id)
	return nil
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
	return grpcadapter.Dial(contextNoCancel(), target, tlsCfg)
}

// contextNoCancel returns a fresh background context. Wrapped in a function
// so callers can grep for the intent.
func contextNoCancel() context.Context { return context.Background() }

// stderrf is a brief helper.
func stderrf(format string, a ...any) { fmt.Fprintf(os.Stderr, format, a...) }
