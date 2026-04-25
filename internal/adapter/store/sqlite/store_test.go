package sqlite_test

import (
	"path/filepath"
	"testing"

	"github.com/notpop/hearth/internal/adapter/store/sqlite"
	"github.com/notpop/hearth/internal/adapter/store/storetest"
	"github.com/notpop/hearth/internal/app"
)

func TestSQLiteConformance(t *testing.T) {
	storetest.RunSuite(t, func(t *testing.T) app.Store {
		dir := t.TempDir()
		s, err := sqlite.Open(filepath.Join(dir, "hearth.db"))
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}
