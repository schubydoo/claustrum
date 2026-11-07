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

// paneShapes are the argv that c9125eb8 runs with "gitRead":true. The list is
// the union of the measured rows: each one ran on Linux, macOS or Windows. Every
// other argv gets the shape refusal. That refusal is measured for the argv that
// the rows tried. For any other argv it is claustrum's own, not measured, and
// an argv differs from a row if a single byte differs. The run argv of each
// shape is that of the call logs (measured on Linux, and the status row on Windows).
var paneShapes = []paneShape{
	{sent: []string{"status", "--porcelain=v2", "--branch", "-z"},
		run: []string{"status", "--ignore-submodules=dirty", "--porcelain=v2", "--branch", "-z"}, noLocks: true, realIndex: true},
	{sent: []string{"rev-parse", "--show-toplevel"}},
	{sent: []string{"rev-parse", "--abbrev-ref", "HEAD"}},
	{sent: []string{"rev-parse", "--is-shallow-repository"}},
	{sent: []string{"rev-parse", "--verify", "HEAD"}},
	{sent: []string{"symbolic-ref", "--short", "HEAD"}},
	{sent: []string{"ls-files", "--others", "--exclude-standard", "--full-name", ":/"}, realIndex: true},
	{sent: []string{"show", "--no-textconv", "--no-ext-diff", "HEAD:README", "--"},
		run: []string{"show", "--ignore-submodules=dirty", "--submodule=short", "--no-ext-diff", "--no-textconv",
			"--no-textconv", "--no-ext-diff", "HEAD:README", "--"}},
	{sent: []string{"merge-base", "HEAD", "HEAD"}},
	{sent: []string{"rev-list", "--left-right", "--count", "HEAD...HEAD"},
		run: []string{"rev-list", "--left-right", "--count", "HEAD...HEAD", "--"}},
	{sent: []string{"diff", "--name-status", "-M", "HEAD"},
		run: []string{"diff-index", "--ignore-submodules=dirty", "--submodule=short", "--no-ext-diff", "--no-textconv",
			"--name-status", "-M", "HEAD", "--"}, realIndex: true},
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
		if slices.Equal(paneShapes[i].sent, args) {
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

// paneArgv is the argv of the run of shape s, after "git": the items of the
// config.worktree file (front), the `-c` items, then the run argv.
func paneArgv(s *paneShape, front []string) []string {
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
	run := s.run
	if run == nil {
		run = s.sent
	}
	return append(argv, run...)
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

// Release ends the hold of the copy that the run names. The spawn calls it after
// the start of the child. The hold does not last for the life of the child
// (claustrum's own, not measured).
func (r PaneRun) Release() { r.release() }

// PaneGitRun takes the steps of the git methods before the pane's git in cwd:
// the excludes read, the trust check, the settings gate and the listing. args is
// an argv of paneShapes. A refusal of a step is the error, and the spawn answers
// it with -32603 and the reason other (claustrum's own, not measured). A folder
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
	if !shape.realIndex && u.shadow() {
		index = filepath.Join(u.repoDir, paneNoIndexName)
	}
	return PaneRun{
		Args:    paneArgv(shape, u.front(cwd)),
		env:     func(spawn []string) []string { return paneEnv(u, spawn, pin, hooks, index) },
		release: drop,
	}, nil
}

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
