package main

// Result structs with fields in the exact order the real binary emits them, so
// serialized frames are byte-compatible (Go marshals struct fields in declaration
// order; a map would sort keys alphabetically and diverge).

type pongResult struct {
	Pong bool `json:"pong"`
}

type versionResult struct {
	Version  string `json:"version"`
	Platform string `json:"platform"`
	Arch     string `json:"arch"`
}

type capabilitiesResult struct {
	Version string   `json:"version"`
	Methods []string `json:"methods"`
	// Features advertises optional protocol extensions the client may rely on.
	// Added by the reference daemon in 7c2f88d alongside process.killAndWait and
	// the stdin-offset idempotency contract; the sole entry is
	// "process.stdin.offset". Always present (never omitempty) — the reference
	// emits the array unconditionally.
	Features []string `json:"features"`
}

type statResult struct {
	Exists bool   `json:"exists"`
	IsDir  bool   `json:"isDir"`
	Size   int64  `json:"size"`
	Mode   string `json:"mode"`
}

type listEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"isDir"`
}

type listResult struct {
	Entries []listEntry `json:"entries"`
}

type readResult struct {
	Content string `json:"content"`
	Exists  bool   `json:"exists"`
}

type validateResult struct {
	Valid bool   `json:"valid"`
	IsDir bool   `json:"isDir"`
	Error string `json:"error,omitempty"`
}

type extractResult struct {
	Success   bool   `json:"success"`
	FileCount int    `json:"fileCount"`
	Error     string `json:"error,omitempty"`
}

type successResult struct {
	Success bool `json:"success"`
}

// spawnResult is process.spawn's reply. It is deliberately distinct from the
// successResult shared by process.stdin/process.kill: the CT-1 opt-in
// ("wantPid":true) adds pid + startTime here only, so those fields can never
// leak into a stdin/kill reply. Both are omitempty, so without the opt-in the
// frame is byte-identical to the old {"success":true}. startTime is an opaque
// daemon token (epoch seconds), not an OS-comparable start time — see
// managedProc.startTime.
type spawnResult struct {
	Success   bool    `json:"success"`
	Pid       int     `json:"pid,omitempty"`
	StartTime float64 `json:"startTime,omitempty"`
}

type gitInfoResult struct {
	IsRepo bool   `json:"isRepo"`
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	// Root is the absolute repo top-level (git rev-parse --show-toplevel) — the
	// repo root even when path points at a subdirectory. Added by the reference
	// daemon in 7cbfa471 (the 8de85faa baseline omitted it).
	Root string `json:"root"`
	// RepoSlug and DefaultBranch were added by the reference daemon in 7c2f88d.
	// Both are always present (never omitempty), empty when undeterminable.
	// RepoSlug is the "owner/repo" parsed from remote.origin.url — populated ONLY
	// when the path after the host is exactly two segments (a GitLab subgroup like
	// group/sub/proj yields ""), .git and userinfo stripped (see parseRepoSlug).
	// DefaultBranch is what refs/remotes/origin/HEAD points to (empty when unset).
	RepoSlug      string `json:"repoSlug"`
	DefaultBranch string `json:"defaultBranch"`
}

type gitStatusResult struct {
	IsRepo  bool     `json:"isRepo"`
	Clean   bool     `json:"clean"`
	Changes []string `json:"changes,omitempty"`
}

type branchesResult struct {
	IsRepo   bool     `json:"isRepo"`
	Branches []string `json:"branches"`
}

// Field order is the reference's: success, path, error, errorCode, sourceBranch.
// No input currently populates sourceBranch together with error/errorCode, so
// the difference is unreachable on the wire today — corrected anyway, because
// ordered structs ARE the contract and a future input that populates both would
// otherwise diverge silently.
// worktreeRemoveResult is git.worktree_remove's reply. The reference declares an
// `error` field alongside `success`, and the lenient cases still answer a bare
// {"success":true} — removing a nonexistent worktree, or naming a branch that
// does not exist.
//
// TWO inputs DO populate `error` now; this comment used to say none did:
//
//	git refused AND the daemon's own cleanup also failed  (matches the reference)
//	claustrum's gitTimeout fired                          (claustrum-only)
//
// The second is a frame the reference cannot emit — it runs git with no deadline.
// Both are confined to pathological paths; every reference-reachable reply is
// still the bare {"success":true}, which is what the committed goldens pin.
type worktreeRemoveResult struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

type worktreeResult struct {
	Success      bool   `json:"success"`
	Path         string `json:"path,omitempty"`
	Error        string `json:"error,omitempty"`
	ErrorCode    string `json:"errorCode,omitempty"`
	SourceBranch string `json:"sourceBranch,omitempty"`
}

type reattachResult struct {
	Found    bool   `json:"found"`
	Running  bool   `json:"running"`
	FirstSeq uint64 `json:"firstSeq"`
	LastSeq  uint64 `json:"lastSeq"`
	// StdinApplied is the cumulative count of stdin bytes applied to the process,
	// added by the reference daemon in 7c2f88d. A reconnecting client uses it to
	// resume stdin from the right offset (see the process.stdin.offset contract).
	// Always present (never omitempty); 0 when no process was found.
	StdinApplied uint64 `json:"stdinApplied"`
	// Pid/StartTime are the CT-1 opt-in fields ("wantPid":true), appended last so
	// the default reply stays byte-identical. omitempty drops them when the opt-in
	// is absent (or no process was found). startTime is an opaque daemon token
	// (epoch seconds), not an OS-comparable start time — see managedProc.startTime.
	Pid       int     `json:"pid,omitempty"`
	StartTime float64 `json:"startTime,omitempty"`
}

// stdinResult is process.stdin's reply. Before 7c2f88d it was the bare
// {"success":true}; the reference now always reports the cumulative applied byte
// count, and flags a wholly-duplicate write (offset already covered) so an
// idempotent client can resume stdin across reconnects. Applied is never
// omitempty (the reference emits it unconditionally, even at 0); Duplicate is
// dropped when false.
type stdinResult struct {
	Success   bool   `json:"success"`
	Applied   uint64 `json:"applied"`
	Duplicate bool   `json:"duplicate,omitempty"`
}

// killAndWaitResult is process.killAndWait's reply. Found reports whether the id
// was known; Died whether the process is now gone. AlreadyExited flags a process
// that had already exited before the signal (no kill needed); Escalated flags one
// that ignored the graceful signal and had to be SIGKILL'd after the grace
// window. Both bools are omitempty, so a plain running-process kill is
// {"found":true,"died":true} and an unknown id is {"found":false,"died":false}.
type killAndWaitResult struct {
	Found         bool `json:"found"`
	Died          bool `json:"died"`
	AlreadyExited bool `json:"alreadyExited,omitempty"`
	Escalated     bool `json:"escalated,omitempty"`
}

// notRepoResult is the git.info body for non-repository paths. Since 7c2f88d the
// reference emits {"isRepo":false,"repoSlug":"","defaultBranch":""} — the two new
// fields are always present (empty for a non-repo) just as in gitInfoResult.
type notRepoResult struct {
	IsRepo        bool   `json:"isRepo"`
	RepoSlug      string `json:"repoSlug"`
	DefaultBranch string `json:"defaultBranch"`
}
