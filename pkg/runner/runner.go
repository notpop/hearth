// Package runner is the simplest path to a working Hearth worker for
// external (OSS-consumer) projects.
//
// External Go projects cannot import packages under github.com/notpop/hearth/internal/...
// (Go enforces this). So everything an external worker needs to wire up
// (bundle reading, mTLS, gRPC dial, registration, runtime loop) is hidden
// behind RunWorker. Bring your own pkg/worker.Handler and call:
//
//	runner.RunWorker(ctx, "/path/to/worker.hearth", myHandler{})
//
// For more control (custom logging, custom transport, multi-stage setup),
// see the lower-level Options form below.
package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"time"

	grpcadapter "github.com/notpop/hearth/internal/adapter/transport/grpc"
	"github.com/notpop/hearth/internal/app"
	"github.com/notpop/hearth/internal/app/workerrt"
	"github.com/notpop/hearth/internal/security/bundle"
	"github.com/notpop/hearth/internal/security/pki"
	"github.com/notpop/hearth/pkg/worker"
)

// Handler is re-exported from pkg/worker so callers can do everything with a
// single import path. It is exactly worker.Handler.
type Handler = worker.Handler

// RunWorker is the one-call worker entrypoint. It:
//
//   1. reads the .hearth bundle at bundlePath
//   2. dials the coordinator over mTLS using the bundle's CA + client cert
//   3. registers the worker, advertising the kinds covered by handlers
//   4. runs the handler loop until ctx is cancelled
//
// At least one handler is required.
func RunWorker(ctx context.Context, bundlePath string, handlers ...Handler) error {
	return Run(ctx, Options{
		BundlePath: bundlePath,
		Handlers:   handlers,
	})
}

// Options configures a Run with more control than RunWorker exposes.
type Options struct {
	// BundlePath is the path to the .hearth enrollment bundle.
	BundlePath string
	// Handlers — at least one is required. Each must have a unique Kind().
	Handlers []Handler
	// AddrOverride forces a specific coordinator address. Empty means use
	// the bundle's CoordinatorAddrs[0].
	AddrOverride string
	// Version reported when registering with the coordinator. Optional.
	Version string
	// LeaseTTL requested per leased job. Default: 60s.
	LeaseTTL time.Duration
	// PollTimeout for Lease long-polls. Default: 30s.
	PollTimeout time.Duration
	// HeartbeatPeriod is how often the runtime extends a lease. Default:
	// LeaseTTL / 3 (min 1s).
	HeartbeatPeriod time.Duration
	// Logger. Default: slog.Default().
	Logger *slog.Logger
}

// Run is the configurable form of RunWorker.
func Run(ctx context.Context, opt Options) error {
	if opt.BundlePath == "" {
		return errors.New("runner: BundlePath is required")
	}
	if len(opt.Handlers) == 0 {
		return errors.New("runner: at least one handler is required")
	}

	b, err := bundle.ReadFile(opt.BundlePath)
	if err != nil {
		return fmt.Errorf("read bundle: %w", err)
	}

	target := opt.AddrOverride
	if target == "" {
		if len(b.Manifest.CoordinatorAddrs) == 0 {
			return fmt.Errorf("no coordinator address in bundle; set Options.AddrOverride")
		}
		target = b.Manifest.CoordinatorAddrs[0]
	}
	host := strings.SplitN(target, ":", 2)[0]

	tlsCfg, err := pki.ClientTLSConfig(b.CACertPEM, b.ClientCertPEM, b.ClientKeyPEM, host)
	if err != nil {
		return err
	}

	client, err := grpcadapter.Dial(ctx, target, tlsCfg)
	if err != nil {
		return fmt.Errorf("dial coordinator: %w", err)
	}
	defer client.Close()

	hostname, _ := os.Hostname()
	kinds := make([]string, 0, len(opt.Handlers))
	for _, h := range opt.Handlers {
		kinds = append(kinds, h.Kind())
	}
	if _, err := client.RegisterWorker(ctx, app.WorkerInfo{
		ID:       b.Manifest.WorkerID,
		Hostname: hostname,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		Kinds:    kinds,
		Version:  opt.Version,
		LastSeen: time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("register worker: %w", err)
	}

	logger := opt.Logger
	if logger == nil {
		logger = slog.Default()
	}

	rt := workerrt.New(workerrt.Options{
		WorkerID:        b.Manifest.WorkerID,
		Handlers:        opt.Handlers,
		Client:          client,
		LeaseTTL:        opt.LeaseTTL,
		PollTimeout:     opt.PollTimeout,
		HeartbeatPeriod: opt.HeartbeatPeriod,
		Logger:          logger,
	})

	logger.Info("hearth worker connected",
		"worker_id", b.Manifest.WorkerID,
		"coordinator", target,
		"kinds", kinds)

	if err := rt.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
