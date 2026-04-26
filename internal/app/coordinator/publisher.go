package coordinator

import (
	"sync"

	"github.com/notpop/hearth/pkg/job"
)

// publisher is an in-memory fan-out for job updates. It is intentionally
// trivial: home-scale workloads see at most a handful of subscribers, and
// the publisher only carries fresh-from-store Job snapshots — so backpressure
// is handled by dropping updates for slow consumers (the next state change
// will still arrive). For watch UIs this is correct semantics: rendering the
// latest state is what matters, intermediate frames are nice-to-have.
type publisher struct {
	mu   sync.Mutex
	subs map[job.ID]map[*Subscription]struct{}
}

func newPublisher() *publisher {
	return &publisher{subs: make(map[job.ID]map[*Subscription]struct{})}
}

// Subscription delivers Job updates for a particular job id. The channel is
// closed when Unsubscribe is called.
type Subscription struct {
	id    job.ID
	ch    chan job.Job
	owner *publisher
	once  sync.Once
}

// Updates returns the receive-only channel.
func (s *Subscription) Updates() <-chan job.Job { return s.ch }

// Unsubscribe is idempotent and safe to call from multiple goroutines.
func (s *Subscription) Unsubscribe() {
	s.once.Do(func() {
		p := s.owner
		p.mu.Lock()
		if set, ok := p.subs[s.id]; ok {
			delete(set, s)
			if len(set) == 0 {
				delete(p.subs, s.id)
			}
		}
		close(s.ch)
		p.mu.Unlock()
	})
}

// Subscribe registers a new listener for updates to id.
func (p *publisher) Subscribe(id job.ID) *Subscription {
	s := &Subscription{
		id:    id,
		ch:    make(chan job.Job, 8),
		owner: p,
	}
	p.mu.Lock()
	if p.subs[id] == nil {
		p.subs[id] = make(map[*Subscription]struct{})
	}
	p.subs[id][s] = struct{}{}
	p.mu.Unlock()
	return s
}

// Publish fans j out to every active subscription for j.ID. Slow consumers
// have updates dropped (non-blocking send) so a single stuck client cannot
// stall the coordinator.
func (p *publisher) Publish(j job.Job) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for s := range p.subs[j.ID] {
		select {
		case s.ch <- j:
		default:
		}
	}
}
