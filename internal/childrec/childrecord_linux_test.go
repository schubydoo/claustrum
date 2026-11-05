//go:build linux

package childrec

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMachineID pins the /etc/machine-id -> /var/lib/dbus/machine-id fallback:
// the first readable non-empty file wins, the second is
// used when the first is missing, and "" is returned when neither is readable. Seams
// machineIDPaths at temp files so it does not depend on the host's real id.
func TestMachineID(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	old := machineIDPaths
	t.Cleanup(func() { machineIDPaths = old })
	machineIDPaths = []string{a, b}

	if got := machineID(); got != "" {
		t.Errorf("neither file: machineID = %q, want \"\"", got)
	}
	if err := os.WriteFile(b, []byte("bbbb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := machineID(); got != "bbbb" {
		t.Errorf("fallback only: machineID = %q, want \"bbbb\"", got)
	}
	if err := os.WriteFile(a, []byte("  aaaa\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := machineID(); got != "aaaa" {
		t.Errorf("primary present: machineID = %q, want \"aaaa\"", got)
	}
}

// TestProcStartTicks proves procStartTicks reads field 22 of /proc/<pid>/stat and
// agrees with OwnStartTicks for this process, and returns 0 for a pid it cannot read.
func TestProcStartTicks(t *testing.T) {
	self := procStartTicks(os.Getpid())
	if self <= 0 {
		t.Fatalf("procStartTicks(self) = %d, want > 0", self)
	}
	if own := OwnStartTicks(); self != own {
		t.Errorf("procStartTicks(self) = %d != OwnStartTicks() = %d", self, own)
	}
	if got := procStartTicks(1 << 30); got != 0 {
		t.Errorf("procStartTicks(nonexistent) = %d, want 0", got)
	}
}
