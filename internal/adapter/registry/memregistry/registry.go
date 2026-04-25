// Package memregistry is an in-memory app.WorkerRegistry. Workers are
// expected to re-register on reconnect, so durability is not required.
package memregistry

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/notpop/hearth/internal/app"
)

// Registry holds the live set of workers.
type Registry struct {
	mu      sync.Mutex
	workers map[string]app.WorkerInfo
}

// New constructs an empty Registry.
func New() *Registry {
	return &Registry{workers: make(map[string]app.WorkerInfo)}
}

// Register adds or replaces info for info.ID. JoinedAt is preserved if the
// worker was already present, otherwise it's set to LastSeen.
func (r *Registry) Register(_ context.Context, info app.WorkerInfo) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.workers[info.ID]; ok && !existing.JoinedAt.IsZero() {
		info.JoinedAt = existing.JoinedAt
	} else if info.JoinedAt.IsZero() {
		info.JoinedAt = info.LastSeen
	}
	r.workers[info.ID] = info
	return nil
}

// Heartbeat updates LastSeen for workerID.
func (r *Registry) Heartbeat(_ context.Context, workerID string, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.workers[workerID]
	if !ok {
		return nil // tolerate heartbeats from re-registering workers
	}
	w.LastSeen = now
	r.workers[workerID] = w
	return nil
}

// Deregister removes workerID from the registry.
func (r *Registry) Deregister(_ context.Context, workerID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.workers, workerID)
	return nil
}

// List returns all known workers, sorted by ID for stable output.
func (r *Registry) List(_ context.Context) ([]app.WorkerInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]app.WorkerInfo, 0, len(r.workers))
	for _, w := range r.workers {
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
