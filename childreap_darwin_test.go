//go:build darwin

package main

import (
	"encoding/binary"
	"testing"
)

// fakePS seams the darwin live-process reader: darwinProcStart returns start, runPS returns
// fieldsLine for the "pgid=,stat=,command=" read, and readProcEnv returns env. An empty start
// or fieldsLine makes the process read as gone, matching a real ps that found no such process.
func fakePS(t *testing.T, start, fieldsLine string, env []string) {
	t.Helper()
	oldStart, oldRun, oldEnv := darwinProcStart, runPS, readProcEnv
	t.Cleanup(func() { darwinProcStart, runPS, readProcEnv = oldStart, oldRun, oldEnv })
	darwinProcStart = func(int) string { return start }
	runPS = func(int, string) string { return fieldsLine }
	readProcEnv = func(int) []string { return env }
}

// TestRealReadLiveProcDarwinCommandText proves the darwin reader hands the record test
// the command text of `ps` and its words, through the runPS seam. The line has the
// padding that `ps` puts after the first two columns, and a program path with a blank.
// The run on a Mac covers only this wiring. What a real `ps` prints is measured on the
// VM, not here.
func TestRealReadLiveProcDarwinCommandText(t *testing.T) {
	const start = "Sun Sep 13 08:32:51 2026"
	fakePS(t, start, "54321 S    /tmp/e/bin/my stub --log /tmp/e/c1.log", nil)
	lp := realReadLiveProc(54321, true)
	if lp.cmdText != "/tmp/e/bin/my stub --log /tmp/e/c1.log" || lp.program != "/tmp/e/bin/my" {
		t.Fatalf("cmdText = %q, program = %q, want the whole command text and its first word", lp.cmdText, lp.program)
	}
	if want := []string{"stub", "--log", "/tmp/e/c1.log"}; len(lp.args) != 3 || lp.args[0] != want[0] || lp.args[1] != want[1] || lp.args[2] != want[2] {
		t.Errorf("args = %q, want %q", lp.args, want)
	}
	// Rows PG04s and PG02s: the record of a program with a blank in its path matches.
	if !recordMatchesLive(childRecord{Argv0: "/tmp/e/bin/my stub"}, lp) {
		t.Error("a record of the program with a blank in its path did not match")
	}
	if !recordMatchesLive(childRecord{Argv0: "/tmp/e/bin/wrap", Program: "/tmp/e/bin/my stub"}, lp) {
		t.Error("a launcher record whose program has a blank in its path did not match")
	}
}

// TestRealReadLiveProcDarwin exercises the darwin reader's arms: a full live record with both
// env markers, the two gone paths (no start-time, no ps line), a zombie, and a short line that
// is not ours to judge.
func TestRealReadLiveProcDarwin(t *testing.T) {
	const start = "Sun Sep 13 08:32:51 2026"
	// A live process running /usr/bin/node, tagged with our run dir and a direct-child marker
	// whose start-time (with spaces) equals the recorded start.
	fakePS(t, start, "54321 S /usr/bin/node", []string{
		"CLAUDE_SSH_RUN_DIR=/run/x",
		"CLAUDE_SSH_CHILD=54321:" + start,
		"TERM=xterm",
	})
	lp := realReadLiveProc(54321, true)
	if lp.state != procAlive {
		t.Fatalf("state = %d, want procAlive", lp.state)
	}
	if lp.pgid != 54321 || lp.program != "/usr/bin/node" || lp.startTicks != start {
		t.Errorf("parsed wrong: pgid=%d program=%q startTicks=%q", lp.pgid, lp.program, lp.startTicks)
	}
	if lp.runDir != "/run/x" {
		t.Errorf("runDir = %q, want /run/x", lp.runDir)
	}
	if lp.childMark != "54321:"+start {
		t.Errorf("childMark = %q, want %q", lp.childMark, "54321:"+start)
	}

	// No start-time: ps -o lstart found nothing -> gone.
	fakePS(t, "", "54321 S /usr/bin/node", nil)
	if lp := realReadLiveProc(54321, false); lp.state != procGone {
		t.Errorf("no start-time: state = %d, want procGone", lp.state)
	}

	// A start-time but no fields line (the second ps found nothing) -> gone.
	fakePS(t, start, "", nil)
	if lp := realReadLiveProc(54321, false); lp.state != procGone {
		t.Errorf("no ps line: state = %d, want procGone", lp.state)
	}

	// A zombie (state begins with 'Z') -> gone.
	fakePS(t, start, "54321 Z+ /usr/bin/node", nil)
	if lp := realReadLiveProc(54321, false); lp.state != procGone {
		t.Errorf("zombie: state = %d, want procGone", lp.state)
	}

	// Fewer than three fields (no command) -> not ours to judge.
	fakePS(t, start, "54321 S", nil)
	if lp := realReadLiveProc(54321, false); lp.state != procNotOurs {
		t.Errorf("short line: state = %d, want procNotOurs", lp.state)
	}
}

