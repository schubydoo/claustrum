//go:build unix

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
		InstanceID: newDaemonInstanceID(),
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

// waitForExit polls for the exit of pid until grace elapses. It returns true once the
// process is gone. The tests read the end of their own lock-holder child with it.
func waitForExit(pid int, grace time.Duration) bool {
	deadline := time.Now().Add(grace)
	for {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// evictGone runs the eviction with no lock descriptor and returns its first result. It
// is for the arms that end before the wait on the lock, and for a seamed wait.
func evictGone(path, socket string) bool {
	gone, _ := evictRunDirHolder(-1, path, socket)
	return gone
}

func TestMatchesServeArgv(t *testing.T) {
	const sock = "/run/x/s.sock"
	cases := []struct {
		name string
		argv []string
		want bool
	}{
		{"equals form", []string{"claustrum", "-serve", "-socket=" + sock}, true},
		{"double dash equals form", []string{"claustrum", "--serve", "--socket=" + sock}, true},
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
	if evictGone(path, filepath.Join(dir, "s.sock")) {
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
	if !evictGone(path, filepath.Join(dir, "s.sock")) {
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

// TestClaimRunDirHappyPath pins the two forms of the owner record (89cb6289, Linux
// and macOS rows D1 and XL). The claim writes pid, role and node only. complete adds
// instanceId and startedAt. release leaves an empty file.
func TestClaimRunDirHappyPath(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "s.sock")
	claim := claimRunDir(sock, "serve")
	t.Cleanup(claim.release)

	lockPath := filepath.Join(dir, runDirLockName)
	short, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read owner record: %v", err)
	}
	node, _ := json.Marshal(nodeID())
	wantShort := `{"pid":` + strconv.Itoa(os.Getpid()) + `,"role":"serve","node":` + string(node) + "}\n"
	if nodeID() == "" {
		wantShort = `{"pid":` + strconv.Itoa(os.Getpid()) + `,"role":"serve"}` + "\n"
	}
	if string(short) != wantShort {
		t.Errorf("owner record after the claim = %q, want %q", short, wantShort)
	}

	claim.complete("0123456789abcdef0123456789abcdef", 1790962940734)
	full, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read owner record: %v", err)
	}
	wantFull := strings.TrimSuffix(wantShort, "}\n") + `,"instanceId":"0123456789abcdef0123456789abcdef","startedAt":1790962940734}` + "\n"
	if string(full) != wantFull {
		t.Errorf("owner record after complete = %q, want %q", full, wantFull)
	}

	claim.release()
	fi, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("lock file must survive release (truncate, not unlink): %v", err)
	}
	if fi.Size() != 0 {
		t.Errorf("lock file size after release = %d, want 0 (truncated)", fi.Size())
	}
}

// TestCompleteDoesNotTruncateTheLock pins that complete replaces the short owner record
// with one write and no truncate, so daemon.lock is never empty between the two forms.
// No seam sits between a truncate and a write, so the test reads the file size. It adds
// blanks after the short record. A truncate in complete removes them and shows as a
// smaller file.
func TestCompleteDoesNotTruncateTheLock(t *testing.T) {
	dir := shortTempDir(t)
	claim := claimRunDir(filepath.Join(dir, "s.sock"), "serve")
	t.Cleanup(claim.release)

	lockPath := filepath.Join(dir, runDirLockName)
	f, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(strings.Repeat(" ", 512)); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	before, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}

	claim.complete("0123456789abcdef0123456789abcdef", 1790962940734)

	after, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() {
		t.Errorf("daemon.lock is %d bytes after complete, %d before: complete truncated the file", after.Size(), before.Size())
	}
	rec, err := readOwnerRecord(lockPath)
	if err != nil {
		t.Fatalf("read owner record: %v", err)
	}
	if rec.Pid != os.Getpid() || rec.Role != "serve" || rec.InstanceID != "0123456789abcdef0123456789abcdef" || rec.StartedAt != 1790962940734 {
		t.Errorf("owner record after complete = %+v, want the full record of this process", rec)
	}
}

// spawnLockHolder starts the runlock-hold fixture on lockPath and waits for it to be
// ready (flock taken, record written). termMode is "" or "term-ignore"; nodeOverride
// forces a foreign machine identity when non-empty.
func spawnLockHolder(t *testing.T, lockPath, termMode, nodeOverride string) *exec.Cmd {
	t.Helper()
	cmd := startLockHolder(t, lockPath, termMode, nodeOverride)
	// Reap the holder as soon as it exits. It is a child of the test process, so
	// without this a SIGTERM/SIGKILL leaves a zombie whose pid still answers
	// Kill(pid,0). The tests read the end of the holder with that probe (waitForExit).
	// The eviction ladder itself waits on the lock, and needs no reaping.
	reaped := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(reaped) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-reaped
	})
	return cmd
}

