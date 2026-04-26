// Package workerrt is the worker-side runtime that pulls jobs from the
// coordinator, dispatches them to user-supplied Handlers, keeps the lease
// alive with heartbeats, and reports the outcome.
//
// The runtime never speaks gRPC directly; it depends on the
// CoordinatorClient interface so the same code can be exercised by tests
// using an in-process coordinator and by production using the gRPC adapter.
package workerrt

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/notpop/hearth/internal/app"
	"github.com/notpop/hearth/pkg/job"
	"github.com/notpop/hearth/pkg/worker"
)

// CoordinatorClient is the worker-facing slice of the coordinator API.
// gRPC and in-process implementations both satisfy this.
type CoordinatorClient interface {
	Lease(ctx context.Context, kinds []string, workerID string, ttl, pollTimeout time.Duration) (job.Job, bool, error)
	Heartbeat(ctx context.Context, jobID job.ID, workerID string, progress *job.Progress) (expires time.Time, cancel bool, err error)
	Complete(ctx context.Context, jobID job.ID, workerID string, res job.Result) error
	Fail(ctx context.Context, jobID job.ID, workerID string, errMsg string) error

	GetBlob(ctx context.Context, ref job.BlobRef) (io.ReadCloser, error)
	PutBlob(ctx context.Context, r io.Reader) (job.BlobRef, error)
}

// Options configure a Runtime.
type Options struct {
	WorkerID         string
	Handlers         []worker.Handler
	Client           CoordinatorClient
	Clock            app.Clock     // default: wall clock
	Logger           *slog.Logger  // default: slog.Default()
	LeaseTTL         time.Duration // default: 30s — what the runtime requests
	PollTimeout      time.Duration // default: 30s — Lease long-poll budget
	HeartbeatPeriod  time.Duration // default: LeaseTTL / 3
	OnLeaseLoss      func(job.ID)  // optional hook for tests/observability
}

// Runtime is the long-running worker loop.
type Runtime struct {
	workerID        string
	handlers        map[string]worker.Handler
	kinds           []string
	client          CoordinatorClient
	clock           app.Clock
	log             *slog.Logger
	leaseTTL        time.Duration
	pollTimeout     time.Duration
	heartbeatPeriod time.Duration
	onLeaseLoss     func(job.ID)
}

// New constructs a Runtime, applying defaults.
func New(opt Options) *Runtime {
	if opt.WorkerID == "" {
		panic("workerrt: WorkerID is required")
	}
	if opt.Client == nil {
		panic("workerrt: Client is required")
	}
	if len(opt.Handlers) == 0 {
		panic("workerrt: at least one Handler is required")
	}
	handlers := make(map[string]worker.Handler, len(opt.Handlers))
	kinds := make([]string, 0, len(opt.Handlers))
	for _, h := range opt.Handlers {
		k := h.Kind()
		if err := job.ValidateKind(k); err != nil {
			panic("workerrt: " + err.Error())
		}
		if _, dup := handlers[k]; dup {
			panic("workerrt: duplicate handler for kind " + k)
		}
		handlers[k] = h
		kinds = append(kinds, k)
	}
	if opt.Clock == nil {
		opt.Clock = wallClock{}
	}
	if opt.Logger == nil {
		opt.Logger = slog.Default()
	}
	if opt.LeaseTTL == 0 {
		opt.LeaseTTL = 30 * time.Second
	}
	if opt.PollTimeout == 0 {
		opt.PollTimeout = 30 * time.Second
	}
	if opt.HeartbeatPeriod == 0 {
		opt.HeartbeatPeriod = opt.LeaseTTL / 3
		if opt.HeartbeatPeriod < time.Second {
			opt.HeartbeatPeriod = time.Second
		}
	}
	return &Runtime{
		workerID:        opt.WorkerID,
		handlers:        handlers,
		kinds:           kinds,
		client:          opt.Client,
		clock:           opt.Clock,
		log:             opt.Logger,
		leaseTTL:        opt.LeaseTTL,
		pollTimeout:     opt.PollTimeout,
		heartbeatPeriod: opt.HeartbeatPeriod,
		onLeaseLoss:     opt.OnLeaseLoss,
	}
}

// Run drives the lease/dispatch loop until ctx is done. Returns ctx.Err()
// on shutdown.
//
// Reconnection semantics: if Lease errors (coordinator unreachable, network
// partition, etc.) the loop sleeps for an exponentially-growing backoff
// capped at reconnectMax, then retries. The backoff resets to
// reconnectInitial on the first successful Lease (whether it returned a
// job or not). This gives the worker the "set and forget" property — a
// coordinator restart is just a long Lease error followed by reconnection.
const (
	reconnectInitial = time.Second
	reconnectMax     = 30 * time.Second
)

func (r *Runtime) Run(ctx context.Context) error {
	backoff := reconnectInitial
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		j, ok, err := r.client.Lease(ctx, r.kinds, r.workerID, r.leaseTTL, r.pollTimeout)
		if err != nil {
			r.log.Warn("lease error, backing off", "err", err, "delay", backoff)
			if !sleepCtx(ctx, backoff) {
				return ctx.Err()
			}
			backoff *= 2
			if backoff > reconnectMax {
				backoff = reconnectMax
			}
			continue
		}
		// Successful round-trip — reset backoff.
		backoff = reconnectInitial
		if !ok {
			continue
		}

		r.runOne(ctx, j)
	}
}

