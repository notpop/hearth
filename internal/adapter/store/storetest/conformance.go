// Package storetest exposes a Store conformance suite that any
// implementation can run against itself. memstore and sqlite share these
// tests so the two stay observably identical.
package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/notpop/hearth/internal/app"
	"github.com/notpop/hearth/internal/domain/jobsm"
	"github.com/notpop/hearth/pkg/job"
)

// Factory builds a fresh Store for one test case.
type Factory func(t *testing.T) app.Store

// RunSuite executes every conformance test against the store produced by f.
func RunSuite(t *testing.T, f Factory) {
	t.Helper()
	t.Run("EnqueueGet", func(t *testing.T) { testEnqueueGet(t, f) })
	t.Run("LeaseNextRoundTrip", func(t *testing.T) { testLeaseRoundTrip(t, f) })
	t.Run("LeaseRespectsKinds", func(t *testing.T) { testLeaseRespectsKinds(t, f) })
	t.Run("LeaseRespectsNextRunAt", func(t *testing.T) { testLeaseRespectsNextRunAt(t, f) })
	t.Run("Heartbeat", func(t *testing.T) { testHeartbeat(t, f) })
	t.Run("CompleteRequiresLeaseHolder", func(t *testing.T) { testCompleteRequiresLeaseHolder(t, f) })
	t.Run("FailRequeues", func(t *testing.T) { testFailRequeues(t, f) })
	t.Run("ReclaimExpired", func(t *testing.T) { testReclaimExpired(t, f) })
	t.Run("List", func(t *testing.T) { testList(t, f) })
}

