// Command img2pdf-worker is a runnable Hearth worker that registers the
// img2pdf handler. It demonstrates the pattern users follow to build their
// own worker binaries: import hearth's runtime + adapters, register a
// handler, run.
//
// Usage:
//
//	img2pdf-worker --bundle /path/to/worker.hearth [--coordinator <addr>]
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	grpcadapter "github.com/notpop/hearth/internal/adapter/transport/grpc"
	"github.com/notpop/hearth/internal/app"
	"github.com/notpop/hearth/internal/app/workerrt"
	"github.com/notpop/hearth/internal/security/bundle"
	"github.com/notpop/hearth/internal/security/pki"
	"github.com/notpop/hearth/pkg/worker"

	"github.com/notpop/hearth/examples/img2pdf"
)

const version = "img2pdf-worker/0.0.0-dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	bundlePath := flag.String("bundle", "", "path to .hearth enrollment bundle (required)")
	addr := flag.String("coordinator", "", "override coordinator address (default: from bundle)")
	leaseTTL := flag.Duration("lease-ttl", 60*time.Second, "lease TTL requested per job")
	flag.Parse()

	if *bundlePath == "" {
		return fmt.Errorf("--bundle is required")
	}
	b, err := bundle.ReadFile(*bundlePath)
	if err != nil {
		return fmt.Errorf("read bundle: %w", err)
	}

	target := *addr
	if target == "" {
		if len(b.Manifest.CoordinatorAddrs) == 0 {
			return fmt.Errorf("no coordinator address in bundle; pass --coordinator")
		}
		target = b.Manifest.CoordinatorAddrs[0]
	}
	host := strings.SplitN(target, ":", 2)[0]

	tlsCfg, err := pki.ClientTLSConfig(b.CACertPEM, b.ClientCertPEM, b.ClientKeyPEM, host)
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	client, err := grpcadapter.Dial(ctx, target, tlsCfg)
	if err != nil {
		return err
	}
	defer client.Close()

	hostname, _ := os.Hostname()
	if _, err := client.RegisterWorker(ctx, app.WorkerInfo{
		ID:       b.Manifest.WorkerID,
		Hostname: hostname,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		Version:  version,
		Kinds:    []string{img2pdf.Kind},
		LastSeen: time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("register: %w", err)
	}

	rt := workerrt.New(workerrt.Options{
		WorkerID: b.Manifest.WorkerID,
		Handlers: []worker.Handler{img2pdf.Handler{}},
		Client:   client,
		LeaseTTL: *leaseTTL,
		Logger:   slog.Default(),
	})

	fmt.Printf("img2pdf worker connected\n  worker_id   = %s\n  coordinator = %s\n", b.Manifest.WorkerID, target)
	return rt.Run(ctx)
}
