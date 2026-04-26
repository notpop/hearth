package job_test

import (
	"testing"

	"github.com/notpop/hearth/pkg/job"
)

func TestStateString(t *testing.T) {
	cases := []struct {
		s    job.State
		want string
	}{
		{job.StateQueued, "queued"},
		{job.StateLeased, "leased"},
		{job.StateSucceeded, "succeeded"},
		{job.StateFailed, "failed"},
		{job.StateCancelled, "cancelled"},
		{job.StateUnknown, "unknown"},
		{job.State(99), "unknown"},
	}
	for _, c := range cases {
		if got := c.s.String(); got != c.want {
			t.Errorf("State(%d).String() = %q, want %q", c.s, got, c.want)
		}
	}
}

func TestStateIsTerminal(t *testing.T) {
	terminal := []job.State{job.StateSucceeded, job.StateFailed, job.StateCancelled}
	nonTerminal := []job.State{job.StateUnknown, job.StateQueued, job.StateLeased}

	for _, s := range terminal {
		if !s.IsTerminal() {
			t.Errorf("%v.IsTerminal() = false, want true", s)
		}
	}
	for _, s := range nonTerminal {
		if s.IsTerminal() {
			t.Errorf("%v.IsTerminal() = true, want false", s)
		}
	}
}

func TestStateIsActive(t *testing.T) {
	active := []job.State{job.StateQueued, job.StateLeased}
	inactive := []job.State{job.StateUnknown, job.StateSucceeded, job.StateFailed, job.StateCancelled}
	for _, s := range active {
		if !s.IsActive() {
			t.Errorf("%v.IsActive() = false, want true", s)
		}
	}
	for _, s := range inactive {
		if s.IsActive() {
			t.Errorf("%v.IsActive() = true, want false", s)
		}
	}
}

func TestStateIsRetryable(t *testing.T) {
	if !job.StateFailed.IsRetryable() {
		t.Errorf("StateFailed.IsRetryable() = false, want true")
	}
	for _, s := range []job.State{job.StateUnknown, job.StateQueued, job.StateLeased, job.StateSucceeded, job.StateCancelled} {
		if s.IsRetryable() {
			t.Errorf("%v.IsRetryable() = true, want false", s)
		}
	}
}
