package jobsm_test

import (
	"errors"
	"testing"
	"time"

	"github.com/notpop/hearth/internal/domain/jobsm"
	"github.com/notpop/hearth/pkg/job"
)

var (
	t0  = time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	ttl = 30 * time.Second
)

func newQueued() job.Job {
	return job.Job{
		ID:    "j1",
		State: job.StateQueued,
		Spec: job.Spec{
			Kind:        "test",
			MaxAttempts: 3,
			LeaseTTL:    ttl,
		},
		CreatedAt: t0,
		UpdatedAt: t0,
	}
}

func TestLease(t *testing.T) {
	t.Run("queued -> leased", func(t *testing.T) {
		j := newQueued()
		out, err := jobsm.Lease(j, jobsm.LeaseInput{WorkerID: "w1", Now: t0, TTL: ttl})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if out.State != job.StateLeased {
			t.Errorf("state = %v, want leased", out.State)
		}
		if out.Attempt != 1 {
			t.Errorf("attempt = %d, want 1", out.Attempt)
		}
		if out.Lease == nil || out.Lease.WorkerID != "w1" {
			t.Errorf("lease = %+v", out.Lease)
		}
		if !out.Lease.ExpiresAt.Equal(t0.Add(ttl)) {
			t.Errorf("expiresAt = %v", out.Lease.ExpiresAt)
		}
	})

	t.Run("rejects non-queued", func(t *testing.T) {
		j := newQueued()
		j.State = job.StateLeased
		_, err := jobsm.Lease(j, jobsm.LeaseInput{WorkerID: "w1", Now: t0, TTL: ttl})
		if !errors.Is(err, jobsm.ErrInvalidTransition) {
			t.Errorf("err = %v, want ErrInvalidTransition", err)
		}
	})
}

func TestHeartbeat(t *testing.T) {
	leased, _ := jobsm.Lease(newQueued(), jobsm.LeaseInput{WorkerID: "w1", Now: t0, TTL: ttl})

	t.Run("extends expiry", func(t *testing.T) {
		later := t0.Add(10 * time.Second)
		out, err := jobsm.Heartbeat(leased, "w1", later, ttl)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if !out.Lease.ExpiresAt.Equal(later.Add(ttl)) {
			t.Errorf("expiresAt = %v", out.Lease.ExpiresAt)
		}
	})

	t.Run("rejects wrong worker", func(t *testing.T) {
		_, err := jobsm.Heartbeat(leased, "imposter", t0, ttl)
		if !errors.Is(err, jobsm.ErrInvalidTransition) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("rejects when not leased", func(t *testing.T) {
		_, err := jobsm.Heartbeat(newQueued(), "w1", t0, ttl)
		if !errors.Is(err, jobsm.ErrInvalidTransition) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestComplete(t *testing.T) {
	leased, _ := jobsm.Lease(newQueued(), jobsm.LeaseInput{WorkerID: "w1", Now: t0, TTL: ttl})

	t.Run("succeeds", func(t *testing.T) {
		res := job.Result{Payload: []byte("ok")}
		out, err := jobsm.Complete(leased, "w1", res, t0.Add(time.Second))
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if out.State != job.StateSucceeded {
			t.Errorf("state = %v", out.State)
		}
		if out.Result == nil || string(out.Result.Payload) != "ok" {
			t.Errorf("result = %+v", out.Result)
		}
		if out.Lease != nil {
			t.Errorf("lease should be cleared")
		}
	})

	t.Run("rejects wrong worker", func(t *testing.T) {
		_, err := jobsm.Complete(leased, "imposter", job.Result{}, t0)
		if !errors.Is(err, jobsm.ErrInvalidTransition) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestFail(t *testing.T) {
	t.Run("retries when attempts remain", func(t *testing.T) {
		leased, _ := jobsm.Lease(newQueued(), jobsm.LeaseInput{WorkerID: "w1", Now: t0, TTL: ttl})
		// attempt=1, max=3 → still retryable
		now := t0.Add(time.Second)
		out, dec, err := jobsm.Fail(leased, "w1", "boom", now, 5*time.Second)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if out.State != job.StateQueued {
			t.Errorf("state = %v, want queued", out.State)
		}
		if dec.NextState != job.StateQueued {
			t.Errorf("dec.NextState = %v", dec.NextState)
		}
		if !out.NextRunAt.Equal(now.Add(5 * time.Second)) {
			t.Errorf("nextRunAt = %v", out.NextRunAt)
		}
		if out.LastError != "boom" {
			t.Errorf("lastError = %q", out.LastError)
		}
		if out.Lease != nil {
			t.Errorf("lease should be cleared")
		}
	})

	t.Run("terminates on max attempts", func(t *testing.T) {
		j := newQueued()
		j.Attempt = 3 // already exhausted; lease will set attempt=... wait we lease first
		// Set up: attempt=2, lease => attempt becomes 3 = max
		j.Attempt = 2
		leased, _ := jobsm.Lease(j, jobsm.LeaseInput{WorkerID: "w1", Now: t0, TTL: ttl})
		out, dec, err := jobsm.Fail(leased, "w1", "final", t0, 5*time.Second)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if out.State != job.StateFailed {
			t.Errorf("state = %v, want failed", out.State)
		}
		if dec.NextState != job.StateFailed {
			t.Errorf("dec.NextState = %v", dec.NextState)
		}
	})
}

func TestReclaimExpired(t *testing.T) {
	leased, _ := jobsm.Lease(newQueued(), jobsm.LeaseInput{WorkerID: "w1", Now: t0, TTL: ttl})

	t.Run("not yet expired", func(t *testing.T) {
		_, _, ok := jobsm.ReclaimExpired(leased, t0.Add(ttl-time.Second), time.Second)
		if ok {
			t.Errorf("should not reclaim before expiry")
		}
	})

	t.Run("expired requeues", func(t *testing.T) {
		past := t0.Add(ttl + time.Second)
		out, dec, ok := jobsm.ReclaimExpired(leased, past, 2*time.Second)
		if !ok {
			t.Fatalf("should reclaim")
		}
		if out.State != job.StateQueued {
			t.Errorf("state = %v", out.State)
		}
		if dec.NextState != job.StateQueued {
			t.Errorf("dec.NextState = %v", dec.NextState)
		}
		if out.LastError == "" {
			t.Errorf("lastError should be set")
		}
	})
}

func TestCancel(t *testing.T) {
	t.Run("cancels queued", func(t *testing.T) {
		out, err := jobsm.Cancel(newQueued(), t0)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if out.State != job.StateCancelled {
			t.Errorf("state = %v", out.State)
		}
	})

	t.Run("rejects terminal", func(t *testing.T) {
		j := newQueued()
		j.State = job.StateSucceeded
		_, err := jobsm.Cancel(j, t0)
		if !errors.Is(err, jobsm.ErrInvalidTransition) {
			t.Errorf("err = %v", err)
		}
	})
}
