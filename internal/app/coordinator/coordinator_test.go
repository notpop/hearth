package coordinator_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/notpop/hearth/internal/adapter/store/memstore"
	"github.com/notpop/hearth/internal/app/coordinator"
	"github.com/notpop/hearth/pkg/job"
)

// fakeClock is a controllable Clock for deterministic tests.
type fakeClock struct {
	now atomic.Int64 // unix nanoseconds
}

func (c *fakeClock) Now() time.Time            { return time.Unix(0, c.now.Load()).UTC() }
func (c *fakeClock) Advance(d time.Duration)   { c.now.Add(int64(d)) }
func (c *fakeClock) Set(t time.Time)           { c.now.Store(t.UnixNano()) }

type harness struct {
	store *memstore.Store
	clock *fakeClock
	coord *coordinator.Coordinator
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	store := memstore.New()
	clock := &fakeClock{}
	clock.Set(time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC))
	idCounter := 0
	c := coordinator.New(coordinator.Options{
		Store:  store,
		Clock:  clock,
		Rand01: func() float64 { return 0.5 },
		NewID: func() job.ID {
			idCounter++
			return job.ID([]byte{'j', byte('0' + idCounter)})
		},
	})
	return &harness{store: store, clock: clock, coord: c}
}

func TestSubmitAndGet(t *testing.T) {
	h := newHarness(t)
	id, err := h.coord.Submit(context.Background(), job.Spec{Kind: "k"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	got, err := h.coord.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != job.StateQueued {
		t.Errorf("state = %v", got.State)
	}
	if got.Spec.MaxAttempts != 1 {
		t.Errorf("max attempts default = %d, want 1", got.Spec.MaxAttempts)
	}
}

func TestSubmitRequiresKind(t *testing.T) {
	h := newHarness(t)
	if _, err := h.coord.Submit(context.Background(), job.Spec{}); err == nil {
		t.Errorf("expected error for empty kind")
	}
}

func TestLeaseLongPoll(t *testing.T) {
	h := newHarness(t)
	id, _ := h.coord.Submit(context.Background(), job.Spec{Kind: "k", MaxAttempts: 3, LeaseTTL: time.Minute})

	leased, ok, err := h.coord.Lease(context.Background(), []string{"k"}, "w1", 0, 0)
	if err != nil || !ok {
		t.Fatalf("Lease: ok=%v err=%v", ok, err)
	}
	if leased.ID != id {
		t.Errorf("id = %q, want %q", leased.ID, id)
	}
	if leased.Lease.WorkerID != "w1" {
		t.Errorf("worker = %q", leased.Lease.WorkerID)
	}
}

func TestHeartbeatExtendsExpiry(t *testing.T) {
	h := newHarness(t)
	_, _ = h.coord.Submit(context.Background(), job.Spec{Kind: "k", LeaseTTL: 10 * time.Second})
	leased, _, _ := h.coord.Lease(context.Background(), []string{"k"}, "w1", 10*time.Second, 0)

	h.clock.Advance(3 * time.Second)
	expires, cancel, err := h.coord.Heartbeat(context.Background(), leased.ID, "w1")
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if cancel {
		t.Errorf("cancel should be false for healthy lease")
	}
	want := h.clock.Now().Add(10 * time.Second)
	if !expires.Equal(want) {
		t.Errorf("expires = %v, want %v", expires, want)
	}
}

func TestFailRetriesThenTerminates(t *testing.T) {
	h := newHarness(t)
	_, _ = h.coord.Submit(context.Background(), job.Spec{
		Kind:        "k",
		MaxAttempts: 2,
		LeaseTTL:    time.Minute,
		Backoff:     job.BackoffPolicy{Initial: time.Second, Multiplier: 1},
	})

	// Attempt 1: lease, fail → expect retry.
	leased, _, _ := h.coord.Lease(context.Background(), []string{"k"}, "w1", time.Minute, 0)
	will, retryAt, err := h.coord.Fail(context.Background(), leased.ID, "w1", "boom1")
	if err != nil {
		t.Fatalf("Fail 1: %v", err)
	}
	if !will {
		t.Errorf("expected retry on attempt 1")
	}
	if retryAt.IsZero() {
		t.Errorf("retryAt should be set")
	}

	h.clock.Advance(2 * time.Second)
	leased, ok, _ := h.coord.Lease(context.Background(), []string{"k"}, "w1", time.Minute, 0)
	if !ok {
		t.Fatalf("expected to lease retry")
	}

	// Attempt 2: fail → terminal.
	will, _, err = h.coord.Fail(context.Background(), leased.ID, "w1", "boom2")
	if err != nil {
		t.Fatalf("Fail 2: %v", err)
	}
	if will {
		t.Errorf("expected terminal on attempt 2")
	}
	got, _ := h.coord.Get(context.Background(), leased.ID)
	if got.State != job.StateFailed {
		t.Errorf("final state = %v", got.State)
	}
}

func TestRunReclaimsExpiredLeases(t *testing.T) {
	h := newHarness(t)
	_, _ = h.coord.Submit(context.Background(), job.Spec{Kind: "k", LeaseTTL: time.Second, MaxAttempts: 3})
	leased, _, _ := h.coord.Lease(context.Background(), []string{"k"}, "w1", time.Second, 0)

	// Advance past expiry; reclaim sweep on the underlying store should re-queue it.
	h.clock.Advance(2 * time.Second)
	n, err := h.store.ReclaimExpired(context.Background(), h.clock.Now())
	if err != nil {
		t.Fatalf("ReclaimExpired: %v", err)
	}
	if n != 1 {
		t.Errorf("reclaimed = %d, want 1", n)
	}
	got, _ := h.coord.Get(context.Background(), leased.ID)
	if got.State != job.StateQueued {
		t.Errorf("state after reclaim = %v", got.State)
	}
}
