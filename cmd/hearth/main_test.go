package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/notpop/hearth/internal/security/bundle"
)

func TestSplitCSV(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a", []string{"a"}},
		{"a,b,c", []string{"a", "b", "c"}},
		{"a, b , c", []string{"a", "b", "c"}},
		{",a,,b,", []string{"a", "b"}},
		{",,,", nil},
	}
	for _, tc := range cases {
		got := splitCSV(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("splitCSV(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestDefaultCADirHonoursEnv(t *testing.T) {
	t.Setenv("HEARTH_CA_DIR", "/tmp/explicit-ca")
	if got := defaultCADir(); got != "/tmp/explicit-ca" {
		t.Errorf("defaultCADir = %q", got)
	}
}

func TestDefaultCADirFallsBackToHome(t *testing.T) {
	t.Setenv("HEARTH_CA_DIR", "")
	got := defaultCADir()
	home, err := os.UserHomeDir()
	if err != nil {
		// in extreme sandboxes, accept the bare-fallback path
		if got != ".hearth-ca" {
			t.Errorf("expected .hearth-ca fallback, got %q", got)
		}
		return
	}
	want := filepath.Join(home, ".hearth", "ca")
	if got != want {
		t.Errorf("defaultCADir = %q, want %q", got, want)
	}
}

func TestRunCAInitAndEnrollProducesUsableBundle(t *testing.T) {
	dir := t.TempDir()
	caDir := filepath.Join(dir, "ca")
	t.Setenv("HEARTH_CA_DIR", caDir)

	if err := runCA([]string{"init", "--dir", caDir, "--name", "test-ca"}); err != nil {
		t.Fatalf("runCA: %v", err)
	}
	out := filepath.Join(dir, "imac-2.hearth")
	if err := runEnroll([]string{
		"--ca", caDir,
		"--addr", "hearth.local:7843,192.168.1.10:7843",
		"--out", out,
		"imac-2",
	}); err != nil {
		t.Fatalf("runEnroll: %v", err)
	}

	b, err := bundle.ReadFile(out)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if b.Manifest.WorkerID != "imac-2" {
		t.Errorf("workerID = %q", b.Manifest.WorkerID)
	}
	if len(b.Manifest.CoordinatorAddrs) != 2 {
		t.Errorf("addrs = %v", b.Manifest.CoordinatorAddrs)
	}
}

func TestRunCARequiresSubcommand(t *testing.T) {
	if err := runCA(nil); err == nil {
		t.Errorf("expected error")
	}
}

func TestRunCARejectsUnknownSubcommand(t *testing.T) {
	if err := runCA([]string{"bogus"}); err == nil {
		t.Errorf("expected error")
	}
}

func TestRunEnrollRejectsMissingArgs(t *testing.T) {
	dir := t.TempDir()
	if err := runEnroll([]string{"--ca", dir}); err == nil {
		t.Errorf("expected error for missing worker name")
	}
}

func TestRunEnrollRejectsMissingCA(t *testing.T) {
	dir := t.TempDir()
	if err := runEnroll([]string{"--ca", filepath.Join(dir, "no-such"), "imac-2"}); err == nil {
		t.Errorf("expected error for missing CA")
	}
}

func TestRunWorkerRequiresBundle(t *testing.T) {
	if err := runWorker(nil); err == nil {
		t.Errorf("expected error for missing bundle")
	}
}

func TestRunWorkerRejectsBadBundle(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no.hearth")
	if err := runWorker([]string{"--bundle", missing}); err == nil {
		t.Errorf("expected error for missing bundle file")
	}
}

func TestRunSubmitRequiresKind(t *testing.T) {
	if err := runSubmit(nil); err == nil {
		t.Errorf("expected error for missing --kind")
	}
}

func TestRunVersionPrints(t *testing.T) {
	if err := runVersion(nil); err != nil {
		t.Errorf("runVersion: %v", err)
	}
}

// runCoordinator starts a long-running server and only returns on signal,
// so it isn't directly testable in-process. The constructor helpers it
// composes are covered by TestLoadOrInitCA and TestEnsureAdminBundle.

func TestLoadOrInitCAFreshDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ca")
	ca, err := loadOrInitCA(dir)
	if err != nil {
		t.Fatalf("loadOrInitCA: %v", err)
	}
	if ca == nil {
		t.Fatal("ca is nil")
	}
	// Second call should load, not re-init.
	ca2, err := loadOrInitCA(dir)
	if err != nil {
		t.Fatalf("loadOrInitCA (2): %v", err)
	}
	if ca.Cert.SerialNumber.Cmp(ca2.Cert.SerialNumber) != 0 {
		t.Errorf("second call re-initialised CA")
	}
}

func TestEnsureAdminBundleCreatesAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	caDir := filepath.Join(dir, "ca")
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ca, err := loadOrInitCA(caDir)
	if err != nil {
		t.Fatalf("loadOrInitCA: %v", err)
	}
	if err := ensureAdminBundle(ca, dataDir, "0.0.0.0:7843"); err != nil {
		t.Fatalf("ensureAdminBundle: %v", err)
	}
	path := filepath.Join(dataDir, "admin.hearth")
	info1, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	// Second call must be a no-op (file mtime unchanged).
	if err := ensureAdminBundle(ca, dataDir, "0.0.0.0:7843"); err != nil {
		t.Fatalf("ensureAdminBundle (2): %v", err)
	}
	info2, _ := os.Stat(path)
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Errorf("admin bundle was rewritten on second call")
	}
}

func TestLoopbackAddrRewritesWildcards(t *testing.T) {
	cases := map[string]string{
		"0.0.0.0:7843":   "127.0.0.1:7843",
		":7843":          "127.0.0.1:7843",
		"[::]:7843":      "127.0.0.1:7843",
		"127.0.0.1:7843": "127.0.0.1:7843",
		"imac.local:80":  "imac.local:80",
		"not a host":     "not a host", // unparseable returned verbatim
	}
	for in, want := range cases {
		if got := loopbackAddr(in); got != want {
			t.Errorf("loopbackAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRunStatusRequiresBundle(t *testing.T) {
	t.Setenv("HEARTH_BUNDLE", "")
	t.Setenv("HOME", t.TempDir())
	if err := runStatus(nil); err == nil {
		t.Errorf("expected error: --bundle required")
	}
}

func TestRunNodesRequiresBundle(t *testing.T) {
	t.Setenv("HEARTH_BUNDLE", "")
	t.Setenv("HOME", t.TempDir())
	if err := runNodes(nil); err == nil {
		t.Errorf("expected error: --bundle required")
	}
}

func TestRunSubmitRequiresBundle(t *testing.T) {
	t.Setenv("HEARTH_BUNDLE", "")
	t.Setenv("HOME", t.TempDir())
	if err := runSubmit([]string{"--kind", "k"}); err == nil {
		t.Errorf("expected error: --bundle required")
	}
}
