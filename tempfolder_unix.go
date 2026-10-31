//go:build !windows

package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// tempFolderNames are the inherited names whose folder a spawn tests. TMP is not
// one of them (claustrum's own, not measured).
var tempFolderNames = []string{"TMPDIR", "TEMP", "CLAUDE_CODE_TMPDIR"}

// tempProbeWait is the longest time that a spawn waits for the tests of all its
// folders. After it each name whose test has not ended stays. A test of 683cc6a9
// held the remove 3 s, and the replies came at 2.17 s with the name kept. The 2 s
// bound per spawn is claustrum's own, not measured. A variable for the tests.
var tempProbeWait = 2 * time.Second

// afterTempProbeCreate runs between the create and the remove of the test file. It
// is a seam for the tests. Production never reassigns it.
var afterTempProbeCreate = func(path string) {}

// tempProbe is one test of one folder. Spawns that ask while it runs share it.
type tempProbe struct {
	done    chan struct{}
	err     error
	waiters int // for the tests
}

var tempProbes = struct {
	mu       sync.Mutex
	inFlight map[string]*tempProbe
}{inFlight: map[string]*tempProbe{}}

// dropUnusableTempVars removes from env each name of tempFolderNames whose
// inherited folder cannot take a file, and logs one line for each. A folder that
// does not exist is not tested and stays (683cc6a9, measured on Linux and macOS). A
// relative value is not tested and stays too (measured on Linux).
//
// The tests of all names start first and share one deadline, so a spawn waits at
// most tempProbeWait in all (claustrum's own, not measured).
func dropUnusableTempVars(env []string) []string {
	type tested struct {
		name, dir string
		p         *tempProbe
	}
	var runs []tested
	for _, name := range tempFolderNames {
		dir, ok := os.LookupEnv(name)
		if !ok || !filepath.IsAbs(dir) {
			continue
		}
		// A stat error other than "does not exist" leaves the test to the create
		// (claustrum's own, not measured).
		if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		runs = append(runs, tested{name, dir, startTempProbe(dir)})
	}
	if len(runs) == 0 {
		return env
	}
	t := time.NewTimer(tempProbeWait)
	defer t.Stop()
	expired := false
	for _, r := range runs {
		if expired {
			break
		}
		select {
		case <-r.p.done:
		case <-t.C:
			expired = true
			continue
		}
		if r.p.err != nil {
			logWarnf("[process.Manager] the inherited %s=%s is not handed on to a child: %v", r.name, r.dir, r.p.err)
			env = removeEnvKey(env, r.name)
		}
	}
	return env
}

// startTempProbe starts one test of dir, or joins the one that runs. A spawn that
// stops waiting first keeps the name.
func startTempProbe(dir string) *tempProbe {
	tempProbes.mu.Lock()
	defer tempProbes.mu.Unlock()
	p := tempProbes.inFlight[dir]
	if p == nil {
		p = &tempProbe{done: make(chan struct{})}
		tempProbes.inFlight[dir] = p
		go func() {
			p.err = createAndRemoveTempProbe(dir)
			tempProbes.mu.Lock()
			delete(tempProbes.inFlight, dir)
			tempProbes.mu.Unlock()
			close(p.done)
		}()
	}
	p.waiters++
	return p
}

// createAndRemoveTempProbe makes one new file .claude-ssh-<random> in dir, with
// O_RDWR|O_CREATE|O_EXCL and mode 0600, and removes it by that name. The exclusive
// create never opens a file that was there. A failed create removes nothing. Before
// the remove, an Lstat and os.SameFile test that the name still holds the file that
// this call made, so a renamed swap and a symbolic link stay (claustrum's own). A
// writer with the same uid can still place one entry at the name between that test
// and the remove, and that entry then gets one unlink. POSIX has no unlink that
// checks the inode. The error of a failed create is the one that the log line
// quotes: "open <dir>/.claude-ssh-<n>: <reason>".
func createAndRemoveTempProbe(dir string) error {
	f, err := os.CreateTemp(dir, ".claude-ssh-*")
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	name := f.Name()
	afterTempProbeCreate(name)
	made, err := f.Stat()
	if err != nil {
		return nil
	}
	if now, err := os.Lstat(name); err == nil && os.SameFile(made, now) {
		_ = os.Remove(name)
	}
	return nil
}
