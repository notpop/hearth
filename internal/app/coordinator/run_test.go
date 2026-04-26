package coordinator_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/notpop/hearth/internal/adapter/store/memstore"
	"github.com/notpop/hearth/internal/app/coordinator"
	"github.com/notpop/hearth/pkg/job"
)

func TestNewAppliesDefaults(t *testing.T) {
	c := coordinator.New(coordinator.Options{Store: memstore.New()})
	if c == nil {
		t.Fatal("coordinator is nil")
	}
}

func TestNewPanicsWithoutStore(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Errorf("expected panic")
		}
	}()
	_ = coordinator.New(coordinator.Options{})
}

func TestRunStopsOnContextCancel(t *testing.T) {
	c := coordinator.New(coordinator.Options{
		Store:        memstore.New(),
		ReclaimEvery: 5 * time.Millisecond,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after cancel")
	}
}

func TestRunReclaimsExpiredViaTicker(t *testing.T) {
	store := memstore.New()
	c := coordinator.New(coordinator.Options{
		Store:        store,
		ReclaimEvery: 5 * time.Millisecond,
		PollTick:     time.Millisecond,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	now := time.Now()
	j := job.Job{
		ID:    "j1",
		State: job.StateLeased,
		Spec: job.Spec{
			Kind:        "k",
			MaxAttempts: 3,
			LeaseTTL:    time.Second,
		},
		Attempt: 1,
		Lease: &job.Lease{
			WorkerID:  "w1",
			LeasedAt:  now.Add(-10 * time.Second),
			ExpiresAt: now.Add(-time.Second),
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.Enqueue(context.Background(), j); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = c.Run(ctx)

	got, _ := store.Get(context.Background(), j.ID)
	if got.State != job.StateQueued {
		t.Errorf("state after reclaim = %v, want queued", got.State)
	}
}

func TestLeaseRespectsContextCancellation(t *testing.T) {
	c := coordinator.New(coordinator.Options{
		Store:    memstore.New(),
		PollTick: 10 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := c.Lease(ctx, []string{"k"}, "w1", 0, 5*time.Second)
		done <- err
	}()
	time.Sleep(15 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Lease did not return after cancel")
	}
}

func TestLeaseReturnsNoneAfterPollTimeout(t *testing.T) {
	c := coordinator.New(coordinator.Options{
		Store:    memstore.New(),
		PollTick: 5 * time.Millisecond,
	})
	_, ok, err := c.Lease(context.Background(), []string{"k"}, "w1", 0, 30*time.Millisecond)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	if ok {
		t.Errorf("expected no job")
	}
}

func TestHeartbeatErrorsWhenNotLeased(t *testing.T) {
	store := memstore.New()
	c := coordinator.New(coordinator.Options{Store: store})
	id, _ := c.Submit(context.Background(), job.Spec{Kind: "k", MaxAttempts: 1, LeaseTTL: time.Second})

	if _, _, err := c.Heartbeat(context.Background(), id, "w1"); err == nil {
		t.Errorf("expected error heartbeating queued job")
	}
}

func TestHeartbeatErrorsForUnknownJob(t *testing.T) {
	c := coordinator.New(coordinator.Options{Store: memstore.New()})
	if _, _, err := c.Heartbeat(context.Background(), "missing", "w1"); err == nil {
		t.Errorf("expected error for missing job")
	}
}

func TestCancelRequestSignalsCancelOnHeartbeat(t *testing.T) {
	store := memstore.New()
	c := coordinator.New(coordinator.Options{Store: store})

	id, _ := c.Submit(context.Background(), job.Spec{Kind: "k", MaxAttempts: 1, LeaseTTL: 30 * time.Second})
	leased, _, _ := c.Lease(context.Background(), []string{"k"}, "w1", 30*time.Second, 0)

	if err := c.Cancel(context.Background(), id); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	expires, cancelReq, err := c.Heartbeat(context.Background(), leased.ID, "w1")
	if err != nil {
		t.Fatalf("Heartbeat after cancel: %v", err)
	}
	if !cancelReq {
		t.Errorf("expected cancelRequested=true after Cancel")
	}
	if !expires.IsZero() {
		t.Errorf("expires should be zero, got %v", expires)
	}
}

func TestFailErrorsForUnknownJob(t *testing.T) {
	c := coordinator.New(coordinator.Options{Store: memstore.New()})
	_, _, err := c.Fail(context.Background(), "missing", "w1", "boom")
	if err == nil {
		t.Errorf("expected error")
	}
}