// procArgs2Buf builds a synthetic KERN_PROCARGS2 buffer from argv and env, in the darwin
// layout: a 4-byte argc, the exec path (NUL) plus a padding NUL, then the argv strings (NUL),
// then the env strings (NUL).
func procArgs2Buf(argv, env []string) []byte {
	var argc [4]byte
	binary.LittleEndian.PutUint32(argc[:], uint32(len(argv)))
	b := append([]byte{}, argc[:]...)
	if len(argv) > 0 {
		b = append(b, argv[0]...) // exec path
	}
	b = append(b, 0, 0) // exec-path terminator + one padding NUL
	for _, a := range argv {
		b = append(b, a...)
		b = append(b, 0)
	}
	for _, e := range env {
		b = append(b, e...)
		b = append(b, 0)
	}
	return b
}

// TestParseProcArgs2Env confirms the NUL-delimited read keeps a marker value intact even when
// it contains spaces, '=', and a " NAME=" pattern all at once — the case a space-joined `ps -E`
// line cannot disambiguate and the reference nonetheless reaps (VM-measured).
func TestParseProcArgs2Env(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"CLAUDE_SSH_RUN_DIR=/Users/a b=c FOO=bar/run/x",
		"CLAUDE_SSH_CHILD=54321:Sun Sep 13 08:32:51 2026",
		"TERM=xterm",
	}
	got := parseProcArgs2Env(procArgs2Buf([]string{"/bin/sleep", "8888"}, env))
	if len(got) != len(env) {
		t.Fatalf("got %d entries %q, want %d %q", len(got), got, len(env), env)
	}
	for i := range env {
		if got[i] != env[i] {
			t.Errorf("entry %d = %q, want %q", i, got[i], env[i])
		}
	}
	if v := envLookup(got, "CLAUDE_SSH_RUN_DIR="); v != "/Users/a b=c FOO=bar/run/x" {
		t.Errorf("RUN_DIR = %q, want %q", v, "/Users/a b=c FOO=bar/run/x")
	}
	if v := envLookup(got, "CLAUDE_SSH_CHILD="); v != "54321:Sun Sep 13 08:32:51 2026" {
		t.Errorf("CHILD = %q, want %q", v, "54321:Sun Sep 13 08:32:51 2026")
	}
	if v := envLookup(got, "MISSING="); v != "" {
		t.Errorf("absent = %q, want empty", v)
	}
	// A short buffer yields no entries rather than a panic.
	if got := parseProcArgs2Env([]byte{1, 2}); got != nil {
		t.Errorf("short buffer = %q, want nil", got)
	}
}

// TestEarlierBootOfThisMachineDarwin covers the darwin node shape: a bare boot-session UUID
// with no "/". Two different UUIDs on the same host are an earlier boot of this machine and
// must return true, which the pre-fix `ok`-gated Cut wrongly rejected on darwin.
func TestEarlierBootOfThisMachineDarwin(t *testing.T) {
	const host = "mac-host"
	const uuidA = "6DB2FCC7-9427-4A74-BA07-7955DE0C597C"
	const uuidB = "0F1E2D3C-4B5A-6978-8796-A5B4C3D2E1F0"
	cases := []struct {
		name                               string
		recHost, recNode, ownHost, ownNode string
		want                               bool
	}{
		{"same machine earlier boot", host, uuidA, host, uuidB, true},
		{"same machine same boot", host, uuidA, host, uuidA, false},
		{"different machine", "other-host", uuidA, host, uuidB, false},
		{"empty node id", host, "", host, uuidB, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := earlierBootOfThisMachine(tc.recHost, tc.recNode, tc.ownHost, tc.ownNode); got != tc.want {
				t.Errorf("earlierBootOfThisMachine = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestOwnReapIdentityDarwin confirms the reap's node/host come from the same darwin sources
// childrecord writes: the boot-session UUID and the hostname.
func TestOwnReapIdentityDarwin(t *testing.T) {
	oldBoot, oldHost := bootSessionUUID, darwinHostname
	t.Cleanup(func() { bootSessionUUID, darwinHostname = oldBoot, oldHost })
	bootSessionUUID = func() string { return "BOOT-UUID" }
	darwinHostname = func() string { return "myhost" }

	node, host := ownReapIdentity()
	if node != "BOOT-UUID" || host != "myhost" {
		t.Errorf("ownReapIdentity = (%q, %q), want (BOOT-UUID, myhost)", node, host)
	}
}
