package job_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/notpop/hearth/pkg/job"
)

func TestValidateKindAccepts(t *testing.T) {
	for _, k := range []string{"a", "z", "0", "9", "img2pdf", "img.pdf", "media-transcode", "x_y", "a1.b2.c3"} {
		if err := job.ValidateKind(k); err != nil {
			t.Errorf("ValidateKind(%q) = %v, want nil", k, err)
		}
	}
}

func TestValidateKindRejects(t *testing.T) {
	cases := []struct {
		kind string
		want string
	}{
		{"", "empty"},
		{"with space", "must match"},
		{"UPPER", "must match"},
		{"comma,kind", "must match"},
		{"slash/kind", "must match"},
		{"colon:kind", "must match"},
		{strings.Repeat("a", job.MaxKindLength+1), "must be ≤"},
	}
	for _, c := range cases {
		err := job.ValidateKind(c.kind)
		if err == nil {
			t.Errorf("ValidateKind(%q) = nil, want error", c.kind)
			continue
		}
		if !errors.Is(err, job.ErrInvalidKind) {
			t.Errorf("ValidateKind(%q) error not ErrInvalidKind: %v", c.kind, err)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("ValidateKind(%q) message %q missing substring %q", c.kind, err.Error(), c.want)
		}
	}
}
