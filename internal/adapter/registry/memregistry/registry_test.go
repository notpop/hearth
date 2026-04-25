package memregistry_test

import (
	"context"
	"testing"
	"time"

	"github.com/notpop/hearth/internal/adapter/registry/memregistry"
	"github.com/notpop/hearth/internal/app"
)

func TestRegisterAndList(t *testing.T) {
	r := memregistry.New()
	now := time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC)

	for _, w := range []app.WorkerInfo{
		{ID: "b", LastSeen: now},
		{ID: "a", LastSeen: now},
	} {
		if err := r.Register(context.Background(), w); err != nil {
			t.Fatalf("Register: %v", err)
		}
	}

	got, _ := r.List(context.Background())
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Errorf("got %+v", got)
	}
	if got[0].JoinedAt.IsZero() {
		t.Errorf("JoinedAt should default to LastSeen on first register")
	}
}

func TestRegisterPreservesJoinedAt(t *testing.T) {
	r := memregistry.New()
	first := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	later := first.Add(48 * time.Hour)

	_ = r.Register(context.Background(), app.WorkerInfo{ID: "w", LastSeen: first, JoinedAt: first})
	_ = r.Register(context.Background(), app.WorkerInfo{ID: "w", LastSeen: later})

	got, _ := r.List(context.Background())
	if !got[0].JoinedAt.Equal(first) {
		t.Errorf("JoinedAt = %v, want %v", got[0].JoinedAt, first)
	}
	if !got[0].LastSeen.Equal(later) {
		t.Errorf("LastSeen = %v, want %v", got[0].LastSeen, later)
	}
}

func TestHeartbeat(t *testing.T) {
	r := memregistry.New()
	t0 := time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC)
	_ = r.Register(context.Background(), app.WorkerInfo{ID: "w", LastSeen: t0})

	t1 := t0.Add(time.Minute)
	if err := r.Heartbeat(context.Background(), "w", t1); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	got, _ := r.List(context.Background())
	if !got[0].LastSeen.Equal(t1) {
		t.Errorf("LastSeen = %v", got[0].LastSeen)
	}
}

func TestHeartbeatUnknownIsNoop(t *testing.T) {
	r := memregistry.New()
	if err := r.Heartbeat(context.Background(), "ghost", time.Now()); err != nil {
		t.Errorf("err = %v", err)
	}
}

func TestDeregister(t *testing.T) {
	r := memregistry.New()
	_ = r.Register(context.Background(), app.WorkerInfo{ID: "w", LastSeen: time.Now()})
	if err := r.Deregister(context.Background(), "w"); err != nil {
		t.Fatalf("Deregister: %v", err)
	}
	got, _ := r.List(context.Background())
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}
