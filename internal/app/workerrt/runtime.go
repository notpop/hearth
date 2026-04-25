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
	Heartbeat(ctx context.Context, jobID job.ID, workerID string) (expires time.Time, cancel bool, err error)
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
// on shutdown. Transient lease errors are logged and retried with a small
// delay; the loop only exits on context cancellation.
func (r *Runtime) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		j, ok, err := r.client.Lease(ctx, r.kinds, r.workerID, r.leaseTTL, r.pollTimeout)
		if err != nil {
			r.log.Error("lease", "err", err)
			if !sleepCtx(ctx, time.Second) {
				return ctx.Err()
			}
			continue
		}
		if !ok {
			continue
		}

		r.runOne(ctx, j)
	}
}

func (r *Runtime) runOne(parent context.Context, j job.Job) {
	h, ok := r.handlers[j.Spec.Kind]
	if !ok {
		_ = r.client.Fail(parent, j.ID, r.workerID,
			fmt.Sprintf("worker has no handler for kind %q", j.Spec.Kind))
		return
	}

	jobCtx, cancel := context.WithCancel(parent)
	defer cancel()

	hbDone := make(chan struct{})
	go r.heartbeat(jobCtx, cancel, j.ID, hbDone)

	in := worker.Input{
		JobID:   j.ID,
		Kind:    j.Spec.Kind,
		Attempt: j.Attempt,
		Payload: j.Spec.Payload,
		Blobs:   r.makeInputBlobs(parent, j.Spec.Blobs),
	}

	out, herr := h.Handle(jobCtx, in)
	cancel()
	<-hbDone

	if herr != nil {
		if perr := r.client.Fail(parent, j.ID, r.workerID, herr.Error()); perr != nil {
			r.log.Error("report fail", "id", j.ID, "err", perr)
		}
		return
	}

	res, perr := r.persistOutput(parent, out)
	if perr != nil {
		if ferr := r.client.Fail(parent, j.ID, r.workerID, "persist output: "+perr.Error()); ferr != nil {
			r.log.Error("report fail (persist)", "id", j.ID, "err", ferr)
		}
		return
	}
	if cerr := r.client.Complete(parent, j.ID, r.workerID, res); cerr != nil {
		r.log.Error("report complete", "id", j.ID, "err", cerr)
	}
}

// heartbeat keeps the lease alive while the handler runs. If the
// coordinator reports a lost lease or asks to cancel, it cancels jobCtx
// so the handler can unwind.
func (r *Runtime) heartbeat(ctx context.Context, cancel context.CancelFunc, id job.ID, done chan<- struct{}) {
	defer close(done)
	t := time.NewTicker(r.heartbeatPeriod)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, cancelReq, err := r.client.Heartbeat(ctx, id, r.workerID)
			if err != nil || cancelReq {
				r.log.Warn("lease lost or cancellation requested",
					"job", id, "err", err, "cancelRequested", cancelReq)
				if r.onLeaseLoss != nil {
					r.onLeaseLoss(id)
				}
				cancel()
				return
			}
		}
	}
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
