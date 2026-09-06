//go:build unix

package main

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// runlockHoldFixture is the "runlock-hold" helper mode: take the flock on args[0],
// write an owner record naming this process a serve daemon, then linger so claimRunDir
// must evict it. args[1] == "term-ignore" swallows SIGTERM (the escalation case);
// args[2] is a ready-file the parent waits on; an optional args[3] overrides the node
// so the parent can exercise the machine-identity refusal guard.
func runlockHoldFixture(args []string) int {
	lockPath, termMode, readyPath := args[0], args[1], args[2]
	fd, err := syscall.Open(lockPath, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		return 1
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		fmt.Fprintln(os.Stderr, "flock:", err)
		return 1
	}
	node := nodeID()
	if len(args) > 3 && args[3] != "" {
		node = args[3]
	}
	writeOwnerRecord(fd, ownerRecord{
		Pid:        os.Getpid(),
		Role:       "serve",
		Node:       node,
		InstanceID: newRunDirInstanceID(),
		StartedAt:  time.Now().UnixMilli(),
	})
	if termMode == "term-ignore" {
		ignoreSigterm()
	}
	if err := os.WriteFile(readyPath, []byte("ready"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "ready:", err)
		return 1
	}
	time.Sleep(60 * time.Second)
	return 0
}

func TestMatchesServeArgv(t *testing.T) {
	const sock = "/run/x/s.sock"
	cases := []struct {
		name string
		argv []string
		want bool
	}{
		{"equals form", []string{"claustrum", "-serve", "-socket=" + sock}, true},
		{"double dash + separate value", []string{"claustrum", "--serve", "--socket", sock}, true},
		{"separate value", []string{"claustrum", "-serve", "-socket", sock}, true},
		{"equivalent path, separate value", []string{"claustrum", "-serve", "-socket", "/run/x/./s.sock"}, true},
		{"equivalent path, equals form", []string{"claustrum", "-serve", "-socket=/run/./x/s.sock"}, true},
		{"no serve flag", []string{"claustrum", "-socket=" + sock}, false},
		{"no socket flag", []string{"claustrum", "-serve"}, false},
		{"wrong socket", []string{"claustrum", "-serve", "-socket=/run/y/s.sock"}, false},
		{"wrong argv0", []string{"someothertool", "-serve", "-socket=" + sock}, false},
		{"empty argv", nil, false},
		{"dangling -socket", []string{"claustrum", "-serve", "-socket"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchesServeArgv(tc.argv, sock, "claustrum"); got != tc.want {
				t.Errorf("matchesServeArgv(%q) = %v, want %v", tc.argv, got, tc.want)
			}
		})
	}
	if matchesServeArgv([]string{"claustrum", "-serve", "-socket=" + sock}, sock, "") {
		t.Error("empty self must never match (an unidentifiable daemon must not signal a holder)")
	}
}

func TestHolderSignalRefusal(t *testing.T) {
	self := nodeID()
	const sock = "/run/x/s.sock"
	base := ownerRecord{Pid: 999999, Role: "serve", Node: self}
	// Only the cmdline check reads /proc; seam it true so the other guards are what
	// each case isolates.
	oldCmd := isServeCmdline
	isServeCmdline = func(int, string) bool { return true }
	t.Cleanup(func() { isServeCmdline = oldCmd })

	t.Run("not a serve daemon", func(t *testing.T) {
		r := base
		r.Role = "stop"
		if holderSignalRefusal(r, sock) == "" {
			t.Error("a non-serve holder must be refused")
		}
	})
	t.Run("no usable pid", func(t *testing.T) {
		r := base
		r.Pid = 1
		if holderSignalRefusal(r, sock) == "" {
			t.Error("pid < 2 must be refused")
		}
	})
	t.Run("our own pid", func(t *testing.T) {
		r := base
		r.Pid = os.Getpid()
		if holderSignalRefusal(r, sock) == "" {
			t.Error("our own pid must be refused")
		}
	})
	t.Run("empty holder node", func(t *testing.T) {
		r := base
		r.Node = ""
		if holderSignalRefusal(r, sock) == "" {
			t.Error("an unknown machine identity must be refused")
		}
	})
	t.Run("node mismatch", func(t *testing.T) {
		if self == "" {
			t.Skip("no /proc node identity on this platform")
		}
		r := base
		r.Node = self + "-other"
		if holderSignalRefusal(r, sock) == "" {
			t.Error("a foreign machine identity must be refused")
		}
	})
	t.Run("safe to signal", func(t *testing.T) {
		if self == "" {
			t.Skip("no /proc node identity on this platform")
		}
		// Our parent is a live pid in our namespace that is not us; the only real
		// /proc read left is the pid-namespace check, which it passes.
		r := base
		r.Pid = os.Getppid()
		if r.Pid < 2 || r.Pid == os.Getpid() {
			t.Skip("no suitable sibling pid")
		}
		if reason := holderSignalRefusal(r, sock); reason != "" {
			t.Errorf("expected no refusal for a valid sibling holder, got %q", reason)
		}
	})
	t.Run("not our serve process", func(t *testing.T) {
		if self == "" {
			t.Skip("no /proc node identity on this platform")
		}
		// A live sibling that passes every earlier guard but whose command line is not
		// our -serve process for this socket must be refused (the last guard).
		prev := isServeCmdline
		isServeCmdline = func(int, string) bool { return false }
		defer func() { isServeCmdline = prev }()
		r := base
		r.Pid = os.Getppid()
		if r.Pid < 2 || r.Pid == os.Getpid() {
			t.Skip("no suitable sibling pid")
		}
		if holderSignalRefusal(r, sock) == "" {
			t.Error("a holder whose cmdline is not our serve process must be refused")
		}
	})
}

