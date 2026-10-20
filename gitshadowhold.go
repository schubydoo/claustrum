package main

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// The hold of a repository folder <S>/<32 hex> by one git child.
//
// On 5fd08069 a folder that a git call of a request is using is never removed
// (E2L cell 37, Linux VM). A folder goes between two git calls of its own
// request, and never during one (E3L cell 54, Linux VM). A folder that went and
// came back gets no second read of the repository config (E3L cell 54). The
// mechanism below is from the code: a child whose GIT_COMMON_DIR names a
// repository folder of this daemon holds that folder from just before its
// start until the wait for it ends. That is its exit, plus up to 2 s for most
// calls while its output pipes stay open. The build of a git call makes a
// folder that went or that a failed build left incomplete whole again
// (gitUse.renewCopy), except for a call right after its own config listing. A
// refusal is the error of that one call. The request keeps no refusal, and its
// next call is built and runs as any call.

// repoCopyGoneText is the refusal of a git child whose repository folder this
// daemon no longer has a record of. 5fd08069 answers it in E3L cell 54 and EL2
// cell 47 (Linux VM).
const repoCopyGoneText = "Claude could not prepare its own files on the remote machine: its copy of this repository's settings was cleared away before git started; try again"

// listingInForcePrefix starts the refusal of a refused config listing child.
// 5fd08069 answers that text with no other prefix (E3L cell 54 and EL2 cell 47,
// Linux VM, EW cell 46, Windows VM).
const listingInForcePrefix = "listing the configuration in force: "

// repoCopyGone is the error of a git child that does not start, because its
// repository folder went. listing marks a config listing child.
type repoCopyGone struct{ listing bool }

func (e repoCopyGone) Error() string {
	if e.listing {
		return listingInForcePrefix + repoCopyGoneText
	}
	return repoCopyGoneText
}

// shadowHosts maps the folder of each daemon in this process to its settings
// control (shadowHost.makeFolder). An entry stays after its folder went, so a
// child in an old folder of the daemon is refused (holdRepoCopy).
var shadowHosts sync.Map

// beforeRepoHold and afterRepoDrop are seams for the tests. The first runs
// right before a git child that names a repository folder looks for its
// record, and before the hold of a Windows staging step (holdCopy). The second
// runs right after such a child dropped its hold. Production never reassigns
// them.
var (
	beforeRepoHold = func(args []string) {}
	afterRepoDrop  = func(args []string) {}
)

// childCommonDir is the last GIT_COMMON_DIR value of env, or "".
func childCommonDir(env []string) string {
	dir := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "GIT_COMMON_DIR="); ok {
			dir = v
		}
	}
	return dir
}

// holdRepoCopy takes the hold of the repository folder that cmd names in its
// GIT_COMMON_DIR. A call that names no such folder of a daemon in this process
// holds nothing: the folder `.probe`, the read of the config, the off switch
// and the state fallback-git. drop ends the hold and stores the time of the
// use. A folder with no record gives repoCopyGone, and cmd must not start.
func holdRepoCopy(cmd *exec.Cmd) (drop func(), err error) {
	dir := childCommonDir(cmd.Env)
	name := filepath.Base(dir)
	if dir == "" || !isShadowRepoName(name) {
		return func() {}, nil
	}
	folder := filepath.Dir(dir)
	h, ok := shadowHosts.Load(folder)
	if !ok {
		return func() {}, nil
	}
	beforeRepoHold(cmd.Args)
	end, err := h.(*shadowHost).holdRepo(folder, name, gitSubcommand(cmd.Args) == "config")
	if err != nil {
		return nil, err
	}
	return func() {
		end()
		afterRepoDrop(cmd.Args)
	}, nil
}

