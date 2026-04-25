package workerrt_test

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	blobfs "github.com/notpop/hearth/internal/adapter/blob/fs"
	"github.com/notpop/hearth/internal/adapter/store/memstore"
	"github.com/notpop/hearth/internal/app/coordinator"
	"github.com/notpop/hearth/internal/app/workerrt"
	"github.com/notpop/hearth/pkg/job"
	"github.com/notpop/hearth/pkg/worker"
)

func TestRuntimeOpensInputBlobsForHandler(t *testing.T) {
	store := memstore.New()
	blob, err := blobfs.Open(t.TempDir())
	if err != nil {
		t.Fatalf("blob open: %v", err)
	}
	c := coordinator.New(coordinator.Options{Store: store})
	client := &inProcessClient{c: c, blob: blob}

	// Pre-stage a blob and submit a job referencing it.
	ref, err := blob.Put(context.Background(), bytes.NewReader([]byte("input data")))
	if err != nil {
		t.Fatalf("Put blob: %v", err)
	}
	_, err = c.Submit(context.Background(), job.Spec{
		Kind:        "consume",
		MaxAttempts: 1,
		LeaseTTL:    30 * time.Second,
		Blobs:       []job.BlobRef{ref},
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	got := make(chan []byte, 1)
	rt := workerrt.New(workerrt.Options{
		WorkerID: "w1",
		Handlers: []worker.Handler{handlerFunc{
			kind: "consume",
			fn: func(_ context.Context, in worker.Input) (worker.Output, error) {
				if len(in.Blobs) != 1 {
					t.Errorf("blobs = %d, want 1", len(in.Blobs))
					return worker.Output{}, nil
				}
				rc, err := in.Blobs[0].Open()
				if err != nil {
					t.Errorf("Open: %v", err)
					return worker.Output{}, nil
				}
				defer rc.Close()
				b, _ := io.ReadAll(rc)
				got <- b
				return worker.Output{Payload: []byte("ok")}, nil
			},
		}},
		Client:          client,
		PollTimeout:     20 * time.Millisecond,
		HeartbeatPeriod: 10 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = rt.Run(ctx) }()

	select {
	case data := <-got:
		if string(data) != "input data" {
			t.Errorf("blob data = %q", data)
		}
	case <-ctx.Done():
		t.Fatal("handler never received input blob")
	}
}
