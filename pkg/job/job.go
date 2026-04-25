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
type State int

const (
	StateUnknown State = iota
	StateQueued
	StateLeased
	StateSucceeded
	StateFailed
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

// IsTerminal reports whether s is a final state.
func (s State) IsTerminal() bool {
	return s == StateSucceeded || s == StateFailed || s == StateCancelled
}

// BlobRef points at a content-addressed blob held by a BlobStore.
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

// BackoffPolicy describes exponential-with-jitter retry timing.
type BackoffPolicy struct {
	Initial    time.Duration
	Max        time.Duration
	Multiplier float64
	Jitter     float64 // 0..1
}

// Spec is the immutable, user-supplied description of work to do.
type Spec struct {
	Kind        string
	Payload     []byte
	Blobs       []BlobRef
	MaxAttempts int
	LeaseTTL    time.Duration
	Backoff     BackoffPolicy
}

// Job is the full mutable state held by the coordinator for a single unit of work.
type Job struct {
	ID         ID
	Spec       Spec
	State      State
	Attempt    int
	Lease      *Lease
	Result     *Result
	LastError  string
	NextRunAt  time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}
