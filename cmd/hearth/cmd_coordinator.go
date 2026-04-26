package main

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	blobfs "github.com/notpop/hearth/internal/adapter/blob/fs"
	"github.com/notpop/hearth/internal/adapter/discovery/mdns"
	"github.com/notpop/hearth/internal/adapter/registry/memregistry"
	"github.com/notpop/hearth/internal/adapter/store/sqlite"
	grpcadapter "github.com/notpop/hearth/internal/adapter/transport/grpc"
	"github.com/notpop/hearth/internal/app/coordinator"
	"github.com/notpop/hearth/internal/security/bundle"
	"github.com/notpop/hearth/internal/security/pki"
	hearthv1 "github.com/notpop/hearth/pkg/proto/hearth/v1"
)

func runCoordinator(args []string) error {
	fs := flag.NewFlagSet("coordinator", flag.ExitOnError)
	listen := fs.String("listen", "0.0.0.0:7843", "address for the coordinator gRPC server")
	dataDir := fs.String("data", "./.hearth", "directory for sqlite + blob storage")
	caDir := fs.String("ca", defaultCADir(), "CA directory")
	advertise := fs.Bool("mdns", true, "advertise the coordinator over mDNS (_hearth._tcp)")
	instance := fs.String("instance", "", "mDNS instance name (default: hostname)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ca, err := loadOrInitCA(*caDir)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		return err
	}

	if err := ensureAdminBundle(ca, *dataDir, *listen); err != nil {
		return err
	}
	store, err := sqlite.Open(filepath.Join(*dataDir, "hearth.db"))
	if err != nil {
		return err
	}
	defer store.Close()

	blob, err := blobfs.Open(filepath.Join(*dataDir, "blobs"))
	if err != nil {
		return err
	}

	registry := memregistry.New()

	serverCert, err := loadOrIssueServerCert(ca, *dataDir, *listen)
	if err != nil {
		return err
	}
	tlsCfg, err := ca.ServerTLSConfig(serverCert)
	if err != nil {
		return err
	}

	coord := coordinator.New(coordinator.Options{Store: store})
	srv := grpcadapter.NewServer(coord, blob, registry, grpcadapter.ServerOptions{})

	gs := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsCfg)))
	hearthv1.RegisterCoordinatorServer(gs, srv)

	lis, err := net.Listen("tcp", *listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	fmt.Printf("hearth coordinator listening on %s\n", lis.Addr())

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var mdnsShutdown func()
	if *advertise {
		host := *instance
		if host == "" {
			h, _ := os.Hostname()
			host = strings.TrimSuffix(h, ".local")
			if host == "" {
				host = "hearth"
			}
		}
		port := portFromAddr(lis.Addr().String())
		shutdown, perr := mdns.Publish(host, port, []string{"version=" + version})
		if perr != nil {
			fmt.Fprintf(os.Stderr, "warn: mdns publish: %v\n", perr)
		} else {
			mdnsShutdown = shutdown
			fmt.Printf("mdns advertising as %s.%s.%s\n", host, mdns.ServiceType, mdns.Domain)
		}
	}

	go func() {
		if err := coord.Run(ctx); err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "coordinator background: %v\n", err)
		}
	}()

	srvErr := make(chan error, 1)
	go func() { srvErr <- gs.Serve(lis) }()

	select {
	case <-ctx.Done():
	case err := <-srvErr:
		if err != nil {
			return err
		}
	}

	gs.GracefulStop()
	if mdnsShutdown != nil {
		mdnsShutdown()
	}
	return nil
}

// loadOrIssueServerCert ensures a server cert exists under data/server.{crt,key},
// issuing a fresh one if needed. The cert SANs cover the listening interface
// and the local hostname so workers can verify the connection regardless of
// whether they reach us by IP or by mDNS name.
func loadOrIssueServerCert(ca *pki.CA, dataDir, listenAddr string) (pki.IssuedClient, error) {
	certPath := filepath.Join(dataDir, "server.crt")
	keyPath := filepath.Join(dataDir, "server.key")

	if certBytes, err := os.ReadFile(certPath); err == nil {
		if keyBytes, err := os.ReadFile(keyPath); err == nil {
			if certIsValid(certBytes) {
				return pki.IssuedClient{Name: "server", CertPEM: certBytes, KeyPEM: keyBytes}, nil
			}
		}
	}

	host, _ := os.Hostname()
	dns := []string{"localhost"}
	if host != "" {
		dns = append(dns, host, host+".local")
	}
	ips := []string{"127.0.0.1", "::1"}
	for _, ip := range nonLoopbackIPs() {
		ips = append(ips, ip)
	}

	issued, err := ca.IssueServer("hearth-coordinator", dns, ips, 5*365*24*time.Hour)
	if err != nil {
		return pki.IssuedClient{}, err
	}
	if err := os.WriteFile(certPath, issued.CertPEM, 0o644); err != nil {
		return pki.IssuedClient{}, err
	}
	if err := os.WriteFile(keyPath, issued.KeyPEM, 0o600); err != nil {
		return pki.IssuedClient{}, err
	}
	return issued, nil
}

func certIsValid(certPEM []byte) bool {
	b, _ := pem.Decode(certPEM)
	if b == nil {
		return false
	}
	c, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		return false
	}
	return time.Now().Before(c.NotAfter.Add(-30 * 24 * time.Hour))
}

func nonLoopbackIPs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ip, _, err := net.ParseCIDR(a.String())
			if err != nil {
				continue
			}
			if ip4 := ip.To4(); ip4 != nil {
				out = append(out, ip4.String())
			}
		}
	}
	return out
}

func portFromAddr(addr string) int {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(p)
	return n
}

// loadOrInitCA loads the CA at dir, or initialises one if absent. This is
// the entry point that lets `hearth coordinator` work on a fresh host with
// no prior CA setup — the user gets a working coordinator with one command.
func loadOrInitCA(dir string) (*pki.CA, error) {
	if ca, err := pki.LoadCA(dir); err == nil {
		return ca, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		// Existing but corrupt CA: fail loudly rather than silently overwriting.
		return nil, fmt.Errorf("load CA at %s: %w", dir, err)
	}
	fmt.Printf("CA not found at %s — initialising a fresh one\n", dir)
	return pki.InitCA(dir, "")
}

// ensureAdminBundle issues a long-lived "admin" client cert on first run
// and packs it into <data>/admin.hearth so the local CLI (submit/status/...)
// works without --bundle on the coordinator host.
func ensureAdminBundle(ca *pki.CA, dataDir, listenAddr string) error {
	path := filepath.Join(dataDir, "admin.hearth")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	issued, err := ca.IssueClient("admin", 0)
	if err != nil {
		return fmt.Errorf("issue admin cert: %w", err)
	}
	b := bundle.Bundle{
		Manifest: bundle.Manifest{
			FormatVersion:    bundle.FormatVersion,
			HearthVersion:    version,
			WorkerID:         "admin",
			CoordinatorAddrs: []string{loopbackAddr(listenAddr)},
			IssuedAt:         time.Now().UTC(),
		},
		CACertPEM:     ca.CertPEM,
		ClientCertPEM: issued.CertPEM,
		ClientKeyPEM:  issued.KeyPEM,
	}
	if err := bundle.WriteFile(path, b); err != nil {
		return fmt.Errorf("write admin bundle: %w", err)
	}
	fmt.Printf("Wrote admin bundle: %s\n", path)
	return nil
}

// loopbackAddr converts the --listen value to a client-friendly address
// for the admin bundle. Bare 0.0.0.0 / :: are rewritten to 127.0.0.1.
func loopbackAddr(listenAddr string) string {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return listenAddr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
