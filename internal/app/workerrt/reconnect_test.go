package workerrt_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/notpop/hearth/internal/app/workerrt"
	"github.com/notpop/hearth/pkg/job"
	"github.com/notpop/hearth/pkg/worker"
)

// TestRuntimeBackoffsOnLeaseError verifies the loop keeps retrying after
// transient errors and recovers when the client starts succeeding.
func TestRuntimeBackoffsOnLeaseError(t *testing.T) {
	c := &stubClient{}
	calls := atomic.Int32{}
	served := atomic.Bool{}
	c.leaseFn = func(_ context.Context, _ []string, _ string, _, _ time.Duration) (job.Job, bool, error) {
		n := calls.Add(1)
		if n < 3 {
			return job.Job{}, false, errors.New("transient")
		}
		if served.CompareAndSwap(false, true) {
			return job.Job{
				ID:    "j1",
				Spec:  job.Spec{Kind: "k", MaxAttempts: 1, LeaseTTL: time.Second},
				State: job.StateLeased,
			}, true, nil
		}
		return job.Job{}, false, nil
	}

	handled := make(chan struct{}, 1)
	rt := workerrt.New(workerrt.Options{
		WorkerID: "w1",
		Handlers: []worker.Handler{kindHandler{
			kind: "k",
			fn: func(_ context.Context, _ worker.Input) (worker.Output, error) {
				handled <- struct{}{}
				return worker.Output{}, nil
			},
		}},
		Client:          c,
		PollTimeout:     5 * time.Millisecond,
		HeartbeatPeriod: 10 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = rt.Run(ctx) }()

	select {
	case <-handled:
		// Reached the success path after multiple errors.
		if calls.Load() < 3 {
			t.Errorf("expected at least 3 lease attempts, got %d", calls.Load())
		}
	case <-ctx.Done():
		t.Fatal("worker did not recover from lease errors in time")
	}
}
