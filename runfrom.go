package main

import (
	"fmt"
	"path/filepath"
	"time"
)

// runFromEnv names the file that a launcher asks the binary to check before it
// runs. 13047a51 reads it, measured on Linux, macOS and Windows.
const runFromEnv = "CLAUDE_SSH_RUN_FROM"

// placeRefusedExit is the exit code of a refused place. 13047a51 exits 78 for
// -check-place, -serve, -bridge, -install and no mode, measured on Linux. macOS
// and Windows show it for -check-place and -serve, Windows for -bridge and no
// mode too.
const placeRefusedExit = 78

// The texts of a refused place, copied from the 13047a51 rows on Linux. The
// host text is the one of macOS and Windows.
const (
	notAskedText    = runFromEnv + " is not set, or is empty, so there is nothing to check."
	hostText        = "Only a Linux host's file can be checked."
	notAbsoluteText = "the helper must be an absolute path, not a bare name resolved via PATH"
	anotherFileText = "The program that runs is not the file at that path. If the file was replaced while the helper ran, connect again."
	stalledText     = "The file could not be checked within 8 seconds. If a folder on the way to it is on a network share, check that its server is reachable."
	modeText        = "It must be a regular file that gives the helper no more privilege than the account has: no setuid or setgid bit, no file capabilities (`setcap -r` removes them)."
)

// placeDenial is one refused place: its kind, the path that the line names
// and the reason. path is empty for not_asked, whose line names no path.
// settle is set when the check ran past its bound: it answers when the check
// returns.
type placeDenial struct {
	kind   string
	path   string
	text   string
	settle <-chan *placeDenial
}

// awaitCheck waits for a check that ran past its bound to return. 13047a51
// prints the line at 8 s and exits only when the stalled call returns
// (measured on Linux, a stalled FUSE mount). Go cannot stop that call, so
// claustrum waits for it too.
func (r *placeDenial) awaitCheck() {
	if r.settle != nil {
		<-r.settle
	}
}

// line is the stderr line of the refusal. 13047a51 writes "claude-ssh:" first.
// claustrum writes its own program name there, as in its other stderr lines.
func (r *placeDenial) line() string {
	if r.path == "" {
		return fmt.Sprintf("claustrum: refusing to run (%s): %s\n", r.kind, r.text)
	}
	return fmt.Sprintf("claustrum: refusing to run (%s) from %s: %s\n", r.kind, r.path, r.text)
}

// placeCheckBound bounds the whole place check. 13047a51 refuses a check that
// takes longer with cannot_tell and stalledText (measured on Linux). A test
// sets a short bound.
var placeCheckBound = 8 * time.Second

// examineRunFrom checks the place of the value of CLAUDE_SSH_RUN_FROM. It answers
// nil when the place passes. An empty value is not_asked. The line names the
// cleaned path: Windows prints a path with "/" with "\" (measured), and the
// Clean of the other systems is from the code. A check past placeCheckBound
// is refused (measured on Linux), also when it passes later (claustrum's own,
// not measured). A known difference (measured on Linux): after one 5 s stall
// on FUSE, 13047a51 gives the 8 s text, and claustrum gives the FUSE text.
func examineRunFrom(place, version string) *placeDenial {
	if place == "" {
		return &placeDenial{kind: "not_asked", text: notAskedText}
	}
	path := filepath.Clean(place)
	done := make(chan *placeDenial, 1)
	go func() { done <- examinePlace(path, version) }()
	bound := time.NewTimer(placeCheckBound)
	defer bound.Stop()
	select {
	case r := <-done:
		return r
	case <-bound.C:
		return &placeDenial{kind: "cannot_tell", path: path, text: stalledText, settle: done}
	}
}

// placeVersion is the version that the shape of a place names. 13047a51 names
// the version that its -version line prints. claustrum's equivalent is the
// version word of its own -version line: the version-override of
// claustrum.conf when it is set, and Version otherwise (from the code).
func placeVersion(override string) string {
	if override != "" {
		return override
	}
	return Version
}

// placeGateRuns reports whether a mode checks CLAUDE_SSH_RUN_FROM before it
// runs. -stop alone does not check: with a wrong place 13047a51 prints "none"
// and exits 0 (measured on Linux, macOS and Windows). -stop with -serve,
// -bridge or -install runs that other mode, so it checks (from the code).
func placeGateRuns(stop, serve, bridge, install bool) bool {
	return !stop || serve || bridge || install
}

// gateRunFrom refuses to run from a place that fails the check, with exit 78.
// An unset or empty CLAUDE_SSH_RUN_FROM checks nothing (measured on Linux:
// -serve runs). The empty value is measured for -check-place only. For a serve
// run it first removes the -token-file, unless that is a folder (13047a51,
// measured on Linux, a regular file on macOS and Windows). After a stall past
// 8 s both remove it at the bound, before the wait (measured on Linux).
func gateRunFrom(stderr func(string), place, version string, serveRuns bool, tokenFile string) {
	if place == "" {
		return
	}
	r := examineRunFrom(place, version)
	if r == nil {
		return
	}
	if serveRuns && tokenFile != "" {
		removeRefusedTokenFile(tokenFile)
	}
	stderr(r.line())
	r.awaitCheck()
	osExit(placeRefusedExit)
}