// writeRecordFile stages an owner record into path via the production writer, so the
// eviction tests read exactly the on-disk shape claimRunDir writes.
func writeRecordFile(t *testing.T, path string, rec ownerRecord) {
	t.Helper()
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		t.Fatalf("open record file: %v", err)
	}
	defer func() { _ = syscall.Close(fd) }()
	writeOwnerRecord(fd, rec)
}

func TestLockRunDirFlockError(t *testing.T) {
	// A flock failure that is not EWOULDBLOCK (here EBADF from a closed fd) is fatal to
	// the claim: lockRunDir logs and gives up without attempting eviction.
	fd, err := syscall.Open(filepath.Join(shortTempDir(t), runDirLockName),
		syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = syscall.Close(fd) // the fd is now invalid; flock yields EBADF, not EWOULDBLOCK.
	if lockRunDir(fd, "/nonexistent/daemon.lock", "/nonexistent/s.sock") {
		t.Error("lockRunDir must fail when flock returns a non-EWOULDBLOCK error")
	}
}

func TestEvictRunDirHolderUnusableRecord(t *testing.T) {
	dir := shortTempDir(t)
	path := filepath.Join(dir, runDirLockName)
	// An empty file parses to no usable owner record: eviction refuses rather than
	// signalling an unknown pid.
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if evictRunDirHolder(path, filepath.Join(dir, "s.sock")) {
		t.Error("eviction must refuse when the owner record is unusable")
	}
}

func TestEvictRunDirHolderAbsentPid(t *testing.T) {
	if nodeID() == "" {
		t.Skip("needs a machine identity (nodeID)")
	}
	dir := shortTempDir(t)
	path := filepath.Join(dir, runDirLockName)
	// A record that passes every guard but names a pid that no longer exists: the
	// SIGTERM attempt returns ESRCH, and evictRunDirHolder resolves that via holderGone
	// as "the holder already exited", reporting a successful eviction.
	writeRecordFile(t, path, ownerRecord{Pid: 1 << 30, Role: "serve", Node: nodeID()})
	oldCmd := isServeCmdline
	isServeCmdline = func(int, string) bool { return true }
	t.Cleanup(func() { isServeCmdline = oldCmd })
	if !evictRunDirHolder(path, filepath.Join(dir, "s.sock")) {
		t.Error("an absent holder pid must resolve as already gone (eviction succeeds)")
	}
}

func TestSignalHolderNonESRCHError(t *testing.T) {
	// A kill error that is neither success nor ESRCH (here EINVAL from an out-of-range
	// signal number) is logged and reported as "not delivered". The invalid signal is
	// rejected by the kernel before delivery, so signalling our own pid is safe.
	if signalHolder(os.Getpid(), syscall.Signal(0x3fffffff)) {
		t.Error("signalHolder must report false when kill fails with a non-ESRCH error")
	}
}

func TestWriteOwnerRecordTruncateError(t *testing.T) {
	path := filepath.Join(shortTempDir(t), runDirLockName)
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = syscall.Close(fd) // Ftruncate on the closed fd fails; the write must bail out.
	writeOwnerRecord(fd, ownerRecord{Pid: 2, Role: "serve"})
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "stale" {
		t.Errorf("file changed to %q; writeOwnerRecord must bail when truncate fails", b)
	}
}

func TestReadOwnerRecordMissingFile(t *testing.T) {
	if _, err := readOwnerRecord(filepath.Join(shortTempDir(t), "does-not-exist")); err == nil {
		t.Error("readOwnerRecord must return the read error for a missing file")
	}
}

func TestClaimRunDirHappyPath(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "s.sock")
	release := claimRunDir(sock, "serve")
	t.Cleanup(release)

	lockPath := filepath.Join(dir, runDirLockName)
	rec, err := readOwnerRecord(lockPath)
	if err != nil {
		t.Fatalf("read owner record: %v", err)
	}
	if rec.Pid != os.Getpid() {
		t.Errorf("record pid = %d, want %d", rec.Pid, os.Getpid())
	}
	if rec.Role != "serve" {
		t.Errorf("record role = %q, want serve", rec.Role)
	}
	if len(rec.InstanceID) != 32 {
		t.Errorf("record instanceId = %q, want 32 hex chars", rec.InstanceID)
	}
	if rec.StartedAt == 0 {
		t.Error("record startedAt is zero")
	}

	release()
	fi, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("lock file must survive release (truncate, not unlink): %v", err)
	}
	if fi.Size() != 0 {
		t.Errorf("lock file size after release = %d, want 0 (truncated)", fi.Size())
	}
}

