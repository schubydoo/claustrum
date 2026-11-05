//go:build linux || darwin

package childrec

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/procinfo"
	"github.com/schubydoo/claustrum/internal/sysproc"
)

// TestReapOrphansRefusesRecordSwappedAfterTypeTest swaps a record file between the
// Lstat and the open of the read. The first file is a record of this daemon's own
// child, which the sweep keeps and never signals. The second file names a live helper
// that this test starts, as the orphan of a dead predecessor. It passes every other
// test of the sweep, so only the identity compare of the read stops the signal. The
// read refuses it, and the sweep forgets the record as for any record that does not
// read: one plain remove of the name, no signal and no log line (from the code).
func TestReapOrphansRefusesRecordSwappedAfterTypeTest(t *testing.T) {
	if node, _ := ownReapIdentity(); node == "" {
		t.Skip("no node identity on this host")
	}
	// The helper leads its own group (sysproc.NewSysProcAttr sets Setpgid) and starts no child.
	// Each signal goes through cmd.Process, which sends nothing once the helper is reaped.
	exe, env := helperCommand(t, "sleep")
	cmd := exec.Command(exe, "60")
	cmd.Env = buildEnv(env)
	cmd.SysProcAttr = sysproc.NewSysProcAttr()
	if err := cmd.Start(); err != nil {
		t.Fatalf("helper: %v", err)
	}
	helper := cmd.Process.Pid
	exited := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(exited) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
	})

	runDir := t.TempDir()
	children := filepath.Join(runDir, "children")
	name := strconv.Itoa(helper) + ".json"
	own := orphanRecord(helper, "/n", "")
	own.Instance = "inst-self"
	writeRec(t, runDir, own)
	swapped, err := json.Marshal(orphanRecord(helper, "/n", ""))
	if err != nil {
		t.Fatal(err)
	}

	sim := newProcSim(t)
	sim.addVerified(helper, "9000", runDir)
	// A kill reaches the real helper only, and every other pid gets none.
	procinfo.SetKillGroupForTest(func(pid int, sig syscall.Signal) error {
		sim.kills = append(sim.kills, killRec{pid, sig})
		if pid != helper {
			return nil
		}
		return cmd.Process.Signal(sig)
	})

	swaps := 0
	t.Run("swap", func(t *testing.T) {
		old := recordTypeTested
		t.Cleanup(func() { recordTypeTested = old })
		recordTypeTested = func() {
			swaps++
			tmp := filepath.Join(children, ".swap")
			if err := os.WriteFile(tmp, swapped, 0o600); err != nil {
				t.Error(err)
				return
			}
			if err := os.Rename(tmp, filepath.Join(children, name)); err != nil {
				t.Error(err)
			}
		}

		out := captureLog(t, func() { reapOrphans(runDir, "inst-self") })

		if swaps != 1 {
			t.Fatalf("the seam ran %d times, want once", swaps)
		}
		if len(sim.kills) != 0 {
			t.Errorf("the sweep sent signals for a swapped record: %v", sim.kills)
		}
		if names := childrenNames(t, children); len(names) != 0 {
			t.Errorf("entries left in children/: %v, want the swapped record removed", names)
		}
		if strings.Contains(out, name) || strings.Contains(out, "predecessor's children") {
			t.Errorf("the sweep logged a line for the swapped record:\n%s", out)
		}
		select {
		case <-exited:
			t.Error("the helper ended: the sweep signalled the process of the swapped record")
		case <-time.After(time.Second):
		}
	})

	// The restore check, which holds in any test order: after the subtest, a read no
	// longer runs its hook.
	writeRec(t, runDir, own)
	if _, ok := simCtx(t, runDir).read(name); !ok || swaps != 1 {
		t.Errorf("read after the subtest = %v with %d swaps, want the record and the hook restored", ok, swaps)
	}
}
