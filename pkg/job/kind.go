package job

import (
	"errors"
	"fmt"
	"regexp"
)

// MaxKindLength is the maximum allowed Kind length. Kinds end up in DB
// rows, log lines, and CLI flag values, so we cap at a sane size.
const MaxKindLength = 64

// kindRegexp restricts Kind to a small lowercase identifier alphabet:
// no whitespace, no commas (CLI parses kind lists by comma), no path
// separators or shell-special characters. Dots are allowed so callers
// can namespace kinds like "img.pdf" or "media.transcode".
var kindRegexp = regexp.MustCompile(`^[a-z0-9._-]+$`)

// ErrInvalidKind is returned by ValidateKind when the supplied kind does
// not satisfy the format rules. Use errors.Is to match.
var ErrInvalidKind = errors.New("hearth: invalid Kind")

// ValidateKind returns nil if kind is a well-formed routing key.
// The error is user-facing and explains exactly what to fix.
//
// Rules:
//   - non-empty
//   - length ≤ MaxKindLength
//   - characters in [a-z0-9._-]
func ValidateKind(kind string) error {
	if kind == "" {
		return fmt.Errorf("%w: empty (set Spec.Kind / Handler.Kind() to a non-empty routing key)", ErrInvalidKind)
	}
	if len(kind) > MaxKindLength {
		return fmt.Errorf("%w: %q is %d chars; must be ≤ %d", ErrInvalidKind, kind, len(kind), MaxKindLength)
	}
	if !kindRegexp.MatchString(kind) {
		return fmt.Errorf("%w: %q must match %s (lowercase letters, digits, dot, dash, underscore)",
			ErrInvalidKind, kind, kindRegexp.String())
	}
	return nil
}
