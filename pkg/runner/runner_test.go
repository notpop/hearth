package runner_test

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
	"github.com/notpop/hearth/pkg/job"
	hearthv1 "github.com/notpop/hearth/pkg/proto/hearth/v1"
	"github.com/notpop/hearth/pkg/runner"
	"github.com/notpop/hearth/pkg/worker"
)

// echoHandler is a dead-simple Handler used in the integration test.
type echoHandler struct{}

func (echoHandler) Kind() string { return "echo" }
func (echoHandler) Handle(_ context.Context, in worker.Input) (worker.Output, error) {
	return worker.Output{Payload: append([]byte("echo:"), in.Payload...)}, nil
}

func TestRunWorkerEndToEnd(t *testing.T) {
	dir := t.TempDir()

	// 1. Stand up a coordinator on a random port with mTLS.
	ca, err := pki.InitCA(filepath.Join(dir, "ca"), "test-ca")
	if err != nil {
		t.Fatalf("InitCA: %v", err)
	}
	serverCert, err := ca.IssueServer("localhost", []string{"localhost"}, []string{"127.0.0.1"}, time.Hour)
	if err != nil {
		t.Fatalf("IssueServer: %v", err)
	}
	clientCert, err := ca.IssueClient("test-worker", time.Hour)
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
	defer gs.GracefulStop()

	// 2. Write a bundle the runner will consume.
	bundlePath := filepath.Join(dir, "test-worker.hearth")
	b := bundle.Bundle{
		Manifest: bundle.Manifest{
			FormatVersion:    bundle.FormatVersion,
			HearthVersion:    "test",
			WorkerID:         "test-worker",
			CoordinatorAddrs: []string{lis.Addr().String()},
			IssuedAt:         time.Now().UTC(),
		},
		CACertPEM:     ca.CertPEM,
		ClientCertPEM: clientCert.CertPEM,
		ClientKeyPEM:  clientCert.KeyPEM,
	}
	if err := bundle.WriteFile(bundlePath, b); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// 3. Submit a job.
	id, err := coord.Submit(context.Background(), job.Spec{
		Kind: "echo", Payload: []byte("hi"), MaxAttempts: 1, LeaseTTL: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// 4. Run the runner; it should pick up the job, run echoHandler, complete it.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- runner.RunWorker(ctx, bundlePath, echoHandler{})
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := coord.Get(context.Background(), id)
		if got.State == job.StateSucceeded {
			if string(got.Result.Payload) != "echo:hi" {
				t.Errorf("payload = %q", got.Result.Payload)
			}
			cancel()
			<-done
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("job did not reach Succeeded via runner.RunWorker")
}

func TestRunWorkerRejectsEmptyArgs(t *testing.T) {
	if err := runner.RunWorker(context.Background(), "", echoHandler{}); err == nil {
		t.Errorf("expected error for empty bundle path")
	}
	if err := runner.RunWorker(context.Background(), "/tmp/x.hearth"); err == nil {
		t.Errorf("expected error for no handlers")
	}
}

func TestRunRejectsBadBundle(t *testing.T) {
	err := runner.Run(context.Background(), runner.Options{
		BundlePath: filepath.Join(t.TempDir(), "missing.hearth"),
		Handlers:   []worker.Handler{echoHandler{}},
	})
	if err == nil || !errors.Is(err, errors.Unwrap(err)) {
		// just sanity: should be non-nil
		if err == nil {
			t.Errorf("expected error")
		}
	}
}
