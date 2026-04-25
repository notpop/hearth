package retry_test

import (
	"testing"
	"time"

	"github.com/notpop/hearth/internal/domain/retry"
	"github.com/notpop/hearth/pkg/job"
)

func TestNextDelay(t *testing.T) {
	p := job.BackoffPolicy{
		Initial:    time.Second,
		Max:        time.Minute,
		Multiplier: 2,
	}

	cases := []struct {
		name    string
		policy  job.BackoffPolicy
		attempt int
		rand01  float64
		want    time.Duration
	}{
		{"attempt1 no jitter", p, 1, 0.5, time.Second},
		{"attempt2 doubles", p, 2, 0.5, 2 * time.Second},
		{"attempt3 quadruples", p, 3, 0.5, 4 * time.Second},
		{"clamped to max", p, 20, 0.5, time.Minute},
		{"attempt<1 normalised", p, 0, 0.5, time.Second},
		{
			name:    "jitter low",
			policy:  job.BackoffPolicy{Initial: time.Second, Max: time.Minute, Multiplier: 1, Jitter: 0.5},
			attempt: 1, rand01: 0,
			want: 500 * time.Millisecond,
		},
		{
			name:    "jitter high",
			policy:  job.BackoffPolicy{Initial: time.Second, Max: time.Minute, Multiplier: 1, Jitter: 0.5},
			attempt: 1, rand01: 1,
			want: 1500 * time.Millisecond,
		},
		{
			name:    "zero multiplier treated as 1",
			policy:  job.BackoffPolicy{Initial: time.Second, Multiplier: 0},
			attempt: 5, rand01: 0.5,
			want: time.Second,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := retry.NextDelay(tc.policy, tc.attempt, tc.rand01)
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
