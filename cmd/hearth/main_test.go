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

func TestRunCoordinatorRequiresCA(t *testing.T) {
	t.Setenv("HEARTH_CA_DIR", filepath.Join(t.TempDir(), "no-ca"))
	if err := runCoordinator([]string{"--listen", ":0", "--data", t.TempDir()}); err == nil {
		t.Errorf("expected error when CA missing")
	}
}

func TestRunStatusRequiresBundle(t *testing.T) {
	if err := runStatus(nil); err == nil {
		t.Errorf("expected error: --bundle required")
	}
}

func TestRunNodesRequiresBundle(t *testing.T) {
	if err := runNodes(nil); err == nil {
		t.Errorf("expected error: --bundle required")
	}
}

func TestRunSubmitRequiresBundle(t *testing.T) {
	if err := runSubmit([]string{"--kind", "k"}); err == nil {
		t.Errorf("expected error: --bundle required")
	}
}