// startLockHolder starts the runlock-hold fixture and waits for it to be ready. It does
// not reap the child. The caller does.
func startLockHolder(t *testing.T, lockPath, termMode, nodeOverride string) *exec.Cmd {
	t.Helper()
	exe, env := helperCommand(t, "runlock-hold")
	ready := lockPath + ".ready"
	cmd := exec.Command(exe, lockPath, termMode, ready, nodeOverride)
	cmd.Env = buildEnv(env)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start lock holder: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			return cmd
		}
		if time.Now().After(deadline) {
			// No cleanup owns the holder yet. End it here, so that it does not keep the lock.
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
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

	release := claimRunDir(sock, "serve").release
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

	buf := captureLogBuf(t)
	release := claimRunDir(sock, "serve").release // evicts synchronously, logging the ladder
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

// TestEvictionLinesNameTheInstanceOnlyWhenTheRecordHasOne pins the two forms of the
// eviction lines (89cb6289, Linux). A record with an instance gives `pid <n> (instance
// "<hex>")` (row E3). The short record of a daemon that has not bound yet has no
// instance, and gives `pid <n>` (row P5). Every signal and every wait is seamed.
func TestEvictionLinesNameTheInstanceOnlyWhenTheRecordHasOne(t *testing.T) {
	if nodeID() == "" {
		t.Skip("run-dir eviction needs a machine identity (nodeID)")
	}
	oc, ohold, ow := isServeCmdline, holdHolder, waitHolderLock
	t.Cleanup(func() { isServeCmdline, holdHolder, waitHolderLock = oc, ohold, ow })
	isServeCmdline = func(int, string) bool { return true }
	holdHolder = func(int) (func(syscall.Signal) error, func()) {
		return func(syscall.Signal) error { return nil }, func() {}
	}
	waitHolderLock = func(int, time.Duration) bool { return true }

	for _, tc := range []struct{ name, instance, who string }{
		{"short record", "", "pid 999999"},
		{"full record", "0123abcd", `pid 999999 (instance "0123abcd")`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := shortTempDir(t)
			lockPath := filepath.Join(dir, runDirLockName)
			writeRecordFile(t, lockPath, ownerRecord{Pid: 999999, Role: "serve", Node: nodeID(), InstanceID: tc.instance})
			buf := captureLogBuf(t)
			if gone, locked := evictRunDirHolder(-1, lockPath, filepath.Join(dir, "s.sock")); !gone || !locked {
				t.Errorf("evictRunDirHolder = %v, %v, want true, true", gone, locked)
			}
			wantLinesInOrder(t, buf.String(),
				"[daemon] serve: run dir is held by a live daemon, "+tc.who+"; sending SIGTERM",
				"[daemon] serve: previous daemon "+tc.who+" exited after SIGTERM",
				"[daemon] serve: previous owner of "+dir+": terminated")
		})
	}
}