// spawnLockHolder starts the runlock-hold fixture on lockPath and waits for it to be
// ready (flock taken, record written). termMode is "" or "term-ignore"; nodeOverride
// forces a foreign machine identity when non-empty.
func spawnLockHolder(t *testing.T, lockPath, termMode, nodeOverride string) *exec.Cmd {
	t.Helper()
	exe, env := helperCommand(t, "runlock-hold")
	ready := lockPath + ".ready"
	cmd := exec.Command(exe, lockPath, termMode, ready, nodeOverride)
	cmd.Env = buildEnv(env)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start lock holder: %v", err)
	}
	// Reap the holder as soon as it exits. It is a child of the test process, so
	// without this a SIGTERM/SIGKILL leaves a zombie whose pid still answers
	// Kill(pid,0) — masking the exit the eviction ladder polls for. A real
	// predecessor is not the new daemon's child and is reaped by init, so this only
	// reproduces production reaping inside the test.
	reaped := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(reaped) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-reaped
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			return cmd
		}
		if time.Now().After(deadline) {
			t.Fatalf("lock holder never became ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func shrinkEvictionGraces(t *testing.T) {
	t.Helper()
	ot, ok, op := runDirTermGrace, runDirKillGrace, runDirPollInterval
	runDirTermGrace, runDirKillGrace, runDirPollInterval = 300*time.Millisecond, 2*time.Second, 10*time.Millisecond
	t.Cleanup(func() { runDirTermGrace, runDirKillGrace, runDirPollInterval = ot, ok, op })
}

func TestClaimRunDirEvictsPredecessor(t *testing.T) {
	if nodeID() == "" {
		t.Skip("run-dir eviction needs a machine identity (nodeID)")
	}
	shrinkEvictionGraces(t)
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "s.sock")
	lockPath := filepath.Join(dir, runDirLockName)
	holder := spawnLockHolder(t, lockPath, "", "")

	// The holder's real argv is the test binary, not "-serve"; seam the cmdline check
	// so the eviction ladder runs against it.
	oldCmd := isServeCmdline
	isServeCmdline = func(int, string) bool { return true }
	t.Cleanup(func() { isServeCmdline = oldCmd })

	release := claimRunDir(sock, "serve")
	t.Cleanup(release)

	if !waitForExit(holder.Process.Pid, 3*time.Second) {
		t.Fatal("predecessor was not evicted by SIGTERM")
	}
	rec, err := readOwnerRecord(lockPath)
	if err != nil {
		t.Fatalf("read owner record after eviction: %v", err)
	}
	if rec.Pid != os.Getpid() {
		t.Errorf("after eviction record pid = %d, want our pid %d", rec.Pid, os.Getpid())
	}
}

