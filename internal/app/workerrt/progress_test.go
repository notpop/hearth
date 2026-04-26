package workerrt_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/notpop/hearth/internal/app/workerrt"
	"github.com/notpop/hearth/pkg/job"
	"github.com/notpop/hearth/pkg/worker"
)

// TestRuntimeReportsProgressViaHeartbeat verifies that a handler calling
// Input.Report results in non-nil progress arriving on Heartbeat.
func TestRuntimeReportsProgressViaHeartbeat(t *testing.T) {
	c := &stubClient{}
	served := atomic.Bool{}
	c.leaseFn = func(_ context.Context, _ []string, _ string, _, _ time.Duration) (job.Job, bool, error) {
		if served.CompareAndSwap(false, true) {
			return job.Job{
				ID:    "j1",
				Spec:  job.Spec{Kind: "k", MaxAttempts: 1, LeaseTTL: time.Second},
				State: job.StateLeased,
			}, true, nil
		}
		return job.Job{}, false, nil
	}

	progressCh := make(chan *job.Progress, 4)
	c.heartbeatFn = func(_ context.Context, _ job.ID, _ string, p *job.Progress) (time.Time, bool, error) {
		progressCh <- p
		return time.Time{}, false, nil
	}

	handlerStarted := make(chan struct{})
	handlerCanProceed := make(chan struct{})
	rt := workerrt.New(workerrt.Options{
		WorkerID: "w1",
		Handlers: []worker.Handler{kindHandler{
			kind: "k",
			fn: func(ctx context.Context, in worker.Input) (worker.Output, error) {
				close(handlerStarted)
				in.Report(0.25, "started")
				<-handlerCanProceed
				in.Report(1.0, "done")
				return worker.Output{}, nil
			},
		}},
		Client:          c,
		PollTimeout:     5 * time.Millisecond,
		HeartbeatPeriod: 30 * time.Millisecond,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { _ = rt.Run(ctx) }()

	<-handlerStarted

	// Wait for the first heartbeat to fire after the initial Report.
	deadline := time.After(2 * time.Second)
	var firstNonNil *job.Progress
	for firstNonNil == nil {
		select {
		case p := <-progressCh:
			if p != nil {
				firstNonNil = p
			}
		case <-deadline:
			t.Fatal("no progress observed via heartbeat")
		}
	}
	if firstNonNil.Percent != 0.25 || firstNonNil.Message != "started" {
		t.Errorf("first reported progress = %+v, want {0.25, started}", *firstNonNil)
	}

	close(handlerCanProceed)
	cancel()
}

func TestProgressReportClampsBounds(t *testing.T) {
	c := &stubClient{}
	served := atomic.Bool{}
	c.leaseFn = func(_ context.Context, _ []string, _ string, _, _ time.Duration) (job.Job, bool, error) {
		if served.CompareAndSwap(false, true) {
			return job.Job{
				ID:    "j1",
				Spec:  job.Spec{Kind: "k", MaxAttempts: 1, LeaseTTL: time.Second},
				State: job.StateLeased,
			}, true, nil
		}
		return job.Job{}, false, nil
	}

	got := make(chan *job.Progress, 8)
	c.heartbeatFn = func(_ context.Context, _ job.ID, _ string, p *job.Progress) (time.Time, bool, error) {
		got <- p
		return time.Time{}, false, nil
	}

	rt := workerrt.New(workerrt.Options{
		WorkerID: "w1",
		Handlers: []worker.Handler{kindHandler{
			kind: "k",
			fn: func(_ context.Context, in worker.Input) (worker.Output, error) {
				in.Report(-0.5, "below")
				time.Sleep(20 * time.Millisecond) // give heartbeat a chance to fire
				in.Report(2.5, "above")
				time.Sleep(20 * time.Millisecond)
				return worker.Output{}, nil
			},
		}},
		Client:          c,
		PollTimeout:     5 * time.Millisecond,
		HeartbeatPeriod: 10 * time.Millisecond,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = rt.Run(ctx) }()

	deadline := time.After(time.Second)
	sawZero := false
	sawOne := false
	for !(sawZero && sawOne) {
		select {
		case p := <-got:
			if p == nil {
				continue
			}
			if p.Percent == 0 {
				sawZero = true
			}
			if p.Percent == 1 {
				sawOne = true
			}
		case <-deadline:
			t.Fatalf("did not see clamped values: zero=%v one=%v", sawZero, sawOne)
		}
	}
}
