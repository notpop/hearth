package grpcadapter_test

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	blobfs "github.com/notpop/hearth/internal/adapter/blob/fs"
	"github.com/notpop/hearth/internal/adapter/registry/memregistry"
	"github.com/notpop/hearth/internal/adapter/store/memstore"
	grpcadapter "github.com/notpop/hearth/internal/adapter/transport/grpc"
	"github.com/notpop/hearth/internal/app/coordinator"
	"github.com/notpop/hearth/internal/app/workerrt"
	"github.com/notpop/hearth/internal/security/pki"
	"github.com/notpop/hearth/pkg/job"
	hearthv1 "github.com/notpop/hearth/pkg/proto/hearth/v1"
	"github.com/notpop/hearth/pkg/worker"
)

func TestEndToEndOverMTLS(t *testing.T) {
	dir := t.TempDir()
	ca, err := pki.InitCA(dir+"/ca", "test-ca")
	if err != nil {
		t.Fatalf("InitCA: %v", err)
	}

	serverCert, err := ca.IssueServer("localhost", []string{"localhost"}, []string{"127.0.0.1"}, time.Hour)
	if err != nil {
		t.Fatalf("IssueServer: %v", err)
	}
	clientCert, err := ca.IssueClient("imac-2", time.Hour)
	if err != nil {
		t.Fatalf("IssueClient: %v", err)
	}

	serverTLS, err := ca.ServerTLSConfig(serverCert)
	if err != nil {
		t.Fatalf("ServerTLSConfig: %v", err)
	}
	clientTLS, err := pki.ClientTLSConfig(ca.CertPEM, clientCert.CertPEM, clientCert.KeyPEM, "localhost")
	if err != nil {
		t.Fatalf("ClientTLSConfig: %v", err)
	}

	store := memstore.New()
	blob, err := blobfs.Open(dir + "/blobs")
	if err != nil {
		t.Fatalf("blob: %v", err)
	}
	coord := coordinator.New(coordinator.Options{Store: store})
	registry := memregistry.New()

	srvImpl := grpcadapter.NewServer(coord, blob, registry, grpcadapter.ServerOptions{})
	gs := grpc.NewServer(grpc.Creds(credentials.NewTLS(serverTLS)))
	hearthv1.RegisterCoordinatorServer(gs, srvImpl)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = gs.Serve(lis) }()
	defer gs.GracefulStop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := grpcadapter.Dial(ctx, lis.Addr().String(), clientTLS)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer client.Close()

	// Submit a job through the client API.
	id, err := client.SubmitJob(ctx, job.Spec{
		Kind:        "echo",
		Payload:     []byte("hi"),
		MaxAttempts: 1,
		LeaseTTL:    30 * time.Second,
	})
	if err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}

	// Run a worker runtime against the gRPC client.
	rt := workerrt.New(workerrt.Options{
		WorkerID: "imac-2",
		Handlers: []worker.Handler{
			handlerFunc{kind: "echo", fn: func(_ context.Context, in worker.Input) (worker.Output, error) {
				return worker.Output{Payload: append([]byte("echo:"), in.Payload...)}, nil
			}},
		},
		Client:          client,
		PollTimeout:     200 * time.Millisecond,
		HeartbeatPeriod: 10 * time.Second,
	})

	rtCtx, rtCancel := context.WithCancel(ctx)
	defer rtCancel()
	go func() { _ = rt.Run(rtCtx) }()

	// Wait for completion.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, err := client.GetJob(ctx, id)
		if err == nil && got.State == job.StateSucceeded {
			if string(got.Result.Payload) != "echo:hi" {
				t.Errorf("payload = %q", got.Result.Payload)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("job did not reach Succeeded over gRPC")
}

type handlerFunc struct {
	kind string
	fn   func(ctx context.Context, in worker.Input) (worker.Output, error)
}

func (h handlerFunc) Kind() string { return h.kind }
func (h handlerFunc) Handle(ctx context.Context, in worker.Input) (worker.Output, error) {
	return h.fn(ctx, in)
}