// TestClaimRunDirEvictionLogsMatchReference pins the #320 eviction log MESSAGE
// wording against 4534d86 (runtime-captured, scratch/probe/runlock-log-4534d86.md):
// the "[daemon] serve:" prefix, the instance id in the eviction line, and the
// "previous owner of <rundir>: terminated" summary. Only the message text is
// matched; claustrum keeps its own level tag. The old "[daemon] run dir:" prefix
// must be gone.
func TestClaimRunDirEvictionLogsMatchReference(t *testing.T) {
	if nodeID() == "" {
		t.Skip("run-dir eviction needs a machine identity (nodeID)")
	}
	shrinkEvictionGraces(t)
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "s.sock")
	lockPath := filepath.Join(dir, runDirLockName)
	holder := spawnLockHolder(t, lockPath, "", "")
	oldCmd := isServeCmdline
	isServeCmdline = func(int, string) bool { return true }
	t.Cleanup(func() { isServeCmdline = oldCmd })

	var buf bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&buf)
	release := claimRunDir(sock, "serve") // evicts synchronously, logging the ladder
	log.SetOutput(oldOut)
	t.Cleanup(release)
	_ = waitForExit(holder.Process.Pid, 3*time.Second)

	got := buf.String()
	for _, want := range []string{
		"[daemon] serve: run dir is held by a live daemon, pid ",
		`(instance "`,
		"); sending SIGTERM",
		") exited after SIGTERM",
		"[daemon] serve: previous owner of ",
		": terminated",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("eviction log missing %q\n--- got ---\n%s", want, got)
		}
	}
	if strings.Contains(got, "[daemon] run dir:") || strings.Contains(got, "serving without run-dir ownership") {
		t.Errorf("eviction log still uses the old prefix/tail\n%s", got)
	}
}

// TestEvictRunDirHolderStopRoleLogs pins the held-by-stop wording (4534d86): a
// holder whose record role is "stop" is left in place with the "--stop ... has not
// let go; leaving it" line and a "previous owner of <rundir>: survivor" summary.
func TestEvictRunDirHolderStopRoleLogs(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "s.sock")
	lockPath := filepath.Join(dir, runDirLockName)
	fd, err := syscall.Open(lockPath, syscall.O_RDWR|syscall.O_CREAT, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	// role=stop; the role gate returns before any signal, so the pid is never touched.
	writeOwnerRecord(fd, ownerRecord{Pid: 999999, Role: "stop", Node: nodeID(), InstanceID: "x", StartedAt: 1})
	_ = syscall.Close(fd)

	var buf bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&buf)
	evicted := evictRunDirHolder(lockPath, sock)
	log.SetOutput(oldOut)
	if evicted {
		t.Error("evictRunDirHolder evicted a --stop holder; want left in place")
	}
	got := buf.String()
	for _, want := range []string{"is held by a --stop (pid 999999) that has not let go; leaving it", "[daemon] serve: previous owner of ", ": survivor"} {
		if !strings.Contains(got, want) {
			t.Errorf("stop-role log missing %q\n--- got ---\n%s", want, got)
		}
	}
}

func TestClaimRunDirEscalatesToSIGKILL(t *testing.T) {
	if nodeID() == "" {
		t.Skip("run-dir eviction needs a machine identity (nodeID)")
	}
	shrinkEvictionGraces(t)
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "s.sock")
	lockPath := filepath.Join(dir, runDirLockName)
	holder := spawnLockHolder(t, lockPath, "term-ignore", "")

	oldCmd := isServeCmdline
	isServeCmdline = func(int, string) bool { return true }
	t.Cleanup(func() { isServeCmdline = oldCmd })

	release := claimRunDir(sock, "serve")
	t.Cleanup(release)

	if !waitForExit(holder.Process.Pid, 3*time.Second) {
		t.Fatal("predecessor survived the SIGTERM->SIGKILL ladder")
	}
	rec, err := readOwnerRecord(lockPath)
	if err != nil {
		t.Fatalf("read owner record after escalation: %v", err)
	}
	if rec.Pid != os.Getpid() {
		t.Errorf("after escalation record pid = %d, want our pid %d", rec.Pid, os.Getpid())
	}
}

