// Package app holds the orchestration layer. It depends only on the
// interfaces declared in this file (the "ports") and on internal/domain.
//
// Concrete implementations of Store, BlobStore, Clock, and Transport live
// under internal/adapter and are wired together at the cmd/ layer.
//
// This split is what lets the domain be tested without I/O and lets the
// SQLite store be swapped for Badger, an in-memory fake, or anything else
// without rewriting orchestration code.
package app

import (
	"context"
	"io"
	"time"

	"github.com/notpop/hearth/pkg/job"
)

// Clock abstracts wall-clock time so orchestration code can be tested
// deterministically.
type Clock interface {
	Now() time.Time
}

// Store persists Jobs and supports the lease/heartbeat/complete protocol.
//
// Implementations must serialise writes for a given job id; concurrent
// updates are resolved with optimistic concurrency or row-level locks. The
// app layer assumes that successful method returns are durable.
type Store interface {
	// Enqueue persists a freshly-created Job in StateQueued.
	Enqueue(ctx context.Context, j job.Job) error

	// Get retrieves a Job by id.
	Get(ctx context.Context, id job.ID) (job.Job, error)

	// LeaseNext atomically picks one job whose Kind is in kinds and whose
	// NextRunAt <= now, transitions it to StateLeased for workerID, and
	// returns it. If no eligible job exists, ok is false.
	LeaseNext(ctx context.Context, kinds []string, workerID string, ttl time.Duration, now time.Time) (j job.Job, ok bool, err error)

	// Heartbeat extends the lease on (id, workerID). Returns an error if
	// the caller no longer holds the lease.
	Heartbeat(ctx context.Context, id job.ID, workerID string, expiresAt time.Time) error

	// Complete records a successful result and transitions to Succeeded.
	Complete(ctx context.Context, id job.ID, workerID string, res job.Result, now time.Time) error

	// Fail records an attempt failure and transitions to either Queued
	// (with NextRunAt set to retryAt) or Failed (terminal), as decided by
	// the caller.
	Fail(ctx context.Context, id job.ID, workerID string, errMsg string, nextState job.State, retryAt time.Time, now time.Time) error

	// ReclaimExpired sweeps leases whose ExpiresAt < now and reroutes the
	// jobs to Queued (or Failed if attempts are exhausted). Returns the
	// number of jobs reclaimed.
	ReclaimExpired(ctx context.Context, now time.Time) (int, error)

	// Cancel transitions any non-terminal job to Cancelled. If the job is
	// currently Leased, the lease is cleared so the holding worker's next
	// Heartbeat reveals the cancellation.
	Cancel(ctx context.Context, id job.ID, now time.Time) error

	// List returns jobs matching filter, newest first.
	List(ctx context.Context, filter ListFilter) ([]job.Job, error)

	// Close releases store resources.
	Close() error
}

// ListFilter narrows a Store.List query.
type ListFilter struct {
	States []job.State
	Kinds  []string
	Limit  int
}

// BlobStore is content-addressable byte storage. Coordinator and workers
// both read/write through it; the wire transport may be HTTP, gRPC streams,
// or local filesystem when colocated.
type BlobStore interface {
	// Put streams r into storage and returns the resulting BlobRef.
	// Implementations compute SHA-256 as they read.
	Put(ctx context.Context, r io.Reader) (job.BlobRef, error)

	// Get opens a reader for the blob identified by ref.
	Get(ctx context.Context, ref job.BlobRef) (io.ReadCloser, error)

	// Has reports whether the blob is locally present.
	Has(ctx context.Context, ref job.BlobRef) (bool, error)
}

// Transport is the coordinator-side server abstraction. The concrete
// implementation (gRPC over mTLS) lives in internal/adapter/transport.
type Transport interface {
	// Serve runs the transport until ctx is cancelled.
	Serve(ctx context.Context) error
}

// WorkerRegistry tracks live worker connections for status and routing.
// Implementations are typically backed by an in-memory map; durability is
// not required because workers re-register on reconnect.
type WorkerRegistry interface {
	Register(ctx context.Context, info WorkerInfo) error
	Heartbeat(ctx context.Context, workerID string, now time.Time) error
	Deregister(ctx context.Context, workerID string) error
	List(ctx context.Context) ([]WorkerInfo, error)
}

// WorkerInfo is the metadata a worker advertises on connect.
type WorkerInfo struct {
	ID       string
	Hostname string
	OS       string
	Arch     string
	Kinds    []string
	Version  string
	JoinedAt time.Time
	LastSeen time.Time
}
