package main

import (
	"encoding/json"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/schubydoo/claustrum/internal/supervisor"
)

// The reference daemon's environ contains CLAUDE_SSH_DAEMON_CHILD=1 after the
// self-daemonize re-exec, and that marker is propagated verbatim into every
// process.spawn child (observed in the real spawned agent's /proc/<pid>/environ).
// claustrum sets the same marker (daemonChildMarker) on its re-exec in
// daemonizeWithToken and supervisor.BuildEnv uses os.Environ() as the base, so it must leak
// to children too. Pin that behavior: a regression that swapped supervisor.BuildEnv's base
// for an empty []string or a curated allow-list — or that dropped the marker
// when the internal CLAUSTRUM_DAEMON_CHILD sentinel was split out — would diverge
// silently: no wire frame mentions the marker, but downstream tooling can detect
// the absence by inspecting its own environment.

func TestBuildEnvMergesOverEnviron(t *testing.T) {
	t.Setenv("CLAUSTRUM_TEST_KEEP", "base")
	t.Setenv("CLAUSTRUM_TEST_OVERRIDE", "old")

	env := supervisor.BuildEnv(map[string]string{
		"CLAUSTRUM_TEST_OVERRIDE": "new",
		"CLAUSTRUM_TEST_ADDED":    "added",
	})

	if !slices.Contains(env, "CLAUSTRUM_TEST_KEEP=base") {
		t.Error("inherited environ entry was dropped")
	}
	if !slices.Contains(env, "CLAUSTRUM_TEST_OVERRIDE=new") {
		t.Error("caller override was not applied")
	}
	if slices.Contains(env, "CLAUSTRUM_TEST_OVERRIDE=old") {
		t.Error("stale value survived the override")
	}
	if !slices.Contains(env, "CLAUSTRUM_TEST_ADDED=added") {
		t.Error("new caller key was not appended")
	}
}

// TestNewCallerKeysComeInARotation pins the order of new caller keys (89cb6289, measured
// on Linux, 20 spawns each). For the request order ZZ, MM, AA the reference gave ZZ, MM,
// AA, then MM, AA, ZZ, then AA, ZZ, MM: the three rotations of the request order. It
// never gave name order, AA, MM, ZZ, which is not one of them.
//
// supervisor.BuildEnv iterates the map that the request decodes into. This test holds what the Go
// 1.26 toolchain does with that map: each order is a rotation of the request order. If
// a toolchain changes the walk of a small map, this test fails. The rule then needs a
// new measurement.
func TestNewCallerKeysComeInARotation(t *testing.T) {
	request := []string{"CLAUSTRUM_TEST_ZZ", "CLAUSTRUM_TEST_MM", "CLAUSTRUM_TEST_AA"}
	for _, k := range append(slices.Clone(request), placeholderName) {
		if _, had := os.LookupEnv(k); had {
			t.Skipf("%s is set in the environment of this test", k)
		}
	}
	rotations := map[string]bool{}
	for i := range request {
		rotations[strings.Join(append(slices.Clone(request[i:]), request[:i]...), ",")] = true
	}
	seen := map[string]int{}
	for range 400 {
		// Decode the keys as process.spawn does, in the order of the request.
		var env map[string]string
		if err := json.Unmarshal([]byte(`{"`+request[0]+`":"1","`+request[1]+`":"2","`+request[2]+`":"3"}`), &env); err != nil {
			t.Fatal(err)
		}
		// The placeholder entry of 4c111e86 comes after the new caller keys.
		got := supervisor.BuildEnv(env)
		if got[len(got)-1] != placeholderEntry {
			t.Fatalf("the last entry is %q, want %s", got[len(got)-1], placeholderEntry)
		}
		got = got[:len(got)-1]
		var order []string
		for _, e := range got[len(got)-3:] {
			k, _, _ := strings.Cut(e, "=")
			order = append(order, k)
		}
		seen[strings.Join(order, ",")]++
	}
	for order, n := range seen {
		if !rotations[order] {
			t.Errorf("the new caller keys came as %s in %d of 400 calls, which is no rotation of the request order", order, n)
		}
	}
	if len(seen) < 2 {
		t.Errorf("the new caller keys came in one order in 400 calls (%v), want more than one rotation", seen)
	}
}

func TestSpawnInheritsDaemonChildMarker(t *testing.T) {
	t.Setenv("CLAUDE_SSH_DAEMON_CHILD", "1")
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	c, frames := pipeConn(t)
	printenv, env := helperCommand(t, "printenv")
	if _, err := m.Spawn(c, "envcheck", printenv,
		[]string{"CLAUDE_SSH_DAEMON_CHILD"}, "", env, false); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if got := firstStdout(t, frames); got != "CLAUDE_SSH_DAEMON_CHILD=1" {
		t.Errorf("child saw %q, want %q — daemon re-exec marker must propagate to spawned children for reference parity", got, "CLAUDE_SSH_DAEMON_CHILD=1")
	}
}

// The reference binary strips CLAUDE_RPC_TOKEN from the env it passes to
// process.spawn children (probe-verified 2026-06-09 against linux-amd64
// 8de85faa). supervisor.BuildEnv must filter it out even when the daemon's own process
// environment contains it (e.g. when Desktop sets it for the bridge client).
func TestSpawnDoesNotInheritRPCToken(t *testing.T) {
	t.Setenv("CLAUDE_RPC_TOKEN", "secret-must-not-leak")
	m := newTestProcManager(t)
	t.Cleanup(m.KillAll)
	c, frames := pipeConn(t)
	printenv, env := helperCommand(t, "printenv")
	if _, err := m.Spawn(c, "tokencheck", printenv,
		[]string{"CLAUDE_RPC_TOKEN"}, "", env, false); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	// An empty value means the var was absent; anything else means it leaked.
	if got := firstStdout(t, frames); got != "CLAUDE_RPC_TOKEN=" {
		t.Errorf("child saw %q, want %q — CLAUDE_RPC_TOKEN must not propagate to spawned children", got, "CLAUDE_RPC_TOKEN=")
	}
}

