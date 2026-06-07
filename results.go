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

type gitInfoResult struct {
	IsRepo bool   `json:"isRepo"`
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
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
}

// notRepo is the minimal {"isRepo":false} body for non-repository paths.
type notRepoResult struct {
	IsRepo bool `json:"isRepo"`
}
