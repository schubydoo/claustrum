package git

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// The run of a process.spawn with "gitRead":true: the pane's git. c9125eb8 runs
// such a git under the daemon's own copy of the repository's settings, as the git
// methods do in the state shadow (measured on Linux, macOS and Windows). The calls
// before it are the excludes read, the start test, the call that asks git about the
// repository, the read of its config and one listing `config -z --list` in the cwd
// (measured on Linux). The listing gives the hook pins of the pane's git.

// paneShape is one argv of the pane's git that a gitRead spawn may run.
type paneShape struct {
	// sent is the argv as the request sends it, after the command "git".
	sent []string
	// run is the argv after the `-c` items of the run.
	run []string
	// noLocks puts --no-optional-locks between paneHardenFirst and
	// paneHardenLast.
	noLocks bool
	// realIndex keeps the index of the repository. Every other shape gets
	// GIT_INDEX_FILE=<copy>/no-index.
	realIndex bool
}

// The slots of a shape. A slot in sent takes one request value that passes its
// test, and the same slot in run gives that value back, in the same order.
const (
	// slotRef is a ref that passes paneRefOK.
	slotRef = "<ref>"
	// slotRev is a slotRef. A value with ".." in it is <A>..<B> or <A>...<B>,
	// each side a slotRef.
	slotRev = "<rev>"
	// slotBase is the ref of a diff: a slotRef, or <A>..<B> with each side a
	// slotRef. A value <A>..<B> runs as diff-tree -r with A and B (paneTreeRun).
	slotBase = "<base>"
	// slotExclude is one pathspec after "--": ":(top,exclude)" or
	// ":(top,exclude,literal)", then a path that is not empty.
	slotExclude = "<exclude>"
)

// paneDiffOpts are the options of each diff run, after its first word.
var paneDiffOpts = []string{"--ignore-submodules=dirty", "--submodule=short", "--no-ext-diff", "--no-textconv"}

// paneDiffRun is the run of a diff shape: diff-index, -p when patch is true,
// paneDiffOpts, then rest.
func paneDiffRun(patch bool, rest ...string) []string {
	run := []string{"diff-index"}
	if patch {
		run = append(run, "-p")
	}
	return slices.Concat(run, paneDiffOpts, rest)
}

// paneShapes are the argv that c9125eb8 runs with "gitRead":true. The fixed
// words and their places are those of the measured rows, and the slots take the
// refs and paths of the rows (measured on Linux, macOS and Windows, the slots on
// Linux). Every other argv gets the shape refusal. The run argv of each shape is
// that of the call logs (measured on Linux, macOS and Windows). So are log with
// <A>...<B>, each diff shape with <A>..<B>, and the paths "a b", ".." and "*.txt"
// in a pathspec (measured on Linux, macOS and Windows). claustrum's own, not
// measured: the run of the two-ref diff, a range with an empty side, and the
// other paths in a pathspec.
var paneShapes = []paneShape{
	{sent: []string{"status", "--porcelain=v2", "--branch", "-z"},
		run: []string{"status", "--ignore-submodules=dirty", "--porcelain=v2", "--branch", "-z"}, noLocks: true, realIndex: true},
	{sent: []string{"rev-parse", "--show-toplevel"}},
	{sent: []string{"rev-parse", "--abbrev-ref", "HEAD"}},
	{sent: []string{"rev-parse", "--is-shallow-repository"}},
	{sent: []string{"rev-parse", "--verify", slotRef}},
	{sent: []string{"symbolic-ref", "--short", "HEAD"}},
	{sent: []string{"ls-files", "--others", "--exclude-standard", "--full-name", ":/"}, realIndex: true},
	{sent: []string{"show", "--no-textconv", "--no-ext-diff", slotRef, "--"},
		run: slices.Concat([]string{"show"}, paneDiffOpts, []string{"--no-textconv", "--no-ext-diff", slotRef, "--"})},
	{sent: []string{"merge-base", slotRef, slotRef}},
	{sent: []string{"rev-list", "--left-right", "--count", slotRev},
		run: []string{"rev-list", "--left-right", "--count", slotRev, "--"}},
	{sent: []string{"log", "--no-show-signature", paneLogFormat, slotRev},
		run: slices.Concat([]string{"log"}, paneDiffOpts, []string{"--no-show-signature", paneLogFormat, slotRev, "--"})},
	{sent: []string{"hash-object", "-t", "tree", "/dev/null"}},
	{sent: []string{"diff", slotBase}, run: paneDiffRun(true, slotBase, "--"), realIndex: true},
	{sent: []string{"diff", "--numstat", slotBase}, run: paneDiffRun(false, "--numstat", slotBase, "--"), realIndex: true},
	{sent: []string{"diff", "-z", "--no-color", "--name-status", "-M", slotBase},
		run: paneDiffRun(false, "-z", "--no-color", "--name-status", "-M", slotBase, "--"), realIndex: true},
	{sent: []string{"diff", "--inter-hunk-context=3", slotBase},
		run: paneDiffRun(true, "--inter-hunk-context=3", slotBase, "--"), realIndex: true},
	{sent: []string{"diff", "--src-prefix=a/", "--dst-prefix=b/", slotBase},
		run: paneDiffRun(true, "--src-prefix=a/", "--dst-prefix=b/", slotBase, "--"), realIndex: true},
	{sent: []string{"diff", "--name-status", "-M", slotBase},
		run: paneDiffRun(false, "--name-status", "-M", slotBase, "--"), realIndex: true},
	{sent: []string{"diff", "--name-status", "-M", slotBase, "--", slotExclude},
		run: paneDiffRun(false, "--name-status", "-M", slotBase, "--", slotExclude), realIndex: true},
	{sent: []string{"diff", "--name-status", "-M", slotRef, slotRef},
		run: slices.Concat([]string{"diff-tree", "-r"}, paneDiffOpts, []string{"--name-status", "-M", slotRef, slotRef, "--"})},
}

