package wire_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/notpop/hearth/internal/app"
	"github.com/notpop/hearth/internal/wire"
	"github.com/notpop/hearth/pkg/job"
	hearthv1 "github.com/notpop/hearth/pkg/proto/hearth/v1"
)

var t0 = time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)

func TestStateRoundTrip(t *testing.T) {
	cases := []job.State{
		job.StateUnknown,
		job.StateQueued,
		job.StateLeased,
		job.StateSucceeded,
		job.StateFailed,
		job.StateCancelled,
	}
	for _, s := range cases {
		got := wire.StateFromProto(wire.StateToProto(s))
		if got != s {
			t.Errorf("State %v -> proto -> %v", s, got)
		}
	}
}

func TestStateFromProtoUnspecified(t *testing.T) {
	if got := wire.StateFromProto(hearthv1.State_STATE_UNSPECIFIED); got != job.StateUnknown {
		t.Errorf("unspecified -> %v", got)
	}
	if got := wire.StateFromProto(hearthv1.State(99)); got != job.StateUnknown {
		t.Errorf("invalid -> %v", got)
	}
}

func TestStateToProtoInvalid(t *testing.T) {
	if got := wire.StateToProto(job.State(99)); got != hearthv1.State_STATE_UNSPECIFIED {
		t.Errorf("invalid -> %v", got)
	}
}

func TestBlobRefRoundTrip(t *testing.T) {
	r := job.BlobRef{SHA256: "abc", Size: 42}
	got := wire.BlobRefFromProto(wire.BlobRefToProto(r))
	if got != r {
		t.Errorf("got %+v", got)
	}
}

func TestBlobRefsNilHandling(t *testing.T) {
	if got := wire.BlobRefsToProto(nil); got != nil {
		t.Errorf("nil -> %v", got)
	}
	if got := wire.BlobRefsFromProto(nil); got != nil {
		t.Errorf("nil -> %v", got)
	}
	if got := wire.BlobRefFromProto(nil); got != (job.BlobRef{}) {
		t.Errorf("nil -> %+v", got)
	}
}

func TestBlobRefsRoundTrip(t *testing.T) {
	in := []job.BlobRef{{SHA256: "a", Size: 1}, {SHA256: "b", Size: 2}}
	got := wire.BlobRefsFromProto(wire.BlobRefsToProto(in))
	if !reflect.DeepEqual(got, in) {
		t.Errorf("got %+v", got)
	}
}

func TestBackoffRoundTrip(t *testing.T) {
	in := job.BackoffPolicy{Initial: time.Second, Max: 10 * time.Second, Multiplier: 2, Jitter: 0.5}
	got := wire.BackoffFromProto(wire.BackoffToProto(in))
	if got != in {
		t.Errorf("got %+v", got)
	}
}

func TestBackoffNil(t *testing.T) {
	if got := wire.BackoffFromProto(nil); got != (job.BackoffPolicy{}) {
		t.Errorf("nil -> %+v", got)
	}
}

func TestSpecRoundTrip(t *testing.T) {
	in := job.Spec{
		Kind:        "k",
		Payload:     []byte("p"),
		Blobs:       []job.BlobRef{{SHA256: "x", Size: 1}},
		MaxAttempts: 3,
		LeaseTTL:    30 * time.Second,
		Backoff:     job.BackoffPolicy{Initial: time.Second, Multiplier: 2},
	}
	got := wire.SpecFromProto(wire.SpecToProto(in))
	if !reflect.DeepEqual(got, in) {
		t.Errorf("got %+v want %+v", got, in)
	}
}

func TestSpecNil(t *testing.T) {
	if got := wire.SpecFromProto(nil); !reflect.DeepEqual(got, job.Spec{}) {
		t.Errorf("nil -> %+v", got)
	}
}

func TestLeaseRoundTrip(t *testing.T) {
	in := &job.Lease{WorkerID: "w1", LeasedAt: t0, ExpiresAt: t0.Add(time.Minute)}
	got := wire.LeaseFromProto(wire.LeaseToProto(in))
	if got == nil || *got != *in {
		t.Errorf("got %+v", got)
	}
}

func TestLeaseNil(t *testing.T) {
	if got := wire.LeaseToProto(nil); got != nil {
		t.Errorf("expected nil")
	}
	if got := wire.LeaseFromProto(nil); got != nil {
		t.Errorf("expected nil")
	}
}

func TestResultRoundTrip(t *testing.T) {
	in := &job.Result{Payload: []byte("done"), Blobs: []job.BlobRef{{SHA256: "y", Size: 9}}}
	got := wire.ResultFromProto(wire.ResultToProto(in))
	if got == nil || !reflect.DeepEqual(*got, *in) {
		t.Errorf("got %+v", got)
	}
}

func TestResultNil(t *testing.T) {
	if got := wire.ResultToProto(nil); got != nil {
		t.Errorf("expected nil")
	}
	if got := wire.ResultFromProto(nil); got != nil {
		t.Errorf("expected nil")
	}
}

func TestJobRoundTrip(t *testing.T) {
	in := job.Job{
		ID: "j1",
		Spec: job.Spec{
			Kind:        "k",
			MaxAttempts: 3,
			LeaseTTL:    time.Minute,
		},
		State:     job.StateLeased,
		Attempt:   1,
		Lease:     &job.Lease{WorkerID: "w1", LeasedAt: t0, ExpiresAt: t0.Add(time.Minute)},
		Result:    nil,
		LastError: "previous boom",
		NextRunAt: t0,
		CreatedAt: t0,
		UpdatedAt: t0,
	}
	got := wire.JobFromProto(wire.JobToProto(in))
	if !reflect.DeepEqual(got, in) {
		t.Errorf("got %+v\nwant %+v", got, in)
	}
}

func TestJobFromProtoNil(t *testing.T) {
	if got := wire.JobFromProto(nil); !reflect.DeepEqual(got, job.Job{}) {
		t.Errorf("nil -> %+v", got)
	}
}

func TestTimestampNilZero(t *testing.T) {
	// Through the public API: a Job with zero times yields nil timestamps,
	// and decoding a Job with nil timestamps yields zero times.
	in := job.Job{ID: "j", State: job.StateQueued}
	p := wire.JobToProto(in)
	if p.CreatedAt != nil || p.UpdatedAt != nil || p.NextRunAt != nil {
		t.Errorf("expected nil timestamps for zero times")
	}
	got := wire.JobFromProto(p)
	if !got.CreatedAt.IsZero() || !got.UpdatedAt.IsZero() || !got.NextRunAt.IsZero() {
		t.Errorf("expected zero times")
	}
}

func TestWorkerInfoRoundTrip(t *testing.T) {
	in := app.WorkerInfo{
		ID:       "w1",
		Hostname: "imac",
		OS:       "darwin",
		Arch:     "arm64",
		Kinds:    []string{"a", "b"},
		Version:  "0.0.0-dev",
		JoinedAt: t0,
		LastSeen: t0.Add(time.Minute),
	}
	got := wire.WorkerInfoFromProto(wire.WorkerInfoToProto(in))
	if !reflect.DeepEqual(got, in) {
		t.Errorf("got %+v", got)
	}
}

func TestWorkerInfoNil(t *testing.T) {
	if got := wire.WorkerInfoFromProto(nil); !reflect.DeepEqual(got, app.WorkerInfo{}) {
		t.Errorf("nil -> %+v", got)
	}
}
