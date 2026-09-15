//go:build linux

package main

import (
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// The containment helpers, the two probes, and the spare/skip arms of the judges. Every
// verdict here decides whether the cleaner ends another process, so each assertion names the
// exact verdict and reason rather than accepting "something non-empty".

func TestHostRootsRemainingForms(t *testing.T) {
	r := &hostRoots{roots: []string{"/opt/claude", "/srv/other"}, daemonBin: "server"}

	// A socket directly under a root, not under run/<id>/. Both forms are real layouts, so a
	// mutant that recognised only run/<id>/rpc.sock would treat this one as foreign.
	if !r.isRunDirSocket("/opt/claude/rpc.sock") {
		t.Error("<root>/rpc.sock not recognised as a run-dir socket")
	}
	if r.isRunDirSocket("/elsewhere/rpc.sock") {
		t.Error("a socket outside every root was recognised")
	}

	// stripRoot answers "" for a path no root contains, which is what makes sameSocketPath
	// refuse two unrelated paths instead of calling them equal.
	if got := r.stripRoot("/elsewhere/run/x/rpc.sock"); got != "" {
		t.Errorf("stripRoot outside every root = %q, want empty", got)
	}
	if r.sameSocketPath("/elsewhere/a/rpc.sock", "/nowhere/b/rpc.sock") {
		t.Error("two paths outside every root compared equal")
	}

	// The --socket=<path> spelling, alongside the separate-argument form the other tests use.
	argv := []string{"/opt/claude/srv/a/server", "--serve", "--socket=/opt/claude/run/x/rpc.sock"}
	if got := serveArgv(argv, "server"); got != "/opt/claude/run/x/rpc.sock" {
		t.Errorf("serveArgv(--socket=) = %q, want the socket path", got)
	}
}

func TestProbeSocketUnusableConnections(t *testing.T) {
	oldDial := hcDial
	t.Cleanup(func() { hcDial = oldDial })

	// A connection with no SO_PEERCRED behind it: the dial succeeded, but nothing identifies
	// the listener, so the probe must not report a live daemon. A mutant that returned
	// hcSockLive here hands judgeDaemon a peer pid of 0.
	conn := newFakePeerConn(t).(*net.UnixConn)
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	hcDial = func(string) (net.Conn, error) { return conn, nil }
	if state, pid, _ := probeSocket("/opt/claude/run/x/rpc.sock"); state != hcSockUnknown {
		t.Errorf("probe of a peerless connection = state %d pid %d, want hcSockUnknown", state, pid)
	}

	// A connection that is not a unix socket at all. This arm is defensive: hcDial always
	// dials unix, and without the type check the peer lookup below fails the same way, so
	// the assertion pins the contract rather than discriminating against a mutant.
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	hcDial = func(string) (net.Conn, error) { return client, nil }
	if state, _, _ := probeSocket("/opt/claude/run/x/rpc.sock"); state != hcSockUnknown {
		t.Errorf("probe of a non-unix connection = state %d, want hcSockUnknown", state)
	}
}

func TestLockProbeUnopenableAndEmpty(t *testing.T) {
	fakeProc(t) // lockProbe's held check reads procRoot/locks; a bare tree means "not held"

	// A lock file that is a symlink: the probe opens with O_NOFOLLOW, so this fails with
	// ELOOP rather than not-found. "Cannot open" is NOT "no lock" — a mutant that answered
	// hcLockStale here would let the tidy pass remove a live daemon's run dir.
	dir := t.TempDir()
	lockPath := filepath.Join(dir, runDirLockName)
	if err := os.Symlink(filepath.Join(dir, "target"), lockPath); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := lockProbe(dir); state != hcLockUnknown {
		t.Errorf("lockProbe of an unopenable lock = %d, want hcLockUnknown", state)
	}

	// An empty lock file parses to no record. This arm is defensive: without it the empty
	// buffer fails to unmarshal and the same nil comes back, so the assertion is the
	// contract, not a mutant oracle.
	empty := t.TempDir()
	if err := os.WriteFile(filepath.Join(empty, runDirLockName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if state, rec, present := lockProbe(empty); state != hcLockUnknown || rec != nil || !present {
		t.Errorf("lockProbe of an empty lock = state %d rec %v present %v, want unknown/nil/true", state, rec, present)
	}
}

func TestVerifyListenerServesAnotherSocket(t *testing.T) {
	proot, mk, link := fakeProc(t)
	c := hcTestCleaner(t, "/opt/claude")
	// A daemon that verifies in every respect except the socket it serves.
	hcFakeDaemon(t, proot, mk, link, 73, "/opt/claude/srv/a/server",
		[]string{"/opt/claude/srv/a/server", "--serve", "--socket", "/opt/claude/run/other/rpc.sock"}, true, "1")
	if reason := c.verifyListener(73, "/opt/claude/run/x/rpc.sock"); reason != "it serves a different socket" {
		t.Errorf("verifyListener = %q, want %q", reason, "it serves a different socket")
	}
}

// TestJudgeDaemonAnotherVerifiedDaemon covers the last socket-live sub-case: the socket is
// answered by a DIFFERENT process that verifies as one of our daemons serving it. That daemon
// owns the socket now, so the candidate is skipped silently — not spared with a reason, and
// not reaped. Deleting the arm drops the candidate through to the lock probe, which with no
// lock file reaps it.
func TestJudgeDaemonAnotherVerifiedDaemon(t *testing.T) {
	proot, mk, link := fakeProc(t)
	c := hcTestCleaner(t, "/opt/claude")
	socket := "/opt/claude/run/x/rpc.sock"
	oldDial := hcDial
	t.Cleanup(func() { hcDial = oldDial })
	hcDial = func(string) (net.Conn, error) { return newFakePeerConn(t), nil } // peer = this process
	// SO_PEERCRED reports the RUNNER's real uid, which judgeDaemon compares against
	// hcGetuid() and verifyListener against the uid in the fake status file. All three must
	// be the same number, or the verdict depends on whether the runner happens to be uid
	// 1000 (hcFakeDaemon's default) — it is on the maintainer host and is not on CI, where
	// this failed. The fixtures therefore carry the real uid, the way
	// TestJudgeDaemonSocketLive already does.
	uid := os.Getuid()
	hcGetuid = func() int { return uid }

	// The answerer: this test process, fully verifiable as a daemon serving that socket.
	hcFakeDaemonUID(t, proot, mk, link, os.Getpid(), "/opt/claude/srv/a/server",
		[]string{"/opt/claude/srv/a/server", "--serve", "--socket", socket}, true, "1", uid)
	// The candidate: another daemon that claims the same socket but no longer answers it.
	hcFakeDaemonUID(t, proot, mk, link, 74, "/opt/claude/srv/a/server",
		[]string{"/opt/claude/srv/a/server", "--serve", "--socket", socket}, true, "1", uid)

	v, reason, target := c.judgeDaemon(mustInspect(t, 74))
	if v != dvSkip {
		t.Errorf("verdict = %v (reason %q, target %v), want skip: a verified daemon serves that socket", v, reason, target)
	}
}

// TestJudgeOrphanUnreadableArms covers the three "spare it, we could not confirm it" reasons.
// Each is a spare with a reason rather than a silent skip, because the cleaner logs them; a
// mutant that returned the wrong one (or none) fails on the string.
func TestJudgeOrphanUnreadableArms(t *testing.T) {
	proot, mk, link := fakeProc(t)
	c := hcTestCleaner(t, "/opt/claude")
	cli := "/opt/claude/ccd-cli/v1/claude"
	old := hcClock().Add(-time.Hour)

	base := hcTracked{
		pid: 75, sameUID: true, sameNS: true, exe: cli, haveAge: true, startWall: old,
		argv: []string{"claude", "--output-format=stream-json"},
		env:  []string{hcDaemonChildMarker},
	}

	noAge := base
	noAge.haveAge = false
	if reap, reason := c.judgeOrphan(noAge); reap || reason != "its age could not be determined" {
		t.Errorf("age unknown: reap=%v reason=%q", reap, reason)
	}

	noEnv := base
	noEnv.env = nil
	if reap, reason := c.judgeOrphan(noEnv); reap || reason != "its environ could not be read" {
		t.Errorf("environ unreadable: reap=%v reason=%q", reap, reason)
	}

	// Everything reads except the descriptor list: pid 75 has no fd directory in the fake
	// tree, so the last gate cannot confirm it and it is spared.
	if reap, reason := c.judgeOrphan(base); reap || reason != "its open descriptors were unreadable" {
		t.Errorf("fds unreadable: reap=%v reason=%q", reap, reason)
	}
	// With the descriptors readable the same record IS reaped, which is what makes the
	// assertion above about that gate and not about one of the earlier ones.
	link(75, "fd/0", "pipe:[1]")
	if reap, reason := c.judgeOrphan(base); !reap || reason != "" {
		t.Errorf("readable fds: reap=%v reason=%q, want a silent reap", reap, reason)
	}
	_, _ = proot, mk
}

// TestRetireAbandonedGuards covers the three refusals that keep the idle-run-dir sweep from
// SIGTERMing something it should not. Every case asserts that NO signal was sent, which is
// what each guard is for.
func TestRetireAbandonedGuards(t *testing.T) {
	proot, mk, link := fakeProc(t)
	c := hcTestCleaner(t, "/opt/claude")
	socket := "/opt/claude/run/x/rpc.sock"

	var signals []int
	var sleeps int
	oldSig, oldSleep := hcSignalPid, hcSleep
	t.Cleanup(func() { hcSignalPid, hcSleep = oldSig, oldSleep })
	hcSignalPid = func(pid int, _ syscall.Signal) error { signals = append(signals, pid); return nil }
	hcSleep = func(time.Duration) { sleeps++ }

	// Ourselves. The record is a fully verifiable, old, idle daemon, so every gate below the
	// self check would pass: deleting that check makes the cleaner SIGTERM its own daemon.
	hcFakeDaemon(t, proot, mk, link, c.selfPid, "/opt/claude/srv/a/server",
		[]string{"/opt/claude/srv/a/server", "--serve", "--socket", socket}, true, "1")
	if c.retireAbandoned(c.selfPid, socket) {
		t.Error("retireAbandoned signalled this very daemon")
	}
	if c.retireAbandoned(1, socket) {
		t.Error("retireAbandoned signalled pid 1")
	}

	// Too young. hcFakeDaemon's uptime is 100000s and the clock is fixed, so a start-ticks
	// value close to the uptime is a process that started seconds ago.
	young := 76
	hcFakeDaemon(t, proot, mk, link, young, "/opt/claude/srv/a/server",
		[]string{"/opt/claude/srv/a/server", "--serve", "--socket", socket}, true, "9999990")
	if c.retireAbandoned(young, socket) {
		t.Error("retireAbandoned signalled a daemon younger than the minimum age")
	}

	if len(signals) != 0 {
		t.Errorf("signals sent by the guarded cases: %v", signals)
	}

	// A signal that fails for a reason other than "already gone" ends the attempt: the
	// daemon is still there, so there is nothing to wait for. A mutant that fell through
	// would poll the wait out to its deadline.
	hcSignalPid = func(pid int, _ syscall.Signal) error { signals = append(signals, pid); return syscall.EPERM }
	sleeps = 0
	victim := 77
	hcFakeDaemon(t, proot, mk, link, victim, "/opt/claude/srv/a/server",
		[]string{"/opt/claude/srv/a/server", "--serve", "--socket", socket}, true, "1")
	if c.retireAbandoned(victim, socket) {
		t.Error("retireAbandoned reported success after a failed signal")
	}
	if len(signals) != 1 || signals[0] != victim {
		t.Errorf("signals = %v, want exactly the one attempt on %d", signals, victim)
	}
	if sleeps != 0 {
		t.Errorf("the wait ran %d polls after a failed signal; it should not have started", sleeps)
	}
}
