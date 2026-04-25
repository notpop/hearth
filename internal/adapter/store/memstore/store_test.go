package memstore_test

import (
	"testing"

	"github.com/notpop/hearth/internal/adapter/store/memstore"
	"github.com/notpop/hearth/internal/adapter/store/storetest"
	"github.com/notpop/hearth/internal/app"
)

func TestMemStoreConformance(t *testing.T) {
	storetest.RunSuite(t, func(*testing.T) app.Store {
		return memstore.New()
	})
}
