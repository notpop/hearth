// Package jobsm holds the Job state machine as pure functions.
//
// Every function in this package takes the current Job (and any inputs it
// needs) and returns the next Job — no I/O, no clocks, no goroutines. Time
// values are passed in by the caller. This is what lets the coordinator
// orchestration code be tested without a database or transport.
package jobsm

import (
	"errors"
	"time"

	"github.com/notpop/hearth/pkg/job"
)

// ErrInvalidTransition is returned when a transition is attempted from an
// incompatible state. Callers should treat this as a programming error or a
// concurrency conflict (e.g. two coordinators racing).
var ErrInvalidTransition = errors.New("hearth: invalid state transition")

// LeaseInput captures the inputs needed to lease a queued job.
type LeaseInput struct {
	WorkerID string
	Now      time.Time
	TTL      time.Duration
}

// Lease assigns j to a worker, transitioning Queued -> Leased.
// The attempt counter is incremented so retry math sees the correct number.
func Lease(j job.Job, in LeaseInput) (job.Job, error) {
	if j.State != job.StateQueued {
		return j, ErrInvalidTransition
	}
	j.State = job.StateLeased
	j.Attempt++
	j.Lease = &job.Lease{
		WorkerID:  in.WorkerID,
		LeasedAt:  in.Now,
		ExpiresAt: in.Now.Add(in.TTL),
	}
	j.UpdatedAt = in.Now
	return j, nil
}

// Heartbeat extends an active lease's expiry. The worker must match the
// current lease holder; otherwise this is treated as a stale heartbeat.
func Heartbeat(j job.Job, workerID string, now time.Time, extend time.Duration) (job.Job, error) {
	if j.State != job.StateLeased || j.Lease == nil {
		return j, ErrInvalidTransition
	}
	if j.Lease.WorkerID != workerID {
		return j, ErrInvalidTransition
	}
	j.Lease.ExpiresAt = now.Add(extend)
	j.UpdatedAt = now
	return j, nil
}

// Complete moves a leased job to Succeeded with the worker's result.
func Complete(j job.Job, workerID string, res job.Result, now time.Time) (job.Job, error) {
	if j.State != job.StateLeased || j.Lease == nil {
		return j, ErrInvalidTransition
	}
	if j.Lease.WorkerID != workerID {
		return j, ErrInvalidTransition
	}
	j.State = job.StateSucceeded
	j.Result = &res
	j.Lease = nil
	j.LastError = ""
	j.UpdatedAt = now
	return j, nil
}

// FailDecision describes what should happen after a failed attempt.
type FailDecision struct {
	NextState job.State     // StateQueued (will retry) or StateFailed (terminal)
	NextRunAt time.Time     // when the next attempt becomes eligible
	Backoff   time.Duration // delay used (informational; equal to NextRunAt - Now)
}

// Fail records an attempt failure on j. The retry decision (whether to requeue
// or terminate) is computed here from j.Spec.MaxAttempts and the supplied
// backoff. The function does not call time.Now or random — the caller passes
// the current time and a precomputed backoff so this stays pure.
func Fail(j job.Job, workerID, errMsg string, now time.Time, backoff time.Duration) (job.Job, FailDecision, error) {
	if j.State != job.StateLeased || j.Lease == nil {
		return j, FailDecision{}, ErrInvalidTransition
	}
	if j.Lease.WorkerID != workerID {
		return j, FailDecision{}, ErrInvalidTransition
	}
	j.LastError = errMsg
	j.Lease = nil
	j.UpdatedAt = now

	dec := FailDecision{Backoff: backoff, NextRunAt: now.Add(backoff)}
	if j.Spec.MaxAttempts > 0 && j.Attempt >= j.Spec.MaxAttempts {
		j.State = job.StateFailed
		dec.NextState = job.StateFailed
		dec.NextRunAt = now
		dec.Backoff = 0
	} else {
		j.State = job.StateQueued
		j.NextRunAt = dec.NextRunAt
		dec.NextState = job.StateQueued
	}
	return j, dec, nil
}

// ReclaimExpired returns j moved back to Queued if its lease has expired.
// It mirrors Fail's retry logic so an expired lease counts as a failed attempt.
// If the lease has not expired, the job is returned unchanged with ok=false.
func ReclaimExpired(j job.Job, now time.Time, backoff time.Duration) (out job.Job, dec FailDecision, ok bool) {
	if j.State != job.StateLeased || j.Lease == nil {
		return j, FailDecision{}, false
	}
	if !now.After(j.Lease.ExpiresAt) {
		return j, FailDecision{}, false
	}
	j.LastError = "lease expired"
	j.Lease = nil
	j.UpdatedAt = now

	dec = FailDecision{Backoff: backoff, NextRunAt: now.Add(backoff)}
	if j.Spec.MaxAttempts > 0 && j.Attempt >= j.Spec.MaxAttempts {
		j.State = job.StateFailed
		dec.NextState = job.StateFailed
		dec.NextRunAt = now
		dec.Backoff = 0
	} else {
		j.State = job.StateQueued
		j.NextRunAt = dec.NextRunAt
		dec.NextState = job.StateQueued
	}
	return j, dec, true
}

// Cancel terminates j. Cancellation is allowed from any non-terminal state.
func Cancel(j job.Job, now time.Time) (job.Job, error) {
	if j.State.IsTerminal() {
		return j, ErrInvalidTransition
	}
	j.State = job.StateCancelled
	j.Lease = nil
	j.UpdatedAt = now
	return j, nil
}
