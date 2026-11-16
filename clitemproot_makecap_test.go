//go:build linux || darwin

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/schubydoo/claustrum/internal/rpc"
)

// The tests of the cap of new folders, on Linux and macOS.

// capEnv is the env of a spawn whose temp dir is x on both systems.
func capEnv(x string) []string {
	return []string{"TMPDIR=" + x, "CLAUDE_CODE_TMPDIR=" + x}
}

// runCapSpawn runs the step for a spawn in x and keeps its release.
func runCapSpawn(t *testing.T, x, runDir string, releases *[]func()) ([]string, *rpc.CLITempRoot) {
	t.Helper()
	env, res, release, _ := privateTempRoot(capEnv(x), runDir)
	*releases = append(*releases, release)
	return env, res
}

// The ninth make within 60 s gets no folder, with the text of the cap, and the child
// keeps its env (4c111e86, measured on macOS). A make 60 s old no longer counts, and
// the window slides with each make (claustrum's own, not measured).
func TestCLITempRootCapOfEightMakesInAMinute(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	logs := captureLogBuf(t)
	advance := fakeTempRootClock(t)
	for i := range tempRootMakeCap {
		x, runDir := tempRootDir(t, true)
		if _, res := runCapSpawn(t, x, runDir, &rel); res == nil || res.Name == "" {
			t.Fatalf("make %d: %+v, want a name", i+1, res)
		}
		rel[len(rel)-1]() // the child ended: the place of the live cap is free
		advance(time.Second)
	}
	waitTempRootsLive(t, 0)
	advance(51 * time.Second) // 59 s after the first make
	x, runDir := tempRootDir(t, true)
	env, res := runCapSpawn(t, x, runDir, &rel)
	b, _ := json.Marshal(res)
	if want := `{"refused":"not readable by its owner","whyNot":"too many sessions of yours on this host have a private temp folder of their own; end one, or try again in a minute"}`; string(b) != want {
		t.Fatalf("ninth answer %s, want %s", b, want)
	}
	if !slices.Equal(env, capEnv(x)) || len(ccdFolders(t, x)) != 0 {
		t.Errorf("the ninth got env %q and folders %v, want its own env and none", env, ccdFolders(t, x))
	}
	want := "[process.Manager] Claude Code's temp folder is unfit (it is " + unfitKind + "), but no other was made: " + tempRootTooMany + "\n"
	if !strings.Contains(logs.String(), want) {
		t.Errorf("log %q, want %q", logs.String(), want)
	}
	// The seconds of the lost entry come from the real clock, so they are not pinned.
	addLostEntry("ccd-0123456789", "changed")
	_, res = runCapSpawn(t, x, runDir, &rel)
	b, _ = json.Marshal(res)
	if want := `{"refused":"not readable by its owner","whyNot":"` + tempRootTooMany + `","lost":[{"name":"ccd-0123456789","why":"changed","agoSeconds":`; !strings.HasPrefix(string(b), want) || len(res.Gone) != 1 {
		t.Fatalf("answer with a loss %s, want it to start %s with one entry", b, want)
	}
	advance(time.Second) // 60 s after the first make
	if _, res := runCapSpawn(t, x, runDir, &rel); res == nil || res.Name == "" {
		t.Fatalf("60 s after the first make: %+v, want a name", res)
	}
	x2, runDir2 := tempRootDir(t, true)
	if _, res := runCapSpawn(t, x2, runDir2, &rel); res == nil || res.WhyNot != tempRootTooMany {
		t.Fatalf("59 s after the second make: %+v, want the text of the cap", res)
	}
}

