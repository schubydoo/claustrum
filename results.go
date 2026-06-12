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
// frame is byte-identical to the old {"success":true}. startTime is epoch
// seconds (see managedProc.startTime).
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

type worktreeResult struct {
	Success      bool   `json:"success"`
	Path         string `json:"path,omitempty"`
	SourceBranch string `json:"sourceBranch,omitempty"`
	Error        string `json:"error,omitempty"`
	ErrorCode    string `json:"errorCode,omitempty"`
}

type reattachResult struct {
	Found    bool `json:"found"`
	Running  bool `json:"running"`
	FirstSeq int  `json:"firstSeq"`
	LastSeq  int  `json:"lastSeq"`
	// Pid/StartTime are the CT-1 opt-in fields ("wantPid":true), appended after
	// the original four so the default reply stays byte-identical. omitempty drops
	// them when the opt-in is absent (or no process was found). startTime is epoch
	// seconds (see managedProc.startTime).
	Pid       int     `json:"pid,omitempty"`
	StartTime float64 `json:"startTime,omitempty"`
}

// notRepo is the minimal {"isRepo":false} body for non-repository paths.
type notRepoResult struct {
	IsRepo bool `json:"isRepo"`
}
