// Package retry contains pure backoff math.
//
// Randomness is injected as a [0,1) sample so the function stays deterministic
// and trivially testable. Callers (the app layer) source the sample from
// math/rand or crypto/rand at the boundary.
package retry

import (
	"math"
	"time"

	"github.com/notpop/hearth/pkg/job"
)

// NextDelay computes the wait before the next attempt using exponential
// backoff with optional symmetric jitter.
//
//	delay = clamp(initial * multiplier^(attempt-1), 0, max)
//	delay = delay * (1 + jitter*(2*rand01 - 1))
//
// attempt is 1-based: attempt=1 returns ~initial, attempt=2 returns
// ~initial*multiplier, etc.
//
// rand01 must be in [0,1); pass 0.5 for "no jitter" determinism.
func NextDelay(p job.BackoffPolicy, attempt int, rand01 float64) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	mult := p.Multiplier
	if mult <= 0 {
		mult = 1
	}

	base := float64(p.Initial) * math.Pow(mult, float64(attempt-1))
	if max := float64(p.Max); max > 0 && base > max {
		base = max
	}

	if p.Jitter > 0 {
		jitter := p.Jitter
		if jitter > 1 {
			jitter = 1
		}
		base *= 1 + jitter*(2*rand01-1)
	}

	if base < 0 {
		base = 0
	}
	return time.Duration(base)
}