// TestEvictionWaitsOnTheLockOfAHolderThatNobodyCollects pins the wait of the eviction
// (89cb6289, Linux rows H7, P5 and N3, macOS row P5). The holder is a child of this test
// and the test does not collect it until the claim has returned. Its pid therefore still
// answers kill(pid, 0) after it ended, as the pid of a daemon under a launcher that
// still runs does. The lock is free at that point, and the wait reads the lock.
//
// The record is the short one of a daemon that has not bound yet (rows P5 and N3).
// SAFETY: every signal goes to the holder that this test started.
func TestEvictionWaitsOnTheLockOfAHolderThatNobodyCollects(t *testing.T) {
	if nodeID() == "" {
		t.Skip("run-dir eviction needs a machine identity (nodeID)")
	}
	for _, tc := range []struct {
		name, termMode string
		termGrace      time.Duration
		want           func(pid int, dir string) []string
	}{
		// The grace is 30 s, so a claim that returns sooner did not wait it out.
		{"exits on SIGTERM", "", 30 * time.Second, func(pid int, dir string) []string {
			return []string{
				fmt.Sprintf("[daemon] serve: run dir is held by a live daemon, pid %d; sending SIGTERM", pid),
				fmt.Sprintf("[daemon] serve: previous daemon pid %d exited after SIGTERM", pid),
				"[daemon] serve: previous owner of " + dir + ": terminated",
			}
		}},
		{"ignores SIGTERM", "term-ignore", 300 * time.Millisecond, func(pid int, dir string) []string {
			return []string{
				fmt.Sprintf("[daemon] serve: run dir is held by a live daemon, pid %d; sending SIGTERM", pid),
				fmt.Sprintf("[daemon] serve: WARNING previous daemon pid %d ignored SIGTERM for 300ms; sending SIGKILL (its Claude Code children, if any, are ended next, before this daemon serves)", pid),
				"[daemon] serve: previous owner of " + dir + ": killed",
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ot, ok, op := runDirTermGrace, runDirKillGrace, runDirPollInterval
			runDirTermGrace, runDirKillGrace, runDirPollInterval = tc.termGrace, 30*time.Second, 10*time.Millisecond
			oldCmd := isServeCmdline
			isServeCmdline = func(int, string) bool { return true }
			t.Cleanup(func() {
				runDirTermGrace, runDirKillGrace, runDirPollInterval = ot, ok, op
				isServeCmdline = oldCmd
			})
			dir := shortTempDir(t)
			lockPath := filepath.Join(dir, runDirLockName)
			holder := startLockHolder(t, lockPath, tc.termMode, "")
			t.Cleanup(func() {
				_ = holder.Process.Kill()
				_, _ = holder.Process.Wait()
			})
			pid := holder.Process.Pid
			writeRecordFile(t, lockPath, ownerRecord{Pid: pid, Role: "serve", Node: nodeID()})

			buf := captureLogBuf(t)
			start := time.Now()
			claim := claimRunDir(filepath.Join(dir, "s.sock"), "serve")
			took := time.Since(start)
			t.Cleanup(claim.release)

			if took >= 30*time.Second {
				t.Errorf("the claim took %s: it waited a whole grace on a holder that had ended", took)
			}
			got := buf.String()
			wantLinesInOrder(t, got, tc.want(pid, dir)...)
			for _, no := range []string{"survived SIGKILL", "no longer our serve holder", runDirNotHolderLine} {
				if strings.Contains(got, no) {
					t.Errorf("log holds %q\n--- got ---\n%s", no, got)
				}
			}
			if rec, err := readOwnerRecord(lockPath); err != nil || rec.Pid != os.Getpid() {
				t.Errorf("owner record = %+v (err %v), want the record of this process", rec, err)
			}
		})
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

	buf := captureLogBuf(t)
	evicted := evictGone(lockPath, sock)
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

	release := claimRunDir(sock, "serve").release
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

// TestEvictRunDirHolderSignalArms drives the signal outcomes a live same-user process
// cannot reproduce: an undeliverable SIGTERM or SIGKILL (the holder already gone, or
// present but unsignalable) and a holder that survives SIGKILL (nothing survives SIGKILL
// on a modern kernel). It seams holdHolder/holderGone/waitHolderLock and feeds a synthetic
// owner record whose bogus pid passes holderSignalRefusal (a missing /proc entry reads as
// "already gone" and isServeCmdline is stubbed true). Together with
// TestClaimRunDirEscalatesToSIGKILL (the delivered-then-exits arm, on a real holder) this
// exercises every branch of the SIGTERM and SIGKILL blocks.
func TestEvictRunDirHolderSignalArms(t *testing.T) {
	if nodeID() == "" {
		t.Skip("run-dir eviction needs a machine identity (nodeID)")
	}
	// The seams report SIGTERM delivered and its grace expired, so evictRunDirHolder
	// escalates past re-verification into the SIGKILL block, where the case decides.
	restore := func() func() {
		oc, ohold, oh, ow := isServeCmdline, holdHolder, holderGone, waitHolderLock
		return func() { isServeCmdline, holdHolder, holderGone, waitHolderLock = oc, ohold, oh, ow }
	}

	cases := []struct {
		name            string
		termDelivered   bool // whether the hold delivers SIGTERM
		killDelivered   bool // whether the hold delivers SIGKILL (SIGKILL reached only when termDelivered)
		holderGone      bool // holderGone() result (read on an undeliverable signal)
		exitedAfterKill bool // waitHolderLock(killGrace) result (read only when killDelivered)
		wantEvicted     bool
		wantLog         []string
		notWantLog      []string
	}{
		{
			name:          "sigterm-undeliverable-holder-already-gone",
			termDelivered: false, holderGone: true,
			wantEvicted: true,
			wantLog:     []string{"[daemon] serve: previous owner of ", ": terminated"},
		},
		{
			name:          "sigterm-undeliverable-holder-still-present",
			termDelivered: false, holderGone: false,
			wantEvicted: false,
			wantLog:     []string{"[daemon] serve: previous owner of ", ": survivor"},
			notWantLog:  []string{": terminated", ": killed"},
		},
		{
			name:          "sigkill-undeliverable-holder-already-gone",
			termDelivered: true, killDelivered: false, holderGone: true,
			wantEvicted: true,
			wantLog:     []string{"[daemon] serve: previous owner of ", ": killed"},
		},
		{
			name:          "sigkill-undeliverable-holder-still-present",
			termDelivered: true, killDelivered: false, holderGone: false,
			wantEvicted: false,
			wantLog:     []string{"[daemon] serve: previous owner of ", ": survivor"},
			notWantLog:  []string{": terminated", ": killed"},
		},
		{
			name:          "sigkill-delivered-but-survives",
			termDelivered: true, killDelivered: true, exitedAfterKill: false,
			wantEvicted: false,
			wantLog:     []string{"survived SIGKILL", "[daemon] serve: previous owner of ", ": survivor"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(restore())
			isServeCmdline = func(int, string) bool { return true }
			holdHolder = func(int) (func(syscall.Signal) error, func()) {
				return func(sig syscall.Signal) error {
					if (sig == syscall.SIGKILL && tc.killDelivered) || (sig != syscall.SIGKILL && tc.termDelivered) {
						return nil
					}
					return syscall.ESRCH
				}, func() {}
			}
			holderGone = func(int) bool { return tc.holderGone }
			waitHolderLock = func(_ int, grace time.Duration) bool {
				if grace == runDirKillGrace {
					return tc.exitedAfterKill
				}
				return false // SIGTERM grace expires -> escalate
			}

			dir := shortTempDir(t)
			sock := filepath.Join(dir, "s.sock")
			lockPath := filepath.Join(dir, runDirLockName)
			fd, err := syscall.Open(lockPath, syscall.O_RDWR|syscall.O_CREAT, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			writeOwnerRecord(fd, ownerRecord{Pid: 999999, Role: "serve", Node: nodeID(), InstanceID: "x", StartedAt: 1})
			_ = syscall.Close(fd)

			buf := captureLogBuf(t)
			evicted := evictGone(lockPath, sock)

			if evicted != tc.wantEvicted {
				t.Errorf("evictRunDirHolder = %v, want %v", evicted, tc.wantEvicted)
			}
			got := buf.String()
			for _, want := range tc.wantLog {
				if !strings.Contains(got, want) {
					t.Errorf("log missing %q\n--- got ---\n%s", want, got)
				}
			}
			for _, no := range tc.notWantLog {
				if strings.Contains(got, no) {
					t.Errorf("log unexpectedly contains %q\n--- got ---\n%s", no, got)
				}
			}
		})
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

	buf := captureLogBuf(t)
	release := claimRunDir(sock, "serve").release
	t.Cleanup(release)
	// The daemon serves without the lock, and says so.
	if !strings.Contains(buf.String(), runDirNotHolderLine+"\n") {
		t.Errorf("log = %q, want the line %q", buf.String(), runDirNotHolderLine)
	}

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

	release := claimRunDir(sock, "serve").release
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

	release := claimRunDir(sock, "serve").release
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

// TestEvictionTimingValues pins the shipped eviction timing, which every eviction test
// sets for itself. Linux row H7 (89cb6289) measured the poll of 50 ms and the 2 s
// before SIGKILL. The 1 s after SIGKILL is claustrum's own.
func TestEvictionTimingValues(t *testing.T) {
	if runDirPollInterval != 50*time.Millisecond || runDirTermGrace != 2*time.Second || runDirKillGrace != 1*time.Second {
		t.Errorf("poll %v, SIGTERM grace %v, SIGKILL grace %v, want 50ms, 2s and 1s",
			runDirPollInterval, runDirTermGrace, runDirKillGrace)
	}
}
