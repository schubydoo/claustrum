//go:build linux

package supervisor

import (
	"errors"
	"testing"

	"github.com/schubydoo/claustrum/internal/install"
)

// TestRefuseManagedLauncherOwnPathWithCheck pins that a launcher which is the command
// gets the own-path text with no fault, before the file check runs (claustrum's own,
// not measured). The file check would give another text for this path.
func TestRefuseManagedLauncherOwnPathWithCheck(t *testing.T) {
	lo := &LauncherOpts{Check: &install.LauncherCheck{}}
	err := RefuseManagedLauncher([]string{"/opt/claude/cli"}, "/opt/claude/cli", lo)
	var sr *SpawnRefusal
	if !errors.As(err, &sr) {
		t.Fatalf("got %v, want a SpawnRefusal", err)
	}
	want := ManagedSpawnCannotUse + "launcher `/opt/claude/cli` is Claude Code's own path"
	if sr.Msg != want || sr.Fault != "" {
		t.Errorf("got %q fault %q, want %q and no fault", sr.Msg, sr.Fault, want)
	}
}