// paneLogFormat is the format of the log shape.
const paneLogFormat = "--format=%H%x00%h%x00%s%x00%an%x00%ae%x00%ct%x1e"

// paneRefMax is the longest ref in bytes. A ref of 4096 bytes runs, and one of
// 4097 bytes gets the shape refusal (measured on Linux, macOS and Windows).
const paneRefMax = 4096

// paneRefOK is the test of a slotRef. An empty ref and one with a leading "-"
// are refused (measured on Linux). So are one longer than paneRefMax and one with
// a TAB, LF, SOH or DEL byte (measured on Linux, and on macOS and Windows but
// SOH). A ref with NUL, 0x02, 0x1b or 0x1f gets it too (measured on Linux, macOS
// and Windows). claustrum's own, not measured: the other bytes below 0x20. A ref
// with a space or ".." inside passes to git (claustrum's own, not measured).
func paneRefOK(s string) bool {
	if s == "" || s[0] == '-' || len(s) > paneRefMax {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return false
		}
	}
	return true
}

// paneRangeOK reports whether v is <A>...<B> or <A>..<B>, each side a slotRef.
func paneRangeOK(v string) bool {
	a, b, ok := strings.Cut(v, "...")
	if !ok {
		a, b, ok = strings.Cut(v, "..")
	}
	return ok && paneRefOK(a) && paneRefOK(b)
}

// paneSlotOK reports whether v passes the test of slot. A slotBase with "..."
// gets the shape refusal (measured on Linux, macOS and Windows). The bound of
// paneRefOK applies to each side of a range, not to the whole value. A diff
// <A>..<B> of more than 4096 bytes runs when each side has 4096 bytes or less
// (c9125eb8, measured on Linux, macOS and Windows, a side of exactly 4096 bytes on
// macOS and Windows). The same rule for a range in slotRev, and the refusal of a
// side of more than 4096 bytes or with a control byte, are claustrum's own, not
// measured.
func paneSlotOK(slot, v string) bool {
	switch slot {
	case slotRef:
		return paneRefOK(v)
	case slotRev:
		if strings.Contains(v, "..") {
			return paneRangeOK(v)
		}
		return paneRefOK(v)
	case slotBase:
		a, b, ok := strings.Cut(v, "..")
		if !ok {
			return paneRefOK(v)
		}
		return !strings.Contains(v, "...") && paneRefOK(a) && paneRefOK(b) && !strings.Contains(b, "..")
	case slotExclude:
		if !paneRefOK(v) {
			return false
		}
		for _, p := range []string{":(top,exclude,literal)", ":(top,exclude)"} {
			if rest, ok := strings.CutPrefix(v, p); ok {
				return rest != ""
			}
		}
	}
	return false
}

// isPaneSlot reports whether w is a slot.
func isPaneSlot(w string) bool {
	return w == slotRef || w == slotRev || w == slotBase || w == slotExclude
}

// paneMatch reports whether args fits the sent argv of s.
func (s *paneShape) paneMatch(args []string) bool {
	if len(args) != len(s.sent) {
		return false
	}
	for i, w := range s.sent {
		if isPaneSlot(w) {
			if !paneSlotOK(w, args[i]) {
				return false
			}
		} else if args[i] != w {
			return false
		}
	}
	return true
}

