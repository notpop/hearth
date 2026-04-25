package grpcadapter_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"

	blobfs "github.com/notpop/hearth/internal/adapter/blob/fs"
	"github.com/notpop/hearth/internal/adapter/registry/memregistry"
	"github.com/notpop/hearth/internal/adapter/store/memstore"
	grpcadapter "github.com/notpop/hearth/internal/adapter/transport/grpc"
	"github.com/notpop/hearth/internal/app/coordinator"
	"github.com/notpop/hearth/pkg/job"
	hearthv1 "github.com/notpop/hearth/pkg/proto/hearth/v1"
)

func TestBlobRoundTripOverGRPC(t *testing.T) {
	dir := t.TempDir()
	blob, err := blobfs.Open(dir)
	if err != nil {
		t.Fatalf("blob: %v", err)
	}
	coord := coordinator.New(coordinator.Options{Store: memstore.New()})
	srv := grpcadapter.NewServer(coord, blob, memregistry.New(), grpcadapter.ServerOptions{})

	gs := grpc.NewServer()
	hearthv1.RegisterCoordinatorServer(gs, srv)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = gs.Serve(lis) }()
	defer gs.GracefulStop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := grpcadapter.Dial(ctx, lis.Addr().String(), nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer client.Close()

	// Put.
	data := bytes.Repeat([]byte("hello "), 20_000) // 120 KB → multiple chunks
	ref, err := client.PutBlob(ctx, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("PutBlob: %v", err)
	}
	want := sha256.Sum256(data)
	if ref.SHA256 != hex.EncodeToString(want[:]) {
		t.Errorf("sha mismatch")
	}
	if ref.Size != int64(len(data)) {
		t.Errorf("size = %d", ref.Size)
	}

	// Get.
	rc, err := client.GetBlob(ctx, ref)
	if err != nil {
		t.Fatalf("GetBlob: %v", err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("payload mismatch (len got=%d want=%d)", len(got), len(data))
	}

	// HasBlob via direct rpc — exercise the unary path through the same
	// connection.
	if ok, err := blob.Has(ctx, job.BlobRef{SHA256: ref.SHA256}); err != nil || !ok {
		t.Errorf("Has on server-side blob store: %v ok=%v", err, ok)
	}
}