// TestClaimRunDirRefusesSymlinkLock pins the O_NOFOLLOW behavior (parity with the
// reference on linux, measured): a pre-planted daemon.lock symlink (what a local
// attacker in a shared socket dir would leave) must NOT be followed and its target
// must NOT be truncated. The daemon serves without run-dir ownership instead. The
// mutant (drop O_NOFOLLOW) follows the link and
// writeOwnerRecord truncates the victim, failing the content check.
func TestClaimRunDirRefusesSymlinkLock(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "s.sock")
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, runDirLockName)); err != nil {
		t.Fatal(err)
	}

	release := claimRunDir(sock, "serve")
	t.Cleanup(release)

	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("victim unreadable: %v", err)
	}
	if string(got) != "precious" {
		t.Errorf("a planted daemon.lock symlink had its target truncated/overwritten: %q — O_NOFOLLOW did not refuse it", got)
	}
}

// TestClaimRunDirDoesNotSIGKILLAReusedPid pins the pre-SIGKILL re-verification: if the
// holder's pid is reused during the SIGTERM grace (its command line no longer matches
// our serve process), the ladder must NOT SIGKILL that innocent pid. The holder ignores
// SIGTERM so the ladder reaches the escalation; isServeCmdline passes once (pre-SIGTERM)
// then fails (pre-SIGKILL), simulating the reuse. The mutant (drop the re-check) SIGKILLs
// the still-alive holder, so holderGone flips true and the test fails.
func TestClaimRunDirDoesNotSIGKILLAReusedPid(t *testing.T) {
	if nodeID() == "" {
		t.Skip("run-dir eviction needs a machine identity (nodeID)")
	}
	shrinkEvictionGraces(t)
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "s.sock")
	lockPath := filepath.Join(dir, runDirLockName)
	holder := spawnLockHolder(t, lockPath, "term-ignore", "")

	oldCmd := isServeCmdline
	var calls int
	isServeCmdline = func(int, string) bool {
		calls++
		return calls == 1 // our serve holder before SIGTERM; a reused pid before SIGKILL
	}
	t.Cleanup(func() { isServeCmdline = oldCmd })

	release := claimRunDir(sock, "serve")
	t.Cleanup(release)

	if calls < 2 {
		t.Fatalf("isServeCmdline ran %d times; the pre-SIGKILL re-check did not run", calls)
	}
	if holderGone(holder.Process.Pid) {
		t.Error("the reused pid was SIGKILL'd; the pre-SIGKILL re-verification did not protect it")
	}
}

func TestClaimRunDirRefusesForeignHolder(t *testing.T) {
	if nodeID() == "" {
		t.Skip("needs a machine identity (nodeID)")
	}
	shrinkEvictionGraces(t)
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "s.sock")
	lockPath := filepath.Join(dir, runDirLockName)
	// Force a foreign node so the machine-identity guard refuses, even though the
	// cmdline check is seamed true.
	holder := spawnLockHolder(t, lockPath, "", nodeID()+"-other")

	oldCmd := isServeCmdline
	isServeCmdline = func(int, string) bool { return true }
	t.Cleanup(func() { isServeCmdline = oldCmd })

	release := claimRunDir(sock, "serve")
	t.Cleanup(release)

	// The holder must be left alive, and it must still hold the lock (our claim gave
	// up without ownership).
	if waitForExit(holder.Process.Pid, 500*time.Millisecond) {
		t.Fatal("a foreign-node holder was signalled; the machine-identity guard failed")
	}
	fd, err := syscall.Open(lockPath, syscall.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open lock: %v", err)
	}
	defer syscall.Close(fd)
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != syscall.EWOULDBLOCK {
		t.Errorf("lock was free after a refused claim (flock err = %v), holder should still own it", err)
	}
}

func TestNewServerOnSocketWritesRunLock(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "s.sock")
	s, err := newServerOnSocket(sock, "tok", "", wireLogOptions{}, false, false)
	if err != nil {
		t.Fatalf("newServerOnSocket: %v", err)
	}
	t.Cleanup(func() { s.procs.killAll() })

	lockPath := filepath.Join(dir, runDirLockName)
	rec, err := readOwnerRecord(lockPath)
	if err != nil {
		t.Fatalf("daemon.lock not written at boot: %v", err)
	}
	if rec.Pid != os.Getpid() || rec.Role != "serve" {
		t.Errorf("boot owner record = %+v, want our pid + role serve", rec)
	}

	s.signalShutdown()
	s.closeAll(sock)
	fi, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("daemon.lock must survive closeAll (truncate, not unlink): %v", err)
	}
	if fi.Size() != 0 {
		t.Errorf("daemon.lock size after closeAll = %d, want 0", fi.Size())
	}
}
