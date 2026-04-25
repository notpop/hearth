// Package coordinator orchestrates job lifecycle on top of the Store and
// pure-domain helpers. It is the single place where retry policy is
// applied: backoff math (pure) is converted to a NextRunAt timestamp (I/O)
// here, then handed to the Store as data.
//
// All non-determinism (current time, random jitter, id generation) is
// injected through Options so tests can drive the coordinator with
// repeatable inputs.
package coordinator

import (
	crand "crypto/rand"
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	mrand "math/rand/v2"
	"time"

	"github.com/notpop/hearth/internal/app"
	"github.com/notpop/hearth/internal/domain/jobsm"
	"github.com/notpop/hearth/internal/domain/retry"
	"github.com/notpop/hearth/pkg/job"
)

// Options configures a Coordinator. Only Store is required.
type Options struct {
	Store        app.Store
	Clock        app.Clock         // default: wall clock
	NewID        func() job.ID     // default: 16 random bytes hex-encoded
	Rand01       func() float64    // default: math/rand/v2 Float64
	Logger       *slog.Logger      // default: slog.Default()
	ReclaimEvery time.Duration     // default: 5s
	PollTick     time.Duration     // default: 200ms
}

// Coordinator is the orchestration object. It is safe for concurrent use:
// the Store implementation is responsible for serialising writes.
type Coordinator struct {
	store        app.Store
	clock        app.Clock
	newID        func() job.ID
	rand01       func() float64
	log          *slog.Logger
	reclaimEvery time.Duration
	pollTick     time.Duration
}

// New constructs a Coordinator from opt, applying defaults as needed.
func New(opt Options) *Coordinator {
	if opt.Store == nil {
		panic("coordinator: Options.Store is required")
	}
	if opt.Clock == nil {
		opt.Clock = wallClock{}
	}
	if opt.NewID == nil {
		opt.NewID = defaultNewID
	}
	if opt.Rand01 == nil {
		opt.Rand01 = mrand.Float64
	}
	if opt.Logger == nil {
		opt.Logger = slog.Default()
	}
	if opt.ReclaimEvery == 0 {
		opt.ReclaimEvery = 5 * time.Second
	}
	if opt.PollTick == 0 {
		opt.PollTick = 200 * time.Millisecond
	}
	return &Coordinator{
		store:        opt.Store,
		clock:        opt.Clock,
		newID:        opt.NewID,
		rand01:       opt.Rand01,
		log:          opt.Logger,
		reclaimEvery: opt.ReclaimEvery,
		pollTick:     opt.PollTick,
	}
}

// Submit enqueues a fresh job described by spec and returns its id.
func (c *Coordinator) Submit(ctx context.Context, spec job.Spec) (job.ID, error) {
	if spec.Kind == "" {
		return "", errors.New("coordinator: spec.Kind is required")
	}
	if spec.MaxAttempts <= 0 {
		spec.MaxAttempts = 1
	}
	if spec.LeaseTTL <= 0 {
		spec.LeaseTTL = 30 * time.Second
	}
	now := c.clock.Now()
	j := job.Job{
		ID:        c.newID(),
		Spec:      spec,
		State:     job.StateQueued,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := c.store.Enqueue(ctx, j); err != nil {
		return "", err
	}
	return j.ID, nil
}

// Get returns a job by id.
func (c *Coordinator) Get(ctx context.Context, id job.ID) (job.Job, error) {
	return c.store.Get(ctx, id)
}

// List returns jobs matching filter.
func (c *Coordinator) List(ctx context.Context, filter app.ListFilter) ([]job.Job, error) {
	return c.store.List(ctx, filter)
}

// Lease is the worker-side long-poll. It calls LeaseNext repeatedly with a
// short tick until a job is available, pollTimeout elapses, or ctx is done.
// ttl is the requested lease duration; if zero, the spec's LeaseTTL is used
// (resolved by the underlying store).
func (c *Coordinator) Lease(ctx context.Context, kinds []string, workerID string, ttl time.Duration, pollTimeout time.Duration) (job.Job, bool, error) {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	deadline := c.clock.Now().Add(pollTimeout)
	for {
		j, ok, err := c.store.LeaseNext(ctx, kinds, workerID, ttl, c.clock.Now())
		if err != nil {
			return job.Job{}, false, err
		}
		if ok {
			return j, true, nil
		}
		if pollTimeout <= 0 || !c.clock.Now().Before(deadline) {
			return job.Job{}, false, nil
		}
		select {
		case <-ctx.Done():
			return job.Job{}, false, ctx.Err()
		case <-time.After(c.pollTick):
		}
	}
}

// Heartbeat extends the lease on (id, workerID) using the spec's LeaseTTL.
func (c *Coordinator) Heartbeat(ctx context.Context, id job.ID, workerID string) (time.Time, error) {
	j, err := c.store.Get(ctx, id)
	if err != nil {
		return time.Time{}, err
	}
	if j.State != job.StateLeased || j.Lease == nil {
		return time.Time{}, jobsm.ErrInvalidTransition
	}
	expires := c.clock.Now().Add(j.Spec.LeaseTTL)
	if err := c.store.Heartbeat(ctx, id, workerID, expires); err != nil {
		return time.Time{}, err
	}
	return expires, nil
}

// Complete marks the job Succeeded with res.
func (c *Coordinator) Complete(ctx context.Context, id job.ID, workerID string, res job.Result) error {
	return c.store.Complete(ctx, id, workerID, res, c.clock.Now())
}

// Fail records a failed attempt and applies the configured backoff/retry
// policy. willRetry is true if the job was requeued; nextRunAt is when the
// next attempt becomes eligible (zero when terminal).
func (c *Coordinator) Fail(ctx context.Context, id job.ID, workerID string, errMsg string) (willRetry bool, nextRunAt time.Time, err error) {
	j, err := c.store.Get(ctx, id)
	if err != nil {
		return false, time.Time{}, err
	}
	now := c.clock.Now()

	nextState := job.StateQueued
	delay := retry.NextDelay(j.Spec.Backoff, j.Attempt, c.rand01())
	retryAt := now.Add(delay)
	if j.Spec.MaxAttempts > 0 && j.Attempt >= j.Spec.MaxAttempts {
		nextState = job.StateFailed
		retryAt = time.Time{}
	}
	if err := c.store.Fail(ctx, id, workerID, errMsg, nextState, retryAt, now); err != nil {
		return false, time.Time{}, err
	}
	return nextState == job.StateQueued, retryAt, nil
}

// Run drives background maintenance (lease reclamation) until ctx is done.
func (c *Coordinator) Run(ctx context.Context) error {
	t := time.NewTicker(c.reclaimEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			n, err := c.store.ReclaimExpired(ctx, c.clock.Now())
			if err != nil {
				c.log.Error("reclaim expired", "err", err)
				continue
			}
			if n > 0 {
				c.log.Info("reclaimed expired leases", "n", n)
			}
		}
	}
}

// --- defaults -----------------------------------------------------------

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now().UTC() }

func defaultNewID() job.ID {
	var b [16]byte
	if _, err := crand.Read(b[:]); err != nil {
		// crypto/rand failure is catastrophic; fall back to a timestamp.
		return job.ID("nonrandom-" + time.Now().UTC().Format(time.RFC3339Nano))
	}
	return job.ID(hex.EncodeToString(b[:]))
}
