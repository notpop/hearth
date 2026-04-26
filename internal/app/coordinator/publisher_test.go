package coordinator_test

import (
	"context"
	"testing"
	"time"

	"github.com/notpop/hearth/internal/adapter/store/memstore"
	"github.com/notpop/hearth/internal/app/coordinator"
	"github.com/notpop/hearth/pkg/job"
)

func TestWatchPublishesOnSubmit(t *testing.T) {
	c := coordinator.New(coordinator.Options{Store: memstore.New()})
	sub := c.Watch("not-yet")
	defer sub.Unsubscribe()

	id, err := c.Submit(context.Background(), job.Spec{Kind: "k", MaxAttempts: 1, LeaseTTL: time.Second})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	// We subscribed to the wrong id — must NOT get a publish.
	select {
	case j := <-sub.Updates():
		t.Errorf("unexpected publish for unrelated id: %+v", j)
	case <-time.After(50 * time.Millisecond):
	}

	// Now subscribe to the real id.
	sub2 := c.Watch(id)
	defer sub2.Unsubscribe()

	// Trigger another transition: lease.
	if _, _, err := c.Lease(context.Background(), []string{"k"}, "w1", time.Second, 0); err != nil {
		t.Fatalf("Lease: %v", err)
	}

	select {
	case j := <-sub2.Updates():
		if j.State != job.StateLeased {
			t.Errorf("got state %v, want leased", j.State)
		}
	case <-time.After(time.Second):
		t.Fatal("did not receive lease update")
	}
}

func TestWatchPublishesTerminalStates(t *testing.T) {
	c := coordinator.New(coordinator.Options{Store: memstore.New()})

	id, _ := c.Submit(context.Background(), job.Spec{Kind: "k", MaxAttempts: 1, LeaseTTL: time.Second})
	leased, _, _ := c.Lease(context.Background(), []string{"k"}, "w1", time.Second, 0)

	sub := c.Watch(id)
	defer sub.Unsubscribe()

	if err := c.Complete(context.Background(), leased.ID, "w1", job.Result{Payload: []byte("ok")}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	select {
	case j := <-sub.Updates():
		if j.State != job.StateSucceeded {
			t.Errorf("state = %v, want succeeded", j.State)
		}
	case <-time.After(time.Second):
		t.Fatal("no Succeeded update")
	}
}

func TestWatchPublishesCancel(t *testing.T) {
	c := coordinator.New(coordinator.Options{Store: memstore.New()})
	id, _ := c.Submit(context.Background(), job.Spec{Kind: "k", MaxAttempts: 1, LeaseTTL: time.Second})

	sub := c.Watch(id)
	defer sub.Unsubscribe()

	if err := c.Cancel(context.Background(), id); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	select {
	case j := <-sub.Updates():
		if j.State != job.StateCancelled {
			t.Errorf("state = %v, want cancelled", j.State)
		}
	case <-time.After(time.Second):
		t.Fatal("no Cancelled update")
	}
}

func TestUnsubscribeIsIdempotent(t *testing.T) {
	c := coordinator.New(coordinator.Options{Store: memstore.New()})
	sub := c.Watch("x")
	sub.Unsubscribe()
	sub.Unsubscribe() // must not panic
}

func TestSlowConsumerIsNotBlocking(t *testing.T) {
	c := coordinator.New(coordinator.Options{Store: memstore.New()})
	sub := c.Watch("dummy") // never read from
	defer sub.Unsubscribe()

	// Spam publishes by submitting several jobs unrelated to "dummy".
	// What we actually want is to confirm Submit calls don't deadlock; we
	// achieve that by issuing many publishes against a subscription we
	// never read from. Even though the channel target id doesn't match,
	// the test guards against an obvious "publisher holds lock forever"
	// regression.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			_, _ = c.Submit(context.Background(), job.Spec{Kind: "k", MaxAttempts: 1, LeaseTTL: time.Second})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publisher appears to be blocking on slow consumer")
	}
}
