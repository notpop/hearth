package coordinator_test

import (
	"context"
	"testing"
	"time"

	"github.com/notpop/hearth/internal/adapter/store/memstore"
	"github.com/notpop/hearth/internal/app"
	"github.com/notpop/hearth/internal/app/coordinator"
	"github.com/notpop/hearth/pkg/job"
)

func TestList(t *testing.T) {
	store := memstore.New()
	c := coordinator.New(coordinator.Options{Store: store})

	for _, k := range []string{"a", "b", "a"} {
		if _, err := c.Submit(context.Background(), job.Spec{Kind: k, MaxAttempts: 1, LeaseTTL: time.Second}); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}

	all, err := c.List(context.Background(), app.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("len = %d, want 3", len(all))
	}

	filtered, _ := c.List(context.Background(), app.ListFilter{Kinds: []string{"a"}})
	if len(filtered) != 2 {
		t.Errorf("filtered = %d, want 2", len(filtered))
	}
}

func TestComplete(t *testing.T) {
	c := coordinator.New(coordinator.Options{Store: memstore.New()})
	id, _ := c.Submit(context.Background(), job.Spec{Kind: "k", MaxAttempts: 1, LeaseTTL: time.Second})
	leased, _, _ := c.Lease(context.Background(), []string{"k"}, "w1", time.Second, 0)

	if err := c.Complete(context.Background(), leased.ID, "w1", job.Result{Payload: []byte("done")}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	got, _ := c.Get(context.Background(), id)
	if got.State != job.StateSucceeded || string(got.Result.Payload) != "done" {
		t.Errorf("got %+v", got)
	}
}