// holdRepo takes the hold of the folder name in folder. folder must still be
// the folder of the daemon, and the daemon must have a record of name. The
// test reads the records in memory, not the disk. Three states refuse. The
// record went or is incomplete while folder is still the folder of the
// daemon. The settings control was shut down. Or folder is an old folder of
// the daemon, which made a new one. In the first state, after a build of
// renewRepo that failed, the error of that build stands in place of
// `repoCopyGoneText`.
func (h *shadowHost) holdRepo(folder, name string, listing bool) (func(), error) {
	h.repoMu.Lock()
	defer h.repoMu.Unlock()
	h.mu.Lock()
	ours := !h.closed && h.dir == folder
	h.mu.Unlock()
	if !ours {
		return nil, repoCopyGone{listing: listing}
	}
	if st := h.repos[name]; st == nil || st.incomplete {
		if err := h.renewFailed[name]; err != nil {
			return nil, err
		}
		return nil, repoCopyGone{listing: listing}
	}
	if h.repoBusy == nil {
		h.repoBusy = map[string]int{}
	}
	h.repoBusy[name]++
	return func() {
		h.repoMu.Lock()
		defer h.repoMu.Unlock()
		if h.repoBusy[name]--; h.repoBusy[name] <= 0 {
			delete(h.repoBusy, name)
		}
		h.useCount++
		if st := h.repos[name]; st != nil {
			st.used = h.useCount
		}
	}, nil
}

// runHeld runs a git child by run, the start and the wait of cmd, under the
// hold of its repository folder. A refused child starts no process, and its
// error is the error of the call, as for a failed start.
func runHeld(cmd *exec.Cmd, run func() error) error {
	drop, err := holdRepoCopy(cmd)
	if err != nil {
		return err
	}
	defer drop()
	return run()
}

// renewRepo makes the folder of the repository with the git directory gitDir
// again in dir when the daemon has no record of it, or an incomplete one.
// read is the read of the config that the request accepted, so the config is
// not read again. The limit test of the folders runs as in syncRepo. A folder
// of the daemon that is no longer dir makes nothing: the start of the child
// refuses then. A failure is kept in renewFailed until the next build of the
// folder, so the start of the child answers it (holdRepo).
func (h *shadowHost) renewRepo(dir, gitDir string, read *repoVerdict) error {
	h.repoMu.Lock()
	defer h.repoMu.Unlock()
	name := shadowRepoName(gitDir)
	if st := h.repos[name]; st != nil && !st.incomplete {
		return nil
	}
	h.mu.Lock()
	ours := !h.closed && h.dir == dir
	h.mu.Unlock()
	if !ours {
		return nil
	}
	if err := h.syncRepoFolder(dir, name, gitDir, read); err != nil {
		if h.renewFailed == nil {
			h.renewFailed = map[string]error{}
		}
		h.renewFailed[name] = shadowRepoRefusal{text: shadowRepoFailureText(err)}
		return err
	}
	h.useCount++
	h.repos[name].used = h.useCount
	return nil
}

// renewCopy is the step at the build of a git call that names the copy: a
// folder that went is made again (renewRepo). Each config listing and each
// call with no listing right before it runs it, also after a call that was
// refused. A call right after its listing skips it (noRenew). A failure
// refuses the start of the call with its text (holdRepo). The text of a full
// limit is the text of `repoLimitError`.
func (u gitUse) renewCopy() {
	if u.host == nil || u.read == nil || u.noRenew {
		return
	}
	_ = u.host.renewRepo(u.folder, u.gitDir, u.read)
}

// shadowRepoRefusal is the error of a folder of a repository that the build of
// a git call failed to make again. text is the reply text.
type shadowRepoRefusal struct{ text string }

func (e shadowRepoRefusal) Error() string { return e.text }

// holdCopy takes the hold of the repository folder of u for a step that is no
// git child: the staging folder of a Windows create, until it goes. A refusal
// is the error of the add. A request with no copy holds nothing. args is the
// argv of the add, for the seam beforeRepoHold.
func (u gitUse) holdCopy(args []string) (func(), error) {
	if u.host == nil || u.repoDir == "" {
		return func() {}, nil
	}
	beforeRepoHold(args)
	return u.host.holdRepo(u.folder, filepath.Base(u.repoDir), false)
}

// copyGone reports whether err is the refusal of a git call whose repository
// folder went (repoCopyGone), or whose build failed to make it again
// (shadowRepoRefusal). Each method answers it as the text of that call, with
// no prefix of its own (from the code, not measured).
func copyGone(err error) bool {
	var gone repoCopyGone
	var renew shadowRepoRefusal
	return errors.As(err, &gone) || errors.As(err, &renew)
}
