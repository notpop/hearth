package workerrt_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/notpop/hearth/internal/app/workerrt"
	"github.com/notpop/hearth/pkg/job"
	"github.com/notpop/hearth/pkg/worker"
)

// stubClient lets tests control every CoordinatorClient call independently.
type stubClient struct {
	mu sync.Mutex

	leaseFn     func(ctx context.Context, kinds []string, workerID string, ttl, poll time.Duration) (job.Job, bool, error)
	heartbeatFn func(ctx context.Context, id job.ID, workerID string) (time.Time, bool, error)
	completeFn  func(ctx context.Context, id job.ID, workerID string, res job.Result) error
	failFn      func(ctx context.Context, id job.ID, workerID string, msg string) error
	getBlobFn   func(ctx context.Context, ref job.BlobRef) (io.ReadCloser, error)
	putBlobFn   func(ctx context.Context, r io.Reader) (job.BlobRef, error)

	completed atomic.Int32
	failed    atomic.Int32
	failMsgs  []string
}

func (c *stubClient) Lease(ctx context.Context, kinds []string, workerID string, ttl, poll time.Duration) (job.Job, bool, error) {
	return c.leaseFn(ctx, kinds, workerID, ttl, poll)
}
func (c *stubClient) Heartbeat(ctx context.Context, id job.ID, workerID string) (time.Time, bool, error) {
	if c.heartbeatFn != nil {
		return c.heartbeatFn(ctx, id, workerID)
	}
	return time.Time{}, false, nil
}
func (c *stubClient) Complete(ctx context.Context, id job.ID, workerID string, res job.Result) error {
	c.completed.Add(1)
	if c.completeFn != nil {
		return c.completeFn(ctx, id, workerID, res)
	}
	return nil
}
func (c *stubClient) Fail(ctx context.Context, id job.ID, workerID string, msg string) error {
	c.mu.Lock()
	c.failMsgs = append(c.failMsgs, msg)
	c.mu.Unlock()
	c.failed.Add(1)
	if c.failFn != nil {
		return c.failFn(ctx, id, workerID, msg)
	}
	return nil
}
func (c *stubClient) GetBlob(ctx context.Context, ref job.BlobRef) (io.ReadCloser, error) {
	return c.getBlobFn(ctx, ref)
}
func (c *stubClient) PutBlob(ctx context.Context, r io.Reader) (job.BlobRef, error) {
	return c.putBlobFn(ctx, r)
}

type kindHandler struct {
	kind string
	fn   func(ctx context.Context, in worker.Input) (worker.Output, error)
}

func (h kindHandler) Kind() string { return h.kind }
func (h kindHandler) Handle(ctx context.Context, in worker.Input) (worker.Output, error) {
	return h.fn(ctx, in)
}

func TestNewPanicsWithoutWorkerID(t *testing.T) {
	defer expectPanic(t)
	workerrt.New(workerrt.Options{
		Handlers: []worker.Handler{kindHandler{kind: "k", fn: nil}},
		Client:   &stubClient{},
	})
}

func TestNewPanicsWithoutClient(t *testing.T) {
	defer expectPanic(t)
	workerrt.New(workerrt.Options{
		WorkerID: "w1",
		Handlers: []worker.Handler{kindHandler{kind: "k", fn: nil}},
	})
}

func TestNewPanicsWithoutHandlers(t *testing.T) {
	defer expectPanic(t)
	workerrt.New(workerrt.Options{
		WorkerID: "w1",
		Client:   &stubClient{},
	})
}

func TestNewPanicsOnDuplicateKind(t *testing.T) {
	defer expectPanic(t)
	workerrt.New(workerrt.Options{
		WorkerID: "w1",
		Client:   &stubClient{},
		Handlers: []worker.Handler{
			kindHandler{kind: "k", fn: nil},
			kindHandler{kind: "k", fn: nil},
		},
	})
}

