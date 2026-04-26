package workerrt

import (
	"sync/atomic"

	"github.com/notpop/hearth/internal/app"
	"github.com/notpop/hearth/pkg/job"
)

// progressTracker holds the latest progress reported by a handler. The
// handler goroutine writes via report; the heartbeat goroutine reads via
// snapshot. atomic.Pointer guarantees that readers always see a fully
// constructed value (never a torn struct).
type progressTracker struct {
	clock app.Clock
	cur   atomic.Pointer[job.Progress]
}

func newProgressTracker(clock app.Clock) *progressTracker {
	return &progressTracker{clock: clock}
}

// report is the function exposed to handlers as worker.Input.Report.
func (t *progressTracker) report(percent float64, message string) {
	if percent < 0 {
		percent = 0
	}
	if percent > 1 {
		percent = 1
	}
	t.cur.Store(&job.Progress{
		Percent:    percent,
		Message:    message,
		ReportedAt: t.clock.Now(),
	})
}

// snapshot returns the latest report, or nil if nothing has been
// reported yet. The returned pointer is owned by the caller (never
// mutated by the tracker after store).
func (t *progressTracker) snapshot() *job.Progress {
	return t.cur.Load()
}
