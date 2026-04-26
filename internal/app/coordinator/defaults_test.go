package coordinator

import (
	"strings"
	"testing"
	"time"

	"github.com/notpop/hearth/pkg/job"
)

func TestValidateKindAccepts(t *testing.T) {
	for _, k := range []string{"img2pdf", "img.pdf", "media-transcode", "a", "x_y", "a1.b2.c3", "single-letter"} {
		if err := validateKind(k); err != nil {
			t.Errorf("validateKind(%q) = %v, want nil", k, err)
		}
	}
}

func TestValidateKindRejects(t *testing.T) {
	cases := map[string]string{
		"":               "empty",
		"with space":     "must match",
		"UPPER":          "must match", // disallowed: uppercase
		"comma,kind":     "must match",
		"slash/kind":     "must match",
		"colon:kind":     "must match",
		strings.Repeat("a", job.MaxKindLength+1): "must be ≤",
	}
	for k, wantSubstr := range cases {
		err := validateKind(k)
		if err == nil {
			t.Errorf("validateKind(%q) = nil, want error", k)
			continue
		}
		if !strings.Contains(err.Error(), wantSubstr) {
			t.Errorf("validateKind(%q) error = %q; want substring %q", k, err.Error(), wantSubstr)
		}
	}
}

func TestApplySpecDefaults(t *testing.T) {
	in := job.Spec{Kind: "k"}
	out := applySpecDefaults(in)
	if out.MaxAttempts != DefaultMaxAttempts {
		t.Errorf("MaxAttempts = %d, want %d", out.MaxAttempts, DefaultMaxAttempts)
	}
	if out.LeaseTTL != DefaultLeaseTTL {
		t.Errorf("LeaseTTL = %v, want %v", out.LeaseTTL, DefaultLeaseTTL)
	}
	if out.Backoff != DefaultBackoff {
		t.Errorf("Backoff = %+v, want %+v", out.Backoff, DefaultBackoff)
	}
}

func TestApplySpecDefaultsKeepsExplicit(t *testing.T) {
	in := job.Spec{
		Kind:        "k",
		MaxAttempts: 7,
		LeaseTTL:    5 * time.Second,
		Backoff:     job.BackoffPolicy{Initial: 2 * time.Second, Max: 10 * time.Second, Multiplier: 3, Jitter: 0.5},
	}
	out := applySpecDefaults(in)
	if out.MaxAttempts != 7 {
		t.Errorf("MaxAttempts changed: %d", out.MaxAttempts)
	}
	if out.LeaseTTL != 5*time.Second {
		t.Errorf("LeaseTTL changed: %v", out.LeaseTTL)
	}
	if out.Backoff.Multiplier != 3 || out.Backoff.Jitter != 0.5 {
		t.Errorf("explicit backoff modified: %+v", out.Backoff)
	}
}

func TestApplySpecDefaultsNegativeMaxAttempts(t *testing.T) {
	// Negative is valid (= unbounded retry); should be preserved.
	in := job.Spec{Kind: "k", MaxAttempts: -1}
	out := applySpecDefaults(in)
	if out.MaxAttempts != -1 {
		t.Errorf("negative MaxAttempts should be preserved, got %d", out.MaxAttempts)
	}
}

func TestApplyBackoffDefaultsPartial(t *testing.T) {
	// Multiplier=0 with other fields set: should fall back to default Multiplier
	// while preserving the user's other values.
	in := job.BackoffPolicy{Initial: 5 * time.Second, Max: 60 * time.Second, Multiplier: 0, Jitter: 0.2}
	out := applyBackoffDefaults(in)
	if out.Multiplier != DefaultBackoff.Multiplier {
		t.Errorf("Multiplier = %v, want %v", out.Multiplier, DefaultBackoff.Multiplier)
	}
	if out.Initial != 5*time.Second {
		t.Errorf("Initial reset: %v", out.Initial)
	}
	if out.Jitter != 0.2 {
		t.Errorf("Jitter reset: %v", out.Jitter)
	}
}

func TestApplyBackoffDefaultsZeroJitterPreserved(t *testing.T) {
	in := job.BackoffPolicy{Initial: time.Second, Max: time.Minute, Multiplier: 2, Jitter: 0}
	out := applyBackoffDefaults(in)
	if out.Jitter != 0 {
		t.Errorf("Jitter=0 should be preserved (means no jitter), got %v", out.Jitter)
	}
}
