// Package memstore is an in-memory implementation of app.Store.
//
// It exists for two reasons:
//   1. App-layer tests can run without sqlite or any disk.
//   2. It is the reference for the Store contract — every behaviour the
//      app layer relies on is exercised here in obvious code, so the SQLite
//      version can be cross-checked against it with shared test fixtures.
package memstore

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/notpop/hearth/internal/app"
	"github.com/notpop/hearth/internal/domain/jobsm"
	"github.com/notpop/hearth/pkg/job"
)

// ErrNotFound is returned when a job id is unknown.
var ErrNotFound = errors.New("memstore: job not found")

// Store is an in-memory app.Store. The zero value is not usable; call New.
type Store struct {
	mu   sync.Mutex
	jobs map[job.ID]job.Job
}

// New constructs an empty Store.
func New() *Store {
	return &Store{jobs: make(map[job.ID]job.Job)}
}

// Enqueue inserts j. Existing ids are replaced — callers are expected to
// generate fresh ids.
func (s *Store) Enqueue(_ context.Context, j job.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j.State == job.StateUnknown {
		j.State = job.StateQueued
	}
	s.jobs[j.ID] = j
	return nil
}

// Get returns the job with the given id.
func (s *Store) Get(_ context.Context, id job.ID) (job.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return job.Job{}, ErrNotFound
	}
	return j, nil
}

// LeaseNext picks the oldest queued job whose Kind matches kinds and whose
// NextRunAt has elapsed, leases it to workerID, and returns it.
func (s *Store) LeaseNext(_ context.Context, kinds []string, workerID string, ttl time.Duration, now time.Time) (job.Job, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	kindSet := make(map[string]struct{}, len(kinds))
	for _, k := range kinds {
		kindSet[k] = struct{}{}
	}

	var picked *job.Job
	for id, j := range s.jobs {
		if j.State != job.StateQueued {
			continue
		}
		if _, ok := kindSet[j.Spec.Kind]; !ok && len(kinds) > 0 {
			continue
		}
		if !j.NextRunAt.IsZero() && j.NextRunAt.After(now) {
			continue
		}
		// Pick the one with the earliest CreatedAt for determinism / fairness.
		if picked == nil || j.CreatedAt.Before(picked.CreatedAt) {
			cp := s.jobs[id]
			picked = &cp
		}
	}

	if picked == nil {
		return job.Job{}, false, nil
	}

	leased, err := jobsm.Lease(*picked, jobsm.LeaseInput{WorkerID: workerID, Now: now, TTL: ttl})
	if err != nil {
		return job.Job{}, false, err
	}
	s.jobs[leased.ID] = leased
	return leased, true, nil
}

// Heartbeat extends the lease's expiry. Errors if (id, workerID) doesn't
// correspond to the current lease holder.
func (s *Store) Heartbeat(_ context.Context, id job.ID, workerID string, expiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return ErrNotFound
	}
	if j.State != job.StateLeased || j.Lease == nil || j.Lease.WorkerID != workerID {
		return jobsm.ErrInvalidTransition
	}
	j.Lease.ExpiresAt = expiresAt
	j.UpdatedAt = expiresAt
	s.jobs[id] = j
	return nil
}

// Complete marks (id, workerID) as Succeeded with res.
func (s *Store) Complete(_ context.Context, id job.ID, workerID string, res job.Result, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return ErrNotFound
	}
	out, err := jobsm.Complete(j, workerID, res, now)
	if err != nil {
		return err
	}
	s.jobs[id] = out
	return nil
}

// Fail records an error and applies the caller-provided retry decision.
func (s *Store) Fail(_ context.Context, id job.ID, workerID string, errMsg string, nextState job.State, retryAt time.Time, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return ErrNotFound
	}
	if j.State != job.StateLeased || j.Lease == nil || j.Lease.WorkerID != workerID {
		return jobsm.ErrInvalidTransition
	}
	j.LastError = errMsg
	j.Lease = nil
	j.UpdatedAt = now
	j.State = nextState
	if nextState == job.StateQueued {
		j.NextRunAt = retryAt
	}
	s.jobs[id] = j
	return nil
}

// ReclaimExpired sweeps expired leases and re-queues / fails them per the
// pure-domain rules. Backoff is fixed at zero here; the SQLite store will do
// the same — proper backoff is applied through Fail by the coordinator.
func (s *Store) ReclaimExpired(_ context.Context, now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, j := range s.jobs {
		out, _, ok := jobsm.ReclaimExpired(j, now, 0)
		if !ok {
			continue
		}
		s.jobs[id] = out
		n++
	}
	return n, nil
}

// List returns jobs matching filter, sorted newest-first by UpdatedAt.
func (s *Store) List(_ context.Context, filter app.ListFilter) ([]job.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	stateSet := make(map[job.State]struct{}, len(filter.States))
	for _, st := range filter.States {
		stateSet[st] = struct{}{}
	}
	kindSet := make(map[string]struct{}, len(filter.Kinds))
	for _, k := range filter.Kinds {
		kindSet[k] = struct{}{}
	}

	var out []job.Job
	for _, j := range s.jobs {
		if len(stateSet) > 0 {
			if _, ok := stateSet[j.State]; !ok {
				continue
			}
		}
		if len(kindSet) > 0 {
			if _, ok := kindSet[j.Spec.Kind]; !ok {
				continue
			}
		}
		out = append(out, j)
	}

	sort.Slice(out, func(i, k int) bool {
		return out[i].UpdatedAt.After(out[k].UpdatedAt)
	})
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

// Close is a no-op.
func (s *Store) Close() error { return nil }
