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
