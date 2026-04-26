// Package job defines the public domain types that flow through Hearth.
//
// These types are part of the stable public API: client SDKs, custom Store
// implementations, and worker handlers all depend on them. The types are pure
// data; behavior (state transitions, retry math, lease expiry) lives in
// internal/domain so the public surface stays minimal and decoupled.
package job

import "time"

// ID is an opaque, globally-unique job identifier.
type ID string

// State is the lifecycle position of a Job.
//
// State values progress through the lifecycle:
//
//	Queued ── lease ──> Leased ── complete ──> Succeeded
//	   ▲                  │
//	   │                  ├── fail (retries left) ──> Queued
//	   │                  │
//	   │                  └── fail (exhausted) ──> Failed
//	   │
//	   └── cancel from any non-terminal ─────> Cancelled
//
// Use the predicate methods (IsTerminal, IsActive, IsRetryable) rather than
// comparing to const values directly when you can — that way new states
// added in the future inherit reasonable behaviour.
type State int

const (
	// StateUnknown is the zero value and indicates a state that has not
	// been set or could not be parsed. Production stores never persist
	// this value; if you see it in a Job, something deserialised wrong.
	StateUnknown State = iota

	// StateQueued means the job is waiting for a worker to lease it.
	// Either the job has never been picked up, or a previous attempt
	// failed and the backoff has elapsed.
	StateQueued

	// StateLeased means a worker currently owns the job and is expected
	// to complete it before its Lease.ExpiresAt. The coordinator's
	// reclaim sweeper transitions stale leases back to Queued.
	StateLeased

	// StateSucceeded is terminal: a worker reported success and Result
	// holds the output.
	StateSucceeded

	// StateFailed is terminal: the job exhausted MaxAttempts. LastError
	// holds the last reported error.
	StateFailed

	// StateCancelled is terminal: a user requested cancellation while
	// the job was still in a non-terminal state.
	StateCancelled
)

// String returns the canonical lowercase name for s.
func (s State) String() string {
	switch s {
	case StateQueued:
		return "queued"
	case StateLeased:
		return "leased"
	case StateSucceeded:
		return "succeeded"
	case StateFailed:
		return "failed"
	case StateCancelled:
		return "cancelled"
	default:
		return "unknown"
	}
}

// IsTerminal reports whether s is a final state — Succeeded, Failed, or
// Cancelled. Once a Job reaches a terminal state, it never transitions
// again.
func (s State) IsTerminal() bool {
	return s == StateSucceeded || s == StateFailed || s == StateCancelled
}

// IsActive reports whether the coordinator may still touch the job — i.e.
// it is Queued (awaiting a worker) or Leased (a worker is running it).
// Equivalent to !IsTerminal() but spells the intent out.
func (s State) IsActive() bool {
	return s == StateQueued || s == StateLeased
}

// IsRetryable reports whether s is a Failed state caused by retry
// exhaustion (vs Cancelled, which is user-driven and not retryable).
// Useful when surfacing errors in a UI: "this job failed, try again?"
// makes sense for IsRetryable but not for Cancelled.
func (s State) IsRetryable() bool {
	return s == StateFailed
}

// BlobRef points at a content-addressed blob held by a BlobStore. The
// blob is identified by its SHA-256 — two callers that produce the same
// bytes get the same ref and storage is shared.
type BlobRef struct {
	SHA256 string
	Size   int64
}

// Lease records the worker that currently owns a Job and when its claim expires.
type Lease struct {
	WorkerID  string
	LeasedAt  time.Time
	ExpiresAt time.Time
}

// Result is what a worker returns on successful completion.
type Result struct {
	Payload []byte
	Blobs   []BlobRef
}

// Progress is a snapshot of a running job's progress, reported by the
// worker via its handler's Input.Report callback. The coordinator stores
// the latest snapshot per job and exposes it on Job.Progress.
type Progress struct {
	// Percent is in [0, 1]. Implementations should clamp on report.
	Percent float64
	// Message is a free-form human-readable description such as
	// "page 5 of 10" or "encoding chunk 3/4".
	Message string
	// ReportedAt is the worker-side wall-clock time when the report
	// was generated. Useful for staleness detection in UIs.
	ReportedAt time.Time
}

// BackoffPolicy describes exponential-with-jitter retry timing applied
// after each failed attempt. The next-attempt delay is computed as:
//
//	delay = clamp(Initial * Multiplier^(attempt-1), 0, Max)
//	delay *= 1 + Jitter * (2*rand01 - 1)   // symmetric jitter
//
// Zero value (BackoffPolicy{}) is treated as the default policy:
// Initial=1s, Max=60s, Multiplier=2, Jitter=0.1. To opt out of backoff
// entirely, set Initial=0 and Multiplier=1 explicitly.
type BackoffPolicy struct {
	Initial    time.Duration
	Max        time.Duration
	Multiplier float64
	Jitter     float64 // 0..1
}

// Spec is the immutable, user-supplied description of work to do.
//
// Each field has a useful zero value (the coordinator fills sensible
// defaults), so the minimal Spec is `Spec{Kind: "my-kind"}`.
type Spec struct {
	// Kind is the routing key. Workers advertise the kinds they accept
	// via Handler.Kind() and the coordinator only delivers matching
	// kinds. Must match ^[a-z0-9._-]+$.
	Kind string

	// Payload is an inline byte slice delivered to the handler as
	// Input.Payload. Keep it small (≤ a few KB); larger data should go
	// in Blobs to avoid bloating the queue table.
	Payload []byte

	// Blobs are content-addressed inputs the handler can stream from
	// the coordinator's blob store. Use these for MB-class data
	// (images, video chunks, model weights).
	Blobs []BlobRef

	// MaxAttempts is the upper bound on delivery attempts.
	//   0 (zero value) → use the default of 3.
	//   1               → no retry; one shot.
	//   n > 1           → up to n attempts.
	//   negative        → unbounded retry (use with caution).
	MaxAttempts int

	// LeaseTTL is how long a worker holds the job before it must
	// heartbeat to keep it. Zero means use the default of 30s.
	LeaseTTL time.Duration

	// Backoff governs retry timing. Zero value is a sensible default;
	// see BackoffPolicy.
	Backoff BackoffPolicy
}

// Job is the full mutable state held by the coordinator for a single unit
// of work.
type Job struct {
	ID    ID
	Spec  Spec
	State State

	// Attempt is the 1-based attempt counter. The first delivery has
	// Attempt == 1; the n-th has Attempt == n. Compare against
	// Spec.MaxAttempts to know whether a failure here will be terminal
	// (Attempt == MaxAttempts → no further retry).
	Attempt int

	// Lease is set while State == Leased; nil otherwise.
	Lease *Lease

	// Result is set on Succeeded.
	Result *Result

	// Progress is the latest progress snapshot reported by the running
	// worker. Nil while no progress has been reported yet, or after
	// reaching a terminal state without progress reports.
	Progress *Progress

	// LastError holds the most recent failure message; reset on success.
	LastError string

	// NextRunAt is when a backed-off retry becomes eligible to lease.
	// Zero for jobs not currently waiting for backoff.
	NextRunAt time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}