// runFor is the run argv of s for the sent args, its slot values in order, and
// whether the run keeps the real index. A slotBase value <A>..<B> gives the run
// of paneTreeRun with A and B, and the index <copy>/no-index. c9125eb8 runs
// `diff --name-status -M main..topic` as `diff-tree -r ... main topic --` with
// that index (measured on Linux, macOS and Windows).
func (s *paneShape) runFor(args []string) (run, values []string, realIndex bool) {
	run, realIndex = s.run, s.realIndex
	if run == nil {
		run = s.sent
	}
	for i, w := range s.sent {
		if !isPaneSlot(w) {
			continue
		}
		if w == slotBase {
			if a, b, ok := strings.Cut(args[i], ".."); ok {
				values = append(values, a, b)
				run, realIndex = paneTreeRun(run), false
				continue
			}
		}
		values = append(values, args[i])
	}
	return run, values, realIndex
}

// paneTreeRun is run with diff-tree -r for diff-index and two slotRef for its
// slotBase.
func paneTreeRun(run []string) []string {
	var out []string
	for _, w := range run {
		switch w {
		case "diff-index":
			out = append(out, "diff-tree", "-r")
		case slotBase:
			out = append(out, slotRef, slotRef)
		default:
			out = append(out, w)
		}
	}
	return out
}

// paneHardenFirst and paneHardenLast are the `-c` items of the pane's git, in
// the order of the call logs (measured on Linux, macOS and Windows). The status
// shape has --no-optional-locks between the two parts (measured on Windows).
// core.hooksPath is hooksNullDevice.
var (
	paneHardenFirst = []string{
		"core.fsmonitor=",
		"core.hooksPath=" + hooksNullDevice,
		"core.longpaths=true",
		"protocol.allow=never",
		"protocol.ext.allow=never",
		"protocol.fd.allow=never",
		"protocol.file.allow=never",
		"protocol.git.allow=never",
		"protocol.ssh.allow=never",
		"protocol.http.allow=never",
		"protocol.https.allow=never",
		"credential.helper=",
		"core.askPass=",
		"fetch.bundleURI=",
		"core.alternateRefsCommand=",
		"alias.remote-https=",
		"alias.remote-http=",
		"alias.remote-ssh=",
		"alias.remote-denied_by_claude_ssh.command=",
		"help.autocorrect=0",
		"submodule.recurse=false",
		"fetch.recurseSubmodules=false",
		"push.recurseSubmodules=false",
		"gc.auto=0",
		"maintenance.auto=false",
	}
	paneHardenLast = []string{
		"core.quotepath=false",
		"diff.autoRefreshIndex=false",
		"log.showSignature=false",
		"status.submoduleSummary=false",
		"diff.ignoreSubmodules=dirty",
		"diff.submodule=short",
		"format.pretty=medium",
		"filter.lfs.clean=",
		"filter.lfs.smudge=",
		"filter.lfs.process=",
		"filter.lfs.required=false",
		"advice.graftFileDeprecated=false",
	}
)

// paneShapeOf is the shape of args, or nil for an argv that the pane may not run.
func paneShapeOf(args []string) *paneShape {
	for i := range paneShapes {
		if paneShapes[i].paneMatch(args) {
			return &paneShapes[i]
		}
	}
	return nil
}

// PaneShapeRefusal is the text of the shape refusal of a gitRead spawn with args,
// or "" for an argv of paneShapes. c9125eb8 answers it with -32602 and the reason
// other. N is the count of args (measured on Linux, macOS and Windows).
func PaneShapeRefusal(args []string) string {
	if paneShapeOf(args) != nil {
		return ""
	}
	return fmt.Sprintf("not one of the Changes pane's git commands (%d arguments)", len(args))
}

// paneArgv is the argv of the run of shape s for the sent args, after "git": the
// items of the config.worktree file (front), the `-c` items, then the run argv.
// Each slot of the run argv gets the value of the same slot of args.
func paneArgv(s *paneShape, args, front []string) []string {
	argv := slices.Clone(front)
	for _, c := range paneHardenFirst {
		argv = append(argv, "-c", c)
	}
	if s.noLocks {
		argv = append(argv, "--no-optional-locks")
	}
	for _, c := range paneHardenLast {
		argv = append(argv, "-c", c)
	}
	run, values, _ := s.runFor(args)
	for _, w := range run {
		if isPaneSlot(w) {
			w, values = values[0], values[1:]
		}
		argv = append(argv, w)
	}
	return argv
}

// PaneRun is the prepared run of the pane's git.
type PaneRun struct {
	// Args is the argv after "git".
	Args []string
	env  func(spawn []string) []string
	// release ends the hold of the copy that the run names.
	release func()
}

// Env is the environment of the pane's git, built from the environment that the
// spawn built for it.
func (r PaneRun) Env(spawn []string) []string { return r.env(spawn) }

// Release ends the hold of the copy that the run names. The spawn calls it when
// the wait on the child returns, as runHeld does for every other git child. A
// spawn whose child does not start calls it at once.
func (r PaneRun) Release() { r.release() }

