// Package worker is the only API surface a Hearth user must implement.
//
// A Handler is a pure unit of computation: it receives an Input (small payload
// plus optional blob readers) and returns an Output. Handlers should be
// idempotent — Hearth may re-deliver a job after a crash or lease expiry, and
// is permitted to discard duplicate completions.
package worker

import (
	"context"
	"io"

	"github.com/notpop/hearth/pkg/job"
)

// Handler is implemented by users to plug their own task logic into Hearth.
type Handler interface {
	// Kind is the routing key the coordinator matches against job.Spec.Kind.
	Kind() string

	// Handle executes one attempt of a job. Returning a non-nil error triggers
	// the configured retry/backoff policy; returning nil marks the job done.
	//
	// Implementations MUST honour ctx cancellation; the runtime cancels ctx
	// when a lease can no longer be extended.
	Handle(ctx context.Context, in Input) (Output, error)
}

// Input is delivered to Handle for a single attempt.
type Input struct {
	JobID   job.ID
	Kind    string
	Attempt int
	Payload []byte
	Blobs   []InputBlob

	// Report publishes the handler's current progress to the
	// coordinator; it is piggybacked on the next heartbeat so it does
	// not generate per-call wire traffic. May be nil — treat as a no-op.
	//
	// Percent is clamped to [0, 1]. Message is free-form and may be
	// empty. The latest call wins; older reports are overwritten by
	// newer ones before transmission, so calling Report in a tight
	// loop is safe.
	Report ReportFunc
}

// ReportFunc is the signature of Input.Report.
type ReportFunc func(percent float64, message string)

// InputBlob exposes a blob attached to the job. Open is supplied by the
// runtime; the worker is responsible for closing the returned reader.
type InputBlob struct {
	Ref  job.BlobRef
	Open func() (io.ReadCloser, error)
}

// Output is what a Handler returns on success.
type Output struct {
	Payload []byte
	Blobs   []OutputBlob
}

// OutputBlob is a blob the worker wants the runtime to persist. The runtime
// reads from Reader, computes the SHA-256, stores it, and records a BlobRef
// in the job result. Size is an optional hint used for progress accounting.
type OutputBlob struct {
	Reader io.Reader
	Size   int64
}
