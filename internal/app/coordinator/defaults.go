package coordinator

import (
	"github.com/notpop/hearth/pkg/job"
)

// validateKind is a thin alias kept for symmetry with applySpecDefaults.
func validateKind(kind string) error { return job.ValidateKind(kind) }

// applySpecDefaults fills in the zero-value fields of spec with the
// documented defaults. It does not mutate the caller's value.
func applySpecDefaults(s job.Spec) job.Spec {
	if s.MaxAttempts == 0 {
		s.MaxAttempts = DefaultMaxAttempts
	}
	if s.LeaseTTL <= 0 {
		s.LeaseTTL = DefaultLeaseTTL
	}
	s.Backoff = applyBackoffDefaults(s.Backoff)
	return s
}

// applyBackoffDefaults applies field-level defaults so a partially-set
// BackoffPolicy still yields sensible math.
//
// In particular, Multiplier == 0 would break the exponential formula
// (delay collapses to 0 from attempt 2 onward). We treat 0 as "use the
// default" rather than "do nothing useful".
func applyBackoffDefaults(b job.BackoffPolicy) job.BackoffPolicy {
	if b == (job.BackoffPolicy{}) {
		return DefaultBackoff
	}
	if b.Initial <= 0 {
		b.Initial = DefaultBackoff.Initial
	}
	if b.Max <= 0 {
		b.Max = DefaultBackoff.Max
	}
	if b.Multiplier == 0 {
		b.Multiplier = DefaultBackoff.Multiplier
	}
	// Jitter==0 is legitimate ("no jitter"); don't override.
	return b
}