// A spawn that takes an intact folder again makes none, so it does not count
// (4c111e86, measured on macOS).
func TestCLITempRootCapSkipsATakeAgain(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	fakeTempRootClock(t)
	x, runDir := tempRootDir(t, true)
	_, first := runCapSpawn(t, x, runDir, &rel)
	if first == nil || first.Name == "" {
		t.Fatalf("first: %+v, want a name", first)
	}
	for i := range 2 * tempRootMakeCap {
		rel[len(rel)-1]()
		waitTempRootsLive(t, 0)
		if _, res := runCapSpawn(t, x, runDir, &rel); res == nil || res.Name != first.Name {
			t.Fatalf("take %d: %+v, want the name %s", i+1, res, first.Name)
		}
	}
	for i := 1; i < tempRootMakeCap; i++ {
		x, runDir := tempRootDir(t, true)
		if _, res := runCapSpawn(t, x, runDir, &rel); res == nil || res.Name == "" {
			t.Fatalf("make %d: %+v, want a name", i+1, res)
		}
	}
	for _, r := range rel {
		r() // the place of the live cap is free
	}
	waitTempRootsLive(t, 0)
	x9, runDir9 := tempRootDir(t, true)
	if _, res := runCapSpawn(t, x9, runDir9, &rel); res == nil || res.WhyNot != tempRootTooMany {
		t.Fatalf("ninth make: %+v, want the text of the cap", res)
	}
}

// A folder that was removed still counts (4c111e86, measured on macOS). A spawn in
// its temp dir then needs a make, and gets none.
func TestCLITempRootCapCountsARemovedFolder(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	fakeTempRootClock(t)
	var firstDir, firstRun string
	for i := range tempRootMakeCap {
		x, runDir := tempRootDir(t, true)
		_, res := runCapSpawn(t, x, runDir, &rel)
		if res == nil || res.Name == "" {
			t.Fatalf("make %d: %+v, want a name", i+1, res)
		}
		rel[len(rel)-1]()
		waitTempRootsLive(t, 0)
		openTree(filepath.Join(x, res.Name))
		if err := os.RemoveAll(filepath.Join(x, res.Name)); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			firstDir, firstRun = x, runDir
		}
	}
	if _, res := runCapSpawn(t, firstDir, firstRun, &rel); res == nil || res.WhyNot != tempRootTooMany {
		t.Fatalf("a spawn where the first folder was: %+v, want the text of the cap", res)
	}
	x, runDir := tempRootDir(t, true)
	if _, res := runCapSpawn(t, x, runDir, &rel); res == nil || res.WhyNot != tempRootTooMany {
		t.Fatalf("ninth make: %+v, want the text of the cap", res)
	}
}

// A make whose new folder fails its test and is removed still counts (claustrum's
// own, not measured).
func TestCLITempRootCapCountsAFailedNewFolder(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	fakeTempRootClock(t)
	setAfterMkdir(t, func(dir, name string) { _ = os.Chmod(filepath.Join(dir, name), 0o755) })
	for i := range tempRootMakeCap {
		x, runDir := tempRootDir(t, true)
		if _, res := runCapSpawn(t, x, runDir, &rel); res == nil || res.WhyNot != tempRootRemoved {
			t.Fatalf("make %d: %+v, want the text of a removed folder", i+1, res)
		}
	}
	afterTempRootMkdir = func(dir, name string) {}
	x, runDir := tempRootDir(t, true)
	if _, res := runCapSpawn(t, x, runDir, &rel); res == nil || res.WhyNot != tempRootTooMany {
		t.Fatalf("ninth make: %+v, want the text of the cap", res)
	}
}

// The record of makes keeps no make older than the window, so it holds at most
// tempRootMakeCap times.
func TestCLITempRootCapRecordStaysSmall(t *testing.T) {
	var rel []func()
	cleanTempRoots(t, &rel)
	advance := fakeTempRootClock(t)
	for i := range 3 * tempRootMakeCap / 2 {
		x, runDir := tempRootDir(t, true)
		if _, res := runCapSpawn(t, x, runDir, &rel); res == nil || res.Name == "" {
			t.Fatalf("make %d: %+v, want a name", i+1, res)
		}
		rel[len(rel)-1]()
		waitTempRootsLive(t, 0)
		advance(tempRootMakeWindow)
	}
	tempRoots.mu.Lock()
	n := len(tempRoots.made)
	tempRoots.mu.Unlock()
	if n > tempRootMakeCap {
		t.Errorf("the record holds %d makes, want at most %d", n, tempRootMakeCap)
	}
}