// PaneGitRun takes the steps of the git methods before the pane's git in cwd:
// the excludes read, the trust check, the settings gate and the listing. args is
// an argv of paneShapes. A refusal of a step is the error, and the spawn answers
// it with -32603 and the reason other (claustrum's own, not measured, and
// measured on Linux and Windows for a copy that cannot be written). The spawn
// answers ErrNoSettingsCopy with -32008. A folder
// with no repository runs on: c9125eb8 answers success there, and git exits
// 128 (measured on Linux and Windows).
func PaneGitRun(d *Deps, cwd string, args []string) (PaneRun, error) {
	shape := paneShapeOf(args)
	if shape == nil {
		return PaneRun{}, errors.New(PaneShapeRefusal(args))
	}
	req := &gitCall{shadow: d.Shadow, offHold: d.OffRequests}
	req.stop = &gitStop{wide: d.OffRequests}
	req.use.stop = req.stop
	defer req.runAtReply()
	// The excludes read is the first call (measured on Linux).
	userExcludesFile()
	t := readGitDirTrust(cwd, true)
	if t.verdict == gitDirRefused {
		return PaneRun{}, errors.New(t.refusal)
	}
	if t.verdict == gitDirTrusted {
		if msg, _ := req.settingsGateRefusal(t, false, cwd); msg != "" {
			return PaneRun{}, errors.New(msg)
		}
	}
	// The listing in the cwd gives the hook pins (measured on Linux). A listing
	// that says "no repository" runs on with no pins (claustrum's own, not measured).
	c := hostileConfigRefusal(req.use, cwd, true)
	if c.refusal != "" {
		return PaneRun{}, errors.New(c.refusal)
	}
	// In the state fallback-git a repository gets ErrNoSettingsCopy after the
	// listing, and its git does not run (measured on Linux, and the frame on macOS
	// and Windows). A folder with no repository runs on (claustrum's own, not
	// measured).
	if t.verdict == gitDirTrusted && !req.shadow.readsRepoConfig() {
		return PaneRun{}, ErrNoSettingsCopy
	}
	if err := req.stop.halted(); err != nil {
		return PaneRun{}, err
	}
	u := req.use
	u.noRenew = true
	pin := u.pin(cwd)
	drop, err := holdRepoCopy(&exec.Cmd{Env: pin})
	if err != nil {
		return PaneRun{}, err
	}
	hooks := c.listing.hooks
	index := ""
	if _, _, real := shape.runFor(args); !real && u.shadow() {
		index = filepath.Join(u.repoDir, paneNoIndexName)
	}
	return PaneRun{
		Args:    paneArgv(shape, args, u.front(cwd)),
		env:     func(spawn []string) []string { return paneEnv(u, spawn, pin, hooks, index) },
		release: drop,
	}, nil
}

// ErrNoSettingsCopy is the refusal of a gitRead spawn in a repository in the
// state fallback-git. c9125eb8 answers it with -32008 and the reason other
// (measured on Linux, macOS and Windows).
var ErrNoSettingsCopy = errors.New("the helper cannot give git its own copy of a repository's settings on this host (this git does not support it, or the app switched it off)")

// paneNoIndexName is the name of the index of most shapes, in the copy of the
// repository. claustrum makes no file of that name (from the code).
const paneNoIndexName = "no-index"

// paneEnv is the environment of the pane's git. The spawn's own environment gets
// the cleaning of the git children (cleanGitChildEnv, withTraceOff). Then come the
// heavy profile, the folder variables pin, the count and the hook pins of hooks,
// GIT_OPTIONAL_LOCKS=0, an empty SSH_AUTH_SOCK and GIT_INDEX_FILE=index when index
// is not "". The order is that of the Linux call log (measured). The
// spawn's own GIT_OPTIONAL_LOCKS, SSH_AUTH_SOCK and GIT_INDEX_FILE go (claustrum's
// own, not measured).
func paneEnv(u gitUse, spawn, pin, hooks []string, index string) []string {
	base := slices.DeleteFunc(withTraceOff(cleanGitChildEnv(spawn, gitEnvHere), gitEnvHere), func(kv string) bool {
		name := envName(kv)
		if gitEnvHere.windows {
			name = strings.ToUpper(name)
		}
		return name == "GIT_OPTIONAL_LOCKS" || name == "SSH_AUTH_SOCK" || name == "GIT_INDEX_FILE"
	})
	env := append(hardenedEnvWith(base, u.profileEnv(true), pin, hooks), "GIT_OPTIONAL_LOCKS=0", "SSH_AUTH_SOCK=")
	if index != "" {
		env = append(env, "GIT_INDEX_FILE="+index)
	}
	return env
}
