//go:build linux

package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startCredsChild starts the host-creds helper with the hostCreds env FOO=1 and
// returns its Proc and its stdout. A name other than "" starts it through a link of
// that name in a temp folder.
func startCredsChild(t *testing.T, m *Manager, id, name string) (*Proc, string) {
	t.Helper()
	c, frames := pipeConn(t)
	exe, env := helperCommand(t, "host-creds")
	if name != "" {
		link := filepath.Join(t.TempDir(), name)
		if err := os.Symlink(exe, link); err != nil {
			t.Fatal(err)
		}
		exe = link
	}
	p, err := m.SpawnWithTempRoot(c, id, exe, nil, "", env, false, nil, 0, TempRootAsk{HostCreds: map[string]string{"FOO": "1"}}, nil, 0)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	return p, untilExit(t, frames)
}

const wantCredsHead = `{"env":{"FOO":"1"},"expiresAt":null,"pid":`

// TestHostCredsSpawnStaysPlain pins that the HostCreds field does not change
// plainGroupEnd of a spawn with a plain name (claustrum's own, not measured). Its child
// reads the payload through the trampoline.
func TestHostCredsSpawnStaysPlain(t *testing.T) {
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	m.RunDir = t.TempDir()
	p, out := startCredsChild(t, m, "hc-plain", "")
	if !p.plainGroupEnd {
		t.Error("plainGroupEnd = false for a hostCreds spawn, want true")
	}
	if !strings.HasPrefix(out, wantCredsHead) {
		t.Errorf("child read %q, want %s…", out, wantCredsHead)
	}
}

// TestHostCredsCLISpawnEndsGroup pins that a hostCreds spawn of a command named ccd-cli
// gets the group end of cliExitEndsGroup, not the plain one.
func TestHostCredsCLISpawnEndsGroup(t *testing.T) {
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	m.RunDir = t.TempDir()
	p, out := startCredsChild(t, m, "hc-cli", "ccd-cli")
	if !p.endsGroup || p.plainGroupEnd {
		t.Errorf("endsGroup %v, plainGroupEnd %v, want true and false", p.endsGroup, p.plainGroupEnd)
	}
	if !strings.HasPrefix(out, wantCredsHead) {
		t.Errorf("child read %q, want %s…", out, wantCredsHead)
	}
}

// TestHostCredsDirectStart pins fd 99 in the direct start after the trampoline did
// not start.
func TestHostCredsDirectStart(t *testing.T) {
	noSelf := filepath.Join(t.TempDir(), "no-such-daemon")
	old := SetTrampolineSelfForTest(func() string { return noSelf })
	t.Cleanup(func() { SetTrampolineSelfForTest(old) })
	_ = captureLogBuf(t)
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	m.RunDir = t.TempDir()
	if _, out := startCredsChild(t, m, "hc-direct", ""); !strings.HasPrefix(out, wantCredsHead) {
		t.Errorf("child read %q, want %s…", out, wantCredsHead)
	}
}

// openFDs is the count of the open fds of this process.
func openFDs(t *testing.T) int {
	t.Helper()
	es, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(es)
}

// TestHostCredsClosesBothEnds pins that a hostCreds spawn leaves no more open fds in
// the daemon than a plain spawn: the daemon closes its read end, and the write end
// closes after the write.
func TestHostCredsClosesBothEnds(t *testing.T) {
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	m.RunDir = t.TempDir()
	c0, frames0 := pipeConn(t)
	c1, frames1 := pipeConn(t)
	c2, frames2 := pipeConn(t)
	exe, env := helperCommand(t, "host-creds")
	// left is the count of fds above base once it falls to want, or after 2 s.
	left := func(base, want int) int {
		n := openFDs(t) - base
		for i := 0; i < 200 && n > want; i++ {
			time.Sleep(10 * time.Millisecond)
			n = openFDs(t) - base
		}
		return n
	}
	// A first spawn opens fds that the manager keeps, so it only warms up.
	if _, err := m.Spawn(c0, "warm", exe, nil, "", env, false); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	untilExit(t, frames0)
	base := openFDs(t)
	if _, err := m.Spawn(c1, "plain", exe, nil, "", env, false); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	untilExit(t, frames1)
	plain := left(base, 0)
	base = openFDs(t)
	if _, err := m.SpawnWithTempRoot(c2, "hc-fds", exe, nil, "", env, false, nil, 0, TempRootAsk{HostCreds: map[string]string{"FOO": "1"}}, nil, 0); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if out := untilExit(t, frames2); !strings.HasPrefix(out, wantCredsHead) {
		t.Errorf("child read %q, want %s…", out, wantCredsHead)
	}
	if n := left(base, plain); n > plain {
		t.Errorf("a hostCreds spawn left %d more fds, a plain spawn %d", n, plain)
	}
}
