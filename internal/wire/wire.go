// Package wire converts between domain types (pkg/job, internal/app) and
// the generated protobuf types.
//
// All conversions are pure: they take values, return values, and never
// touch I/O. This keeps the gRPC adapters thin (just call ToProto / FromProto)
// and lets us unit-test the wire schema without spinning up a server.
package wire

import (
	"time"

	hearthv1 "github.com/notpop/hearth/pkg/proto/hearth/v1"

	"github.com/notpop/hearth/internal/app"
	"github.com/notpop/hearth/pkg/job"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// --- State --------------------------------------------------------------

func StateToProto(s job.State) hearthv1.State {
	switch s {
	case job.StateQueued:
		return hearthv1.State_STATE_QUEUED
	case job.StateLeased:
		return hearthv1.State_STATE_LEASED
	case job.StateSucceeded:
		return hearthv1.State_STATE_SUCCEEDED
	case job.StateFailed:
		return hearthv1.State_STATE_FAILED
	case job.StateCancelled:
		return hearthv1.State_STATE_CANCELLED
	default:
		return hearthv1.State_STATE_UNSPECIFIED
	}
}

func StateFromProto(s hearthv1.State) job.State {
	switch s {
	case hearthv1.State_STATE_QUEUED:
		return job.StateQueued
	case hearthv1.State_STATE_LEASED:
		return job.StateLeased
	case hearthv1.State_STATE_SUCCEEDED:
		return job.StateSucceeded
	case hearthv1.State_STATE_FAILED:
		return job.StateFailed
	case hearthv1.State_STATE_CANCELLED:
		return job.StateCancelled
	default:
		return job.StateUnknown
	}
}

// --- Time / Duration helpers -------------------------------------------

func tsToProto(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

func tsFromProto(p *timestamppb.Timestamp) time.Time {
	if p == nil {
		return time.Time{}
	}
	return p.AsTime()
}

func durToProto(d time.Duration) *durationpb.Duration {
	if d == 0 {
		return nil
	}
	return durationpb.New(d)
}

func durFromProto(p *durationpb.Duration) time.Duration {
	if p == nil {
		return 0
	}
	return p.AsDuration()
}

// --- BlobRef ------------------------------------------------------------

func BlobRefToProto(r job.BlobRef) *hearthv1.BlobRef {
	return &hearthv1.BlobRef{Sha256: r.SHA256, Size: r.Size}
}

func BlobRefFromProto(p *hearthv1.BlobRef) job.BlobRef {
	if p == nil {
		return job.BlobRef{}
	}
	return job.BlobRef{SHA256: p.GetSha256(), Size: p.GetSize()}
}

func BlobRefsToProto(rs []job.BlobRef) []*hearthv1.BlobRef {
	if len(rs) == 0 {
		return nil
	}
	out := make([]*hearthv1.BlobRef, len(rs))
	for i, r := range rs {
		out[i] = BlobRefToProto(r)
	}
	return out
}

func BlobRefsFromProto(ps []*hearthv1.BlobRef) []job.BlobRef {
	if len(ps) == 0 {
		return nil
	}
	out := make([]job.BlobRef, len(ps))
	for i, p := range ps {
		out[i] = BlobRefFromProto(p)
	}
	return out
}

// --- BackoffPolicy ------------------------------------------------------

func BackoffToProto(b job.BackoffPolicy) *hearthv1.BackoffPolicy {
	return &hearthv1.BackoffPolicy{
		Initial:    durToProto(b.Initial),
		Max:        durToProto(b.Max),
		Multiplier: b.Multiplier,
		Jitter:     b.Jitter,
	}
}

func BackoffFromProto(p *hearthv1.BackoffPolicy) job.BackoffPolicy {
	if p == nil {
		return job.BackoffPolicy{}
	}
	return job.BackoffPolicy{
		Initial:    durFromProto(p.GetInitial()),
		Max:        durFromProto(p.GetMax()),
		Multiplier: p.GetMultiplier(),
		Jitter:     p.GetJitter(),
	}
}

// --- Spec ---------------------------------------------------------------

func SpecToProto(s job.Spec) *hearthv1.Spec {
	return &hearthv1.Spec{
		Kind:        s.Kind,
		Payload:     s.Payload,
		Blobs:       BlobRefsToProto(s.Blobs),
		MaxAttempts: int32(s.MaxAttempts),
		LeaseTtl:    durToProto(s.LeaseTTL),
		Backoff:     BackoffToProto(s.Backoff),
	}
}

func SpecFromProto(p *hearthv1.Spec) job.Spec {
	if p == nil {
		return job.Spec{}
	}
	return job.Spec{
		Kind:        p.GetKind(),
		Payload:     p.GetPayload(),
		Blobs:       BlobRefsFromProto(p.GetBlobs()),
		MaxAttempts: int(p.GetMaxAttempts()),
		LeaseTTL:    durFromProto(p.GetLeaseTtl()),
		Backoff:     BackoffFromProto(p.GetBackoff()),
	}
}

// --- Lease --------------------------------------------------------------

func LeaseToProto(l *job.Lease) *hearthv1.Lease {
	if l == nil {
		return nil
	}
	return &hearthv1.Lease{
		WorkerId:  l.WorkerID,
		LeasedAt:  tsToProto(l.LeasedAt),
		ExpiresAt: tsToProto(l.ExpiresAt),
	}
}

func LeaseFromProto(p *hearthv1.Lease) *job.Lease {
	if p == nil {
		return nil
	}
	return &job.Lease{
		WorkerID:  p.GetWorkerId(),
		LeasedAt:  tsFromProto(p.GetLeasedAt()),
		ExpiresAt: tsFromProto(p.GetExpiresAt()),
	}
}

// --- Result -------------------------------------------------------------

func ResultToProto(r *job.Result) *hearthv1.Result {
	if r == nil {
		return nil
	}
	return &hearthv1.Result{
		Payload: r.Payload,
		Blobs:   BlobRefsToProto(r.Blobs),
	}
}

func ResultFromProto(p *hearthv1.Result) *job.Result {
	if p == nil {
		return nil
	}
	return &job.Result{
		Payload: p.GetPayload(),
		Blobs:   BlobRefsFromProto(p.GetBlobs()),
	}
}

// --- Job ----------------------------------------------------------------

func JobToProto(j job.Job) *hearthv1.Job {
	return &hearthv1.Job{
		Id:        string(j.ID),
		Spec:      SpecToProto(j.Spec),
		State:     StateToProto(j.State),
		Attempt:   int32(j.Attempt),
		Lease:     LeaseToProto(j.Lease),
		Result:    ResultToProto(j.Result),
		LastError: j.LastError,
		NextRunAt: tsToProto(j.NextRunAt),
		CreatedAt: tsToProto(j.CreatedAt),
		UpdatedAt: tsToProto(j.UpdatedAt),
	}
}

func JobFromProto(p *hearthv1.Job) job.Job {
	if p == nil {
		return job.Job{}
	}
	return job.Job{
		ID:        job.ID(p.GetId()),
		Spec:      SpecFromProto(p.GetSpec()),
		State:     StateFromProto(p.GetState()),
		Attempt:   int(p.GetAttempt()),
		Lease:     LeaseFromProto(p.GetLease()),
		Result:    ResultFromProto(p.GetResult()),
		LastError: p.GetLastError(),
		NextRunAt: tsFromProto(p.GetNextRunAt()),
		CreatedAt: tsFromProto(p.GetCreatedAt()),
		UpdatedAt: tsFromProto(p.GetUpdatedAt()),
	}
}

// --- WorkerInfo ---------------------------------------------------------

func WorkerInfoToProto(w app.WorkerInfo) *hearthv1.WorkerInfo {
	return &hearthv1.WorkerInfo{
		Id:       w.ID,
		Hostname: w.Hostname,
		Os:       w.OS,
		Arch:     w.Arch,
		Kinds:    append([]string(nil), w.Kinds...),
		Version:  w.Version,
		JoinedAt: tsToProto(w.JoinedAt),
		LastSeen: tsToProto(w.LastSeen),
	}
}

func WorkerInfoFromProto(p *hearthv1.WorkerInfo) app.WorkerInfo {
	if p == nil {
		return app.WorkerInfo{}
	}
	return app.WorkerInfo{
		ID:       p.GetId(),
		Hostname: p.GetHostname(),
		OS:       p.GetOs(),
		Arch:     p.GetArch(),
		Kinds:    append([]string(nil), p.GetKinds()...),
		Version:  p.GetVersion(),
		JoinedAt: tsFromProto(p.GetJoinedAt()),
		LastSeen: tsFromProto(p.GetLastSeen()),
	}
}
