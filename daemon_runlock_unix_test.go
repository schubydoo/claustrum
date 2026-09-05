//go:build unix

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
		t.Skip("run-dir eviction needs a /proc machine identity (Linux)")
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

func TestClaimRunDirEscalatesToSIGKILL(t *testing.T) {
	if nodeID() == "" {
		t.Skip("run-dir eviction needs a /proc machine identity (Linux)")
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

func TestClaimRunDirRefusesForeignHolder(t *testing.T) {
	if nodeID() == "" {
		t.Skip("needs a /proc machine identity (Linux)")
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