func mustEnqueue(t *testing.T, s app.Store, id job.ID, kind string, now time.Time) job.Job {
	t.Helper()
	j := job.Job{
		ID:    id,
		State: job.StateQueued,
		Spec: job.Spec{
			Kind:        kind,
			MaxAttempts: 3,
			LeaseTTL:    30 * time.Second,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.Enqueue(context.Background(), j); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	return j
}

func testEnqueueGet(t *testing.T, f Factory) {
	s := f(t)
	now := time.Now()
	mustEnqueue(t, s, "j1", "k", now)
	got, err := s.Get(context.Background(), "j1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != "j1" || got.Spec.Kind != "k" {
		t.Errorf("got %+v", got)
	}
}

func testLeaseRoundTrip(t *testing.T, f Factory) {
	s := f(t)
	now := time.Now()
	mustEnqueue(t, s, "j1", "k", now)

	leased, ok, err := s.LeaseNext(context.Background(), []string{"k"}, "w1", 30*time.Second, now)
	if err != nil || !ok {
		t.Fatalf("LeaseNext: ok=%v err=%v", ok, err)
	}
	if leased.State != job.StateLeased || leased.Lease.WorkerID != "w1" {
		t.Errorf("bad lease: %+v", leased)
	}

	// Second lease should find nothing.
	_, ok, err = s.LeaseNext(context.Background(), []string{"k"}, "w2", 30*time.Second, now)
	if err != nil {
		t.Fatalf("LeaseNext (2): %v", err)
	}
	if ok {
		t.Errorf("second LeaseNext should return ok=false")
	}
}

func testLeaseRespectsKinds(t *testing.T, f Factory) {
	s := f(t)
	now := time.Now()
	mustEnqueue(t, s, "j1", "img", now)
	_, ok, err := s.LeaseNext(context.Background(), []string{"video"}, "w1", 30*time.Second, now)
	if err != nil {
		t.Fatalf("LeaseNext: %v", err)
	}
	if ok {
		t.Errorf("worker for 'video' should not get 'img' job")
	}
}

func testLeaseRespectsNextRunAt(t *testing.T, f Factory) {
	s := f(t)
	now := time.Now()
	j := mustEnqueue(t, s, "j1", "k", now)
	j.NextRunAt = now.Add(time.Minute)
	_ = s.Enqueue(context.Background(), j) // overwrite with delayed NextRunAt

	_, ok, _ := s.LeaseNext(context.Background(), []string{"k"}, "w1", 30*time.Second, now)
	if ok {
		t.Errorf("should not lease until NextRunAt elapses")
	}

	_, ok, _ = s.LeaseNext(context.Background(), []string{"k"}, "w1", 30*time.Second, now.Add(2*time.Minute))
	if !ok {
		t.Errorf("should lease once NextRunAt elapsed")
	}
}

func testHeartbeat(t *testing.T, f Factory) {
	s := f(t)
	now := time.Now()
	mustEnqueue(t, s, "j1", "k", now)
	leased, _, _ := s.LeaseNext(context.Background(), []string{"k"}, "w1", 30*time.Second, now)

	newExpiry := now.Add(time.Minute)
	if err := s.Heartbeat(context.Background(), leased.ID, "w1", newExpiry); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	got, _ := s.Get(context.Background(), leased.ID)
	if !got.Lease.ExpiresAt.Equal(newExpiry) {
		t.Errorf("expiresAt = %v, want %v", got.Lease.ExpiresAt, newExpiry)
	}

	// Wrong worker rejected.
	if err := s.Heartbeat(context.Background(), leased.ID, "imposter", newExpiry); !errors.Is(err, jobsm.ErrInvalidTransition) {
		t.Errorf("imposter heartbeat err = %v", err)
	}
}

func testCompleteRequiresLeaseHolder(t *testing.T, f Factory) {
	s := f(t)
	now := time.Now()
	mustEnqueue(t, s, "j1", "k", now)
	leased, _, _ := s.LeaseNext(context.Background(), []string{"k"}, "w1", 30*time.Second, now)

	if err := s.Complete(context.Background(), leased.ID, "imposter", job.Result{}, now); !errors.Is(err, jobsm.ErrInvalidTransition) {
		t.Errorf("imposter complete err = %v", err)
	}
	if err := s.Complete(context.Background(), leased.ID, "w1", job.Result{Payload: []byte("ok")}, now); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	got, _ := s.Get(context.Background(), leased.ID)
	if got.State != job.StateSucceeded {
		t.Errorf("state = %v", got.State)
	}
}

func testFailRequeues(t *testing.T, f Factory) {
	s := f(t)
	now := time.Now()
	mustEnqueue(t, s, "j1", "k", now)
	leased, _, _ := s.LeaseNext(context.Background(), []string{"k"}, "w1", 30*time.Second, now)

	retryAt := now.Add(5 * time.Second)
	if err := s.Fail(context.Background(), leased.ID, "w1", "boom", job.StateQueued, retryAt, now); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	got, _ := s.Get(context.Background(), leased.ID)
	if got.State != job.StateQueued {
		t.Errorf("state = %v", got.State)
	}
	if !got.NextRunAt.Equal(retryAt) {
		t.Errorf("nextRunAt = %v", got.NextRunAt)
	}
	if got.LastError != "boom" {
		t.Errorf("lastError = %q", got.LastError)
	}
}

func testReclaimExpired(t *testing.T, f Factory) {
	s := f(t)
	now := time.Now()
	mustEnqueue(t, s, "j1", "k", now)
	_, _, _ = s.LeaseNext(context.Background(), []string{"k"}, "w1", time.Second, now)

	n, err := s.ReclaimExpired(context.Background(), now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("ReclaimExpired: %v", err)
	}
	if n != 1 {
		t.Errorf("reclaimed = %d, want 1", n)
	}
	got, _ := s.Get(context.Background(), "j1")
	if got.State != job.StateQueued {
		t.Errorf("state after reclaim = %v", got.State)
	}
}

func testList(t *testing.T, f Factory) {
	s := f(t)
	now := time.Now()
	mustEnqueue(t, s, "a", "k1", now)
	mustEnqueue(t, s, "b", "k2", now.Add(time.Second))
	mustEnqueue(t, s, "c", "k1", now.Add(2*time.Second))

	all, err := s.List(context.Background(), app.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("len = %d, want 3", len(all))
	}

	filtered, _ := s.List(context.Background(), app.ListFilter{Kinds: []string{"k1"}})
	if len(filtered) != 2 {
		t.Errorf("kind filter len = %d, want 2", len(filtered))
	}
}