// TestSpawnDoesNotInheritPeerCheck: after the start of the daemon, a spawned child
// gets no CLAUDE_SSH_PEER_CHECK of the daemon, for the values 1 and 0, as on
// 5fd08069 (measured on Linux, macOS and Windows). A marker variable of the daemon is
// the control: it arrives. A caller env param of that name arrives too, as on 5fd08069
// (measured on Linux, macOS and Windows). The child is the helper of this test binary,
// and its lookupenv mode tells an absent variable from an empty one.
func TestSpawnDoesNotInheritPeerCheck(t *testing.T) {
	const marker = "CLAUSTRUM_TEST_PEER_MARK"
	for _, value := range []string{"1", "0"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv(peerCheckEnv, value)
			t.Setenv(marker, "1")
			captureLogBuf(t)
			startPeerCheck()
			m := newTestProcManager(t)
			t.Cleanup(m.KillAll)
			cases := []struct{ id, name, param, want string }{
				{"peer", peerCheckEnv, "", peerCheckEnv + " absent"},
				{"mark", marker, "", marker + " set 1"},
				{"param", peerCheckEnv, "x", peerCheckEnv + " set x"},
			}
			for _, tc := range cases {
				c, frames := pipeConn(t)
				exe, env := helperCommand(t, "lookupenv")
				if tc.param != "" {
					env[tc.name] = tc.param
				}
				if _, err := m.Spawn(c, tc.id, exe, []string{tc.name}, "", env, false); err != nil {
					t.Fatalf("spawn: %v", err)
				}
				if got := firstStdout(t, frames); got != tc.want {
					t.Errorf("child %s saw %q, want %q", tc.id, got, tc.want)
				}
			}
		})
	}
}

// placeholderName is the env entry that 4c111e86 puts in the environment of a spawned
// child, and placeholderEntry is its value when the request gives none.
const (
	placeholderName  = "CLAUDE_SSH_TAKES_COMMONDIR_PLACEHOLDER"
	placeholderEntry = placeholderName + "=1"
)

// TestBuildEnvAddsThePlaceholderEntry pins the rows of 4c111e86, measured on Linux and
// macOS. With no request value the child gets the entry with 1, once, after the new
// caller keys (measured on Linux). A request value wins. A daemon entry takes the value
// 1 in place, unless the request gives a value. On Windows the rule is claustrum's own,
// not measured.
func TestBuildEnvAddsThePlaceholderEntry(t *testing.T) {
	cases := []struct {
		name    string
		daemon  *string // the value in the environment of the daemon, nil for none
		request *string // the value in the env of the request, nil for none
		want    string
	}{
		{"no value anywhere", nil, nil, "1"},
		{"request 0", nil, ptr("0"), "0"},
		{"request empty", nil, ptr(""), ""},
		{"request 1", nil, ptr("1"), "1"},
		{"request yes", nil, ptr("yes"), "yes"},
		{"daemon 0", ptr("0"), nil, "1"},
		{"daemon 1", ptr("1"), nil, "1"},
		{"daemon 1 and request 0", ptr("1"), ptr("0"), "0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range []string{placeholderName, "CLAUSTRUM_TEST_NEXT", "CLAUSTRUM_TEST_NEW"} {
				t.Setenv(k, "") // restores the old state at the end
				os.Unsetenv(k)
			}
			if tc.daemon != nil {
				t.Setenv(placeholderName, *tc.daemon)
			}
			t.Setenv("CLAUSTRUM_TEST_NEXT", "x")
			req := map[string]string{"CLAUSTRUM_TEST_NEW": "y"}
			if tc.request != nil {
				req[placeholderName] = *tc.request
			}
			got := supervisor.BuildEnv(req)
			var at []int
			for i, e := range got {
				if strings.HasPrefix(e, placeholderName+"=") {
					at = append(at, i)
				}
			}
			if len(at) != 1 || got[at[0]] != placeholderName+"="+tc.want {
				t.Fatalf("the entries of %s are at %v in %q, want one with %q", placeholderName, at, got, tc.want)
			}
			next := slices.Index(got, "CLAUSTRUM_TEST_NEXT=x")
			switch {
			case tc.daemon != nil:
				// The daemon entry keeps its place, before the later daemon entry. The
				// order of the environment of this test process is fixed on Unix only.
				if runtime.GOOS != "windows" && at[0] != next-1 {
					t.Errorf("the entry is at %d, want %d, the place of the daemon entry: %q", at[0], next-1, got)
				}
			case tc.request == nil:
				// After the new caller key, as the last entry.
				if at[0] != len(got)-1 || got[at[0]-1] != "CLAUSTRUM_TEST_NEW=y" {
					t.Errorf("the entry is at %d of %d, want it last, after the new caller key: %q", at[0], len(got), got)
				}
			default:
				// A request value is a new caller key, after the daemon entries.
				if at[0] < len(got)-2 {
					t.Errorf("the entry is at %d of %d, want it among the new caller keys: %q", at[0], len(got), got)
				}
			}
		})
	}
}

func ptr(s string) *string { return &s }
