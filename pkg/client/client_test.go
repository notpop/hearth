package client_test

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	blobfs "github.com/notpop/hearth/internal/adapter/blob/fs"
	"github.com/notpop/hearth/internal/adapter/registry/memregistry"
	"github.com/notpop/hearth/internal/adapter/store/memstore"
	grpcadapter "github.com/notpop/hearth/internal/adapter/transport/grpc"
	"github.com/notpop/hearth/internal/app/coordinator"
	"github.com/notpop/hearth/internal/security/bundle"
	"github.com/notpop/hearth/internal/security/pki"
	"github.com/notpop/hearth/pkg/client"
	"github.com/notpop/hearth/pkg/job"
	hearthv1 "github.com/notpop/hearth/pkg/proto/hearth/v1"
)

// setup builds a real coordinator (in-process gRPC over mTLS) and returns
// the bundle path, server cleanup, and the underlying coordinator handle.
func setup(t *testing.T) (string, func(), *coordinator.Coordinator) {
	t.Helper()
	dir := t.TempDir()

	ca, err := pki.InitCA(filepath.Join(dir, "ca"), "test-ca")
	if err != nil {
		t.Fatalf("InitCA: %v", err)
	}
	serverCert, err := ca.IssueServer("localhost", []string{"localhost"}, []string{"127.0.0.1"}, time.Hour)
	if err != nil {
		t.Fatalf("IssueServer: %v", err)
	}
	clientCert, err := ca.IssueClient("test-admin", time.Hour)
	if err != nil {
		t.Fatalf("IssueClient: %v", err)
	}
	serverTLS, err := ca.ServerTLSConfig(serverCert)
	if err != nil {
		t.Fatalf("ServerTLSConfig: %v", err)
	}

	store := memstore.New()
	blob, err := blobfs.Open(filepath.Join(dir, "blob"))
	if err != nil {
		t.Fatalf("blob: %v", err)
	}
	coord := coordinator.New(coordinator.Options{Store: store})
	srv := grpcadapter.NewServer(coord, blob, memregistry.New(), grpcadapter.ServerOptions{})
	gs := grpc.NewServer(grpc.Creds(credentials.NewTLS(serverTLS)))
	hearthv1.RegisterCoordinatorServer(gs, srv)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = gs.Serve(lis) }()

	bundlePath := filepath.Join(dir, "admin.hearth")
	if err := bundle.WriteFile(bundlePath, bundle.Bundle{
		Manifest: bundle.Manifest{
			FormatVersion:    bundle.FormatVersion,
			HearthVersion:    "test",
			WorkerID:         "test-admin",
			CoordinatorAddrs: []string{lis.Addr().String()},
			IssuedAt:         time.Now().UTC(),
		},
		CACertPEM:     ca.CertPEM,
		ClientCertPEM: clientCert.CertPEM,
		ClientKeyPEM:  clientCert.KeyPEM,
	}); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	return bundlePath, gs.GracefulStop, coord
}

func TestSubmitGetCancelRoundTrip(t *testing.T) {
	bundlePath, stop, _ := setup(t)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := client.Connect(ctx, bundlePath)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Close()

	id, err := c.Submit(ctx, job.Spec{Kind: "echo", Payload: []byte("hi")})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	got, err := c.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != job.StateQueued {
		t.Errorf("state = %v, want queued", got.State)
	}

	if err := c.Cancel(ctx, id); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	got, _ = c.Get(ctx, id)
	if got.State != job.StateCancelled {
		t.Errorf("state after cancel = %v", got.State)
	}
}

func TestGetUnknownReturnsErrNotFound(t *testing.T) {
	bundlePath, stop, _ := setup(t)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, _ := client.Connect(ctx, bundlePath)
	defer c.Close()

	_, err := c.Get(ctx, "no-such-id")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, client.ErrNotFound) {
		t.Errorf("err is not ErrNotFound: %v", err)
	}
}

func TestSubmitInvalidKindReturnsErrInvalidArgument(t *testing.T) {
	bundlePath, stop, _ := setup(t)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, _ := client.Connect(ctx, bundlePath)
	defer c.Close()

	_, err := c.Submit(ctx, job.Spec{Kind: "BAD KIND"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, client.ErrInvalidArgument) {
		t.Errorf("err is not ErrInvalidArgument: %v", err)
	}
}

func TestCancelTerminalReturnsErrInvalidTransition(t *testing.T) {
	bundlePath, stop, coord := setup(t)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, _ := client.Connect(ctx, bundlePath)
	defer c.Close()

	id, _ := c.Submit(ctx, job.Spec{Kind: "k"})
	// Drive the job to a terminal state via the underlying coordinator.
	if err := coord.Cancel(ctx, id); err != nil {
		t.Fatalf("first Cancel: %v", err)
	}

	err := c.Cancel(ctx, id)
	if err == nil {
		t.Fatal("expected error cancelling terminal")
	}
	if !errors.Is(err, client.ErrInvalidTransition) {
		t.Errorf("err is not ErrInvalidTransition: %v", err)
	}
}

func TestNodesEmpty(t *testing.T) {
	bundlePath, stop, _ := setup(t)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, _ := client.Connect(ctx, bundlePath)
	defer c.Close()

	nodes, err := c.Nodes(ctx)
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if len(nodes) != 0 {
		t.Errorf("len = %d, want 0", len(nodes))
	}
}