func TestRuntimeFailsWhenNoHandlerForKind(t *testing.T) {
	c := &stubClient{}
	calls := 0
	c.leaseFn = func(_ context.Context, _ []string, _ string, _, _ time.Duration) (job.Job, bool, error) {
		calls++
		if calls > 1 {
			return job.Job{}, false, nil
		}
		return job.Job{
			ID: "j1",
			Spec: job.Spec{
				Kind:        "unsupported",
				MaxAttempts: 1,
				LeaseTTL:    time.Second,
			},
			State: job.StateLeased,
		}, true, nil
	}

	rt := workerrt.New(workerrt.Options{
		WorkerID:        "w1",
		Handlers:        []worker.Handler{kindHandler{kind: "real", fn: func(_ context.Context, _ worker.Input) (worker.Output, error) { return worker.Output{}, nil }}},
		Client:          c,
		PollTimeout:     20 * time.Millisecond,
		HeartbeatPeriod: 10 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = rt.Run(ctx)

	if c.failed.Load() == 0 {
		t.Errorf("expected Fail to be called")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.failMsgs) == 0 || !contains(c.failMsgs[0], "no handler") {
		t.Errorf("fail msg = %v", c.failMsgs)
	}
}

func TestRuntimeReportsHandlerError(t *testing.T) {
	c := &stubClient{}
	once := false
	c.leaseFn = func(_ context.Context, _ []string, _ string, _, _ time.Duration) (job.Job, bool, error) {
		if once {
			return job.Job{}, false, nil
		}
		once = true
		return job.Job{
			ID:    "j1",
			Spec:  job.Spec{Kind: "k", MaxAttempts: 1, LeaseTTL: time.Second},
			State: job.StateLeased,
		}, true, nil
	}

	rt := workerrt.New(workerrt.Options{
		WorkerID: "w1",
		Handlers: []worker.Handler{kindHandler{
			kind: "k",
			fn:   func(_ context.Context, _ worker.Input) (worker.Output, error) { return worker.Output{}, errors.New("kaboom") },
		}},
		Client:          c,
		PollTimeout:     20 * time.Millisecond,
		HeartbeatPeriod: 10 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = rt.Run(ctx)

	if c.failed.Load() == 0 {
		t.Errorf("expected Fail to be called")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !contains(c.failMsgs[0], "kaboom") {
		t.Errorf("fail msg = %v", c.failMsgs)
	}
}

func TestRuntimeRetriesOnLeaseError(t *testing.T) {
	c := &stubClient{}
	hits := atomic.Int32{}
	c.leaseFn = func(_ context.Context, _ []string, _ string, _, _ time.Duration) (job.Job, bool, error) {
		hits.Add(1)
		return job.Job{}, false, errors.New("boom")
	}

	rt := workerrt.New(workerrt.Options{
		WorkerID:        "w1",
		Handlers:        []worker.Handler{kindHandler{kind: "k", fn: nil}},
		Client:          c,
		PollTimeout:     5 * time.Millisecond,
		HeartbeatPeriod: 10 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	_ = rt.Run(ctx)

	if hits.Load() < 1 {
		t.Errorf("expected at least one lease attempt")
	}
}

func TestRuntimeCancelsHandlerOnLeaseLoss(t *testing.T) {
	c := &stubClient{}
	served := false
	c.leaseFn = func(_ context.Context, _ []string, _ string, _, _ time.Duration) (job.Job, bool, error) {
		if served {
			return job.Job{}, false, nil
		}
		served = true
		return job.Job{
			ID:    "j1",
			Spec:  job.Spec{Kind: "k", MaxAttempts: 1, LeaseTTL: 50 * time.Millisecond},
			State: job.StateLeased,
		}, true, nil
	}
	c.heartbeatFn = func(_ context.Context, _ job.ID, _ string) (time.Time, bool, error) {
		return time.Time{}, false, errors.New("lease lost")
	}

	handlerCancelled := make(chan struct{}, 1)
	rt := workerrt.New(workerrt.Options{
		WorkerID: "w1",
		Handlers: []worker.Handler{kindHandler{
			kind: "k",
			fn: func(ctx context.Context, _ worker.Input) (worker.Output, error) {
				<-ctx.Done()
				handlerCancelled <- struct{}{}
				return worker.Output{}, ctx.Err()
			},
		}},
		Client:          c,
		PollTimeout:     20 * time.Millisecond,
		HeartbeatPeriod: 10 * time.Millisecond,
		OnLeaseLoss:     func(job.ID) {},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	_ = rt.Run(ctx)

	select {
	case <-handlerCancelled:
	case <-time.After(time.Second):
		t.Fatal("handler not cancelled when heartbeat failed")
	}
}

// --- helpers ------------------------------------------------------------

func expectPanic(t *testing.T) {
	t.Helper()
	if recover() == nil {
		t.Errorf("expected panic")
	}
}

func contains(s, sub string) bool {
	if len(s) < len(sub) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