func (r *Runtime) runOne(parent context.Context, j job.Job) {
	r.log.Info("task received",
		"event", "task.received",
		"worker", r.workerID,
		"job", j.ID,
		"kind", j.Spec.Kind,
		"attempt", j.Attempt)

	h, ok := r.handlers[j.Spec.Kind]
	if !ok {
		r.log.Error("no handler for kind",
			"event", "task.failed",
			"worker", r.workerID,
			"job", j.ID,
			"kind", j.Spec.Kind,
			"reason", "no_handler")
		_ = r.client.Fail(parent, j.ID, r.workerID,
			fmt.Sprintf("worker has no handler for kind %q", j.Spec.Kind))
		return
	}

	jobCtx, cancel := context.WithCancel(parent)
	defer cancel()

	tracker := newProgressTracker(r.clock)

	hbDone := make(chan struct{})
	go r.heartbeat(jobCtx, cancel, j.ID, tracker, hbDone)

	in := worker.Input{
		JobID:   j.ID,
		Kind:    j.Spec.Kind,
		Attempt: j.Attempt,
		Payload: j.Spec.Payload,
		Blobs:   r.makeInputBlobs(parent, j.Spec.Blobs),
		Report:  tracker.report,
	}

	r.log.Info("task started",
		"event", "task.started",
		"worker", r.workerID,
		"job", j.ID,
		"kind", j.Spec.Kind)

	startedAt := r.clock.Now()
	out, herr := h.Handle(jobCtx, in)
	cancel()
	<-hbDone
	elapsed := r.clock.Now().Sub(startedAt)

	if herr != nil {
		r.log.Warn("task failed",
			"event", "task.failed",
			"worker", r.workerID,
			"job", j.ID,
			"kind", j.Spec.Kind,
			"err", herr.Error(),
			"elapsed", elapsed)
		if perr := r.client.Fail(parent, j.ID, r.workerID, herr.Error()); perr != nil {
			r.log.Error("report fail", "id", j.ID, "err", perr)
		}
		return
	}

	res, perr := r.persistOutput(parent, out)
	if perr != nil {
		r.log.Warn("task failed (output persist)",
			"event", "task.failed",
			"worker", r.workerID,
			"job", j.ID,
			"kind", j.Spec.Kind,
			"err", perr.Error(),
			"elapsed", elapsed)
		if ferr := r.client.Fail(parent, j.ID, r.workerID, "persist output: "+perr.Error()); ferr != nil {
			r.log.Error("report fail (persist)", "id", j.ID, "err", ferr)
		}
		return
	}
	if cerr := r.client.Complete(parent, j.ID, r.workerID, res); cerr != nil {
		r.log.Error("report complete", "id", j.ID, "err", cerr)
	}
	r.log.Info("task done",
		"event", "task.done",
		"worker", r.workerID,
		"job", j.ID,
		"kind", j.Spec.Kind,
		"elapsed", elapsed)
}

// heartbeat keeps the lease alive while the handler runs and piggybacks
// any progress reported by the handler. If the coordinator reports a
// lost lease or asks to cancel, it cancels jobCtx so the handler can
// unwind.
func (r *Runtime) heartbeat(ctx context.Context, cancel context.CancelFunc, id job.ID, tracker *progressTracker, done chan<- struct{}) {
	defer close(done)
	t := time.NewTicker(r.heartbeatPeriod)
	defer t.Stop()
	var lastSent *job.Progress
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cur := tracker.snapshot()
			toSend := cur
			// Only send progress on the wire when it changed since last
			// heartbeat to keep stream noise low.
			if toSend != nil && lastSent != nil && progressEqual(*toSend, *lastSent) {
				toSend = nil
			}
			_, cancelReq, err := r.client.Heartbeat(ctx, id, r.workerID, toSend)
			if err != nil || cancelReq {
				r.log.Warn("lease lost or cancellation requested",
					"job", id, "err", err, "cancelRequested", cancelReq)
				if r.onLeaseLoss != nil {
					r.onLeaseLoss(id)
				}
				cancel()
				return
			}
			if toSend != nil {
				lastSent = toSend
			}
		}
	}
}

func progressEqual(a, b job.Progress) bool {
	return a.Percent == b.Percent && a.Message == b.Message
}

func (r *Runtime) makeInputBlobs(ctx context.Context, refs []job.BlobRef) []worker.InputBlob {
	out := make([]worker.InputBlob, len(refs))
	for i, ref := range refs {
		ref := ref
		out[i] = worker.InputBlob{
			Ref: ref,
			Open: func() (io.ReadCloser, error) {
				return r.client.GetBlob(ctx, ref)
			},
		}
	}
	return out
}

func (r *Runtime) persistOutput(ctx context.Context, out worker.Output) (job.Result, error) {
	res := job.Result{Payload: out.Payload}
	for _, b := range out.Blobs {
		ref, err := r.client.PutBlob(ctx, b.Reader)
		if err != nil {
			return job.Result{}, err
		}
		res.Blobs = append(res.Blobs, ref)
	}
	return res, nil
}

// --- helpers ------------------------------------------------------------

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now().UTC() }

// sleepCtx waits for d or ctx cancellation, returning true if d elapsed first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
