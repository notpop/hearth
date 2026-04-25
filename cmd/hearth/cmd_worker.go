package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/notpop/hearth/internal/adapter/discovery/mdns"
	grpcadapter "github.com/notpop/hearth/internal/adapter/transport/grpc"
	"github.com/notpop/hearth/internal/app"
	"github.com/notpop/hearth/internal/security/bundle"
	"github.com/notpop/hearth/internal/security/pki"
)

// runWorker is the OSS-binary-only command. It validates connectivity end-
// to-end (mTLS handshake, RegisterWorker) but does not run any handlers,
// because handlers are user-supplied. To actually process jobs, users build
// a binary that imports this project and registers handlers via workerrt.
func runWorker(args []string) error {
	fs := flag.NewFlagSet("worker", flag.ExitOnError)
	bundlePath := fs.String("bundle", "", "path to the .hearth enrollment bundle (required)")
	addr := fs.String("coordinator", "", "override coordinator address (default: from bundle / mDNS)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *bundlePath == "" {
		return fmt.Errorf("--bundle is required")
	}
	b, err := bundle.ReadFile(*bundlePath)
	if err != nil {
		return fmt.Errorf("read bundle: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	target, err := resolveCoordinator(ctx, *addr, b)
	if err != nil {
		return err
	}

	host := strings.SplitN(target, ":", 2)[0]
	tlsCfg, err := pki.ClientTLSConfig(b.CACertPEM, b.ClientCertPEM, b.ClientKeyPEM, host)
	if err != nil {
		return err
	}

	client, err := grpcadapter.Dial(ctx, target, tlsCfg)
	if err != nil {
		return err
	}
	defer client.Close()

	hostname, _ := os.Hostname()
	hbInterval, err := client.RegisterWorker(ctx, app.WorkerInfo{
		ID:       b.Manifest.WorkerID,
		Hostname: hostname,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		Version:  version,
		LastSeen: time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}

	fmt.Printf("hearth worker connected\n  worker_id   = %s\n  coordinator = %s\n  hb interval = %s\n",
		b.Manifest.WorkerID, target, hbInterval)
	fmt.Println()
	fmt.Println("This OSS binary has no handlers. To run real jobs, build your own")
	fmt.Println("binary that imports github.com/notpop/hearth and registers handlers")
	fmt.Println("via workerrt.New(...). See examples/img2pdf for a worked example.")

	<-ctx.Done()
	return nil
}

// resolveCoordinator picks the address to dial. Priority:
//  1. --coordinator flag (override)
//  2. bundle Manifest.CoordinatorAddrs
//  3. mDNS discovery
func resolveCoordinator(ctx context.Context, override string, b bundle.Bundle) (string, error) {
	if override != "" {
		return override, nil
	}
	if len(b.Manifest.CoordinatorAddrs) > 0 {
		return b.Manifest.CoordinatorAddrs[0], nil
	}
	dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	services, err := mdns.Discover(dctx, 2*time.Second)
	if err != nil {
		return "", fmt.Errorf("mdns: %w", err)
	}
	for _, s := range services {
		if a := s.Address(); a != "" {
			return a, nil
		}
	}
	return "", fmt.Errorf("no coordinator address: bundle has no addrs and mDNS found nothing")
}
