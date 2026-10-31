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

// tempFolderNames are the inherited names whose folder a spawn tests, in the order
// of the tests and of the log lines (claustrum's own, not measured).
var tempFolderNames = []string{"CLAUDE_CODE_TMPDIR", "TMPDIR", "TMP", "TEMP"}

// tempProbeWait is the longest time that a spawn waits for one test, counted from
// the start of that test. After it the name stays. A test of 683cc6a9 held the
// remove 3 s, and the replies came at 2.17 s with the name kept. The bound of 2 s
// per test is claustrum's own, not measured. A variable for the tests.
var tempProbeWait = 2 * time.Second

// afterTempProbeCreate runs between the create and the remove of the test file. It
// is a seam for the tests. Production never reassigns it.
var afterTempProbeCreate = func(path string) {}

// tempProbe is one test of one folder. Spawns that ask while it runs share it.
type tempProbe struct {
	start   time.Time
	done    chan struct{}
	err     error
	waiters int // for the tests
}

var tempProbes = struct {
	mu       sync.Mutex
	inFlight map[string]*tempProbe
}{inFlight: map[string]*tempProbe{}}

// dropUnusableTempVars removes from env each name of tempFolderNames whose
// inherited folder cannot take a file, and logs one line for each. A relative value
// is not tested and stays (measured on Linux).
//
// The tests of all names start first. Then a spawn waits for each test until
// tempProbeWait after the start of that test, and keeps the name after it. A spawn
// that joins a test older than tempProbeWait does not wait. The spawn that started a test logs one
// line when its wait ends first (claustrum's own, not measured).
func dropUnusableTempVars(env []string) []string {
	type tested struct {
		name, dir string
		p         *tempProbe
		started   bool
	}
	var runs []tested
	for _, name := range tempFolderNames {
		dir, ok := os.LookupEnv(name)
		if !ok || !filepath.IsAbs(dir) {
			continue
		}
		p, started := startTempProbe(dir)
		runs = append(runs, tested{name, dir, p, started})
	}
	for _, r := range runs {
		if !waitTempProbe(r.p) {
			if r.started {
				logWarnf("[process.Manager] the temporary folder %s has not answered in %v: children keep it until it does", r.dir, tempProbeWait)
			}
			continue
		}
		if r.p.err != nil {
			logWarnf("[process.Manager] the inherited %s=%s is not handed on to a child: %v", r.name, r.dir, r.p.err)
			env = removeEnvKey(env, r.name)
		}
	}
	return env
}

// waitTempProbe waits until p ends or until tempProbeWait after its start, and
// reports whether it ended.
func waitTempProbe(p *tempProbe) bool {
	select {
	case <-p.done:
		return true
	default:
	}
	left := time.Until(p.start.Add(tempProbeWait))
	if left <= 0 {
		return false
	}
	t := time.NewTimer(left)
	defer t.Stop()
	select {
	case <-p.done:
		return true
	case <-t.C:
		return false
	}
}

// startTempProbe starts one test of dir, or joins the one that runs. It reports
// whether this call started it.
func startTempProbe(dir string) (*tempProbe, bool) {
	tempProbes.mu.Lock()
	defer tempProbes.mu.Unlock()
	p := tempProbes.inFlight[dir]
	started := p == nil
	if started {
		p = &tempProbe{start: time.Now(), done: make(chan struct{})}
		tempProbes.inFlight[dir] = p
		go func() {
			p.err = probeNearestFolder(dir)
			tempProbes.mu.Lock()
			delete(tempProbes.inFlight, dir)
			tempProbes.mu.Unlock()
			close(p.done)
		}()
	}
	p.waiters++
	return p, started
}

// probeNearestFolder tests dir, or the nearest folder above it that exists. After a
// failed create, a dir that does not exist passes the test to its parent. Any other
// result of the Lstat ends the test with the error of the create. The file is thus
// made and removed in the nearest existing folder (claustrum's own, not measured).
func probeNearestFolder(dir string) error {
	for {
		err := createAndRemoveTempProbe(dir)
		if err == nil {
			return nil
		}
		if _, lerr := os.Lstat(dir); !errors.Is(lerr, fs.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return err
		}
		dir = parent
	}
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
