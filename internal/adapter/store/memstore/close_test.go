package memstore_test

import (
	"testing"

	"github.com/notpop/hearth/internal/adapter/store/memstore"
)

func TestCloseIsNoop(t *testing.T) {
	if err := memstore.New().Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}
