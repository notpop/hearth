package workerrt_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	blobfs "github.com/notpop/hearth/internal/adapter/blob/fs"
	"github.com/notpop/hearth/internal/adapter/store/memstore"
	"github.com/notpop/hearth/internal/app/coordinator"
	"github.com/notpop/hearth/internal/app/workerrt"
	"github.com/notpop/hearth/pkg/job"
	"github.com/notpop/hearth/pkg/worker"
)

// inProcessClient adapts a Coordinator + BlobStore to CoordinatorClient.
// In production this role is filled by the gRPC client.
type inProcessClient struct {
	c    *coordinator.Coordinator
	blob *blobfs.Store
}

func (c *inProcessClient) Lease(ctx context.Context, kinds []string, workerID string, ttl, pollTimeout time.Duration) (job.Job, bool, error) {
	return c.c.Lease(ctx, kinds, workerID, ttl, pollTimeout)
}
func (c *inProcessClient) Heartbeat(ctx context.Context, id job.ID, workerID string) (time.Time, bool, error) {
	exp, err := c.c.Heartbeat(ctx, id, workerID)
	return exp, false, err
}
func (c *inProcessClient) Complete(ctx context.Context, id job.ID, workerID string, res job.Result) error {
	return c.c.Complete(ctx, id, workerID, res)
}
func (c *inProcessClient) Fail(ctx context.Context, id job.ID, workerID string, msg string) error {
	_, _, err := c.c.Fail(ctx, id, workerID, msg)
	return err
}
func (c *inProcessClient) GetBlob(ctx context.Context, ref job.BlobRef) (io.ReadCloser, error) {
	return c.blob.Get(ctx, ref)
}
func (c *inProcessClient) PutBlob(ctx context.Context, r io.Reader) (job.BlobRef, error) {
	return c.blob.Put(ctx, r)
}

type echoHandler struct{ called atomic.Int32 }

func (*echoHandler) Kind() string { return "echo" }
func (e *echoHandler) Handle(_ context.Context, in worker.Input) (worker.Output, error) {
	e.called.Add(1)
	return worker.Output{Payload: append([]byte("echo:"), in.Payload...)}, nil
}

type flakyHandler struct {
	calls atomic.Int32
}

func (*flakyHandler) Kind() string { return "flaky" }
func (f *flakyHandler) Handle(_ context.Context, _ worker.Input) (worker.Output, error) {
	n := f.calls.Add(1)
	if n < 2 {
		return worker.Output{}, errors.New("transient")
	}
	return worker.Output{Payload: []byte("ok")}, nil
}

func setup(t *testing.T) (*coordinator.Coordinator, *blobfs.Store, *inProcessClient) {
	t.Helper()
	store := memstore.New()
	blob, err := blobfs.Open(t.TempDir())
	if err != nil {
		t.Fatalf("blob open: %v", err)
	}
	c := coordinator.New(coordinator.Options{Store: store})
	return c, blob, &inProcessClient{c: c, blob: blob}
}

func TestRuntimeProcessesJob(t *testing.T) {
	coord, _, client := setup(t)

	id, err := coord.Submit(context.Background(), job.Spec{
		Kind: "echo", Payload: []byte("hi"), MaxAttempts: 1, LeaseTTL: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	h := &echoHandler{}
	rt := workerrt.New(workerrt.Options{
		WorkerID:        "w1",
		Handlers:        []worker.Handler{h},
		Client:          client,
		LeaseTTL:        30 * time.Second,
		PollTimeout:     200 * time.Millisecond,
		HeartbeatPeriod: 10 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { _ = rt.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := coord.Get(context.Background(), id)
		if got.State == job.StateSucceeded {
			if string(got.Result.Payload) != "echo:hi" {
				t.Errorf("payload = %q", got.Result.Payload)
			}
			if h.called.Load() != 1 {
				t.Errorf("handler called %d times", h.called.Load())
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("job did not reach Succeeded in time")
}

func TestRuntimeRetriesFailedJob(t *testing.T) {
	coord, _, client := setup(t)

	id, err := coord.Submit(context.Background(), job.Spec{
		Kind:     "flaky",
		MaxAttempts: 3,
		LeaseTTL: 30 * time.Second,
		Backoff:  job.BackoffPolicy{Initial: 0, Multiplier: 1},
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	h := &flakyHandler{}
	rt := workerrt.New(workerrt.Options{
		WorkerID:        "w1",
		Handlers:        []worker.Handler{h},
		Client:          client,
		LeaseTTL:        30 * time.Second,
		PollTimeout:     100 * time.Millisecond,
		HeartbeatPeriod: 10 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { _ = rt.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := coord.Get(context.Background(), id)
		if got.State == job.StateSucceeded {
			if h.calls.Load() < 2 {
				t.Errorf("expected at least 2 attempts, got %d", h.calls.Load())
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("flaky job did not eventually succeed")
}

func TestRuntimePersistsOutputBlob(t *testing.T) {
	coord, blobStore, client := setup(t)

	type blobHandler struct{}
	id, _ := coord.Submit(context.Background(), job.Spec{Kind: "blob", MaxAttempts: 1, LeaseTTL: 30 * time.Second})

	rt := workerrt.New(workerrt.Options{
		WorkerID: "w1",
		Handlers: []worker.Handler{handlerFunc{
			kind: "blob",
			fn: func(_ context.Context, _ worker.Input) (worker.Output, error) {
				return worker.Output{
					Blobs: []worker.OutputBlob{{Reader: bytes.NewReader([]byte("hello blobs"))}},
				}, nil
			},
		}},
		Client:          client,
		PollTimeout:     100 * time.Millisecond,
		HeartbeatPeriod: 10 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { _ = rt.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := coord.Get(context.Background(), id)
		if got.State == job.StateSucceeded && got.Result != nil && len(got.Result.Blobs) == 1 {
			ref := got.Result.Blobs[0]
			rc, err := blobStore.Get(context.Background(), ref)
			if err != nil {
				t.Fatalf("read blob: %v", err)
			}
			defer rc.Close()
			b, _ := io.ReadAll(rc)
			if string(b) != "hello blobs" {
				t.Errorf("blob payload = %q", b)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("blob job did not reach Succeeded in time")
}

type handlerFunc struct {
	kind string
	fn   func(ctx context.Context, in worker.Input) (worker.Output, error)
}

func (h handlerFunc) Kind() string { return h.kind }
func (h handlerFunc) Handle(ctx context.Context, in worker.Input) (worker.Output, error) {
	return h.fn(ctx, in)
}
