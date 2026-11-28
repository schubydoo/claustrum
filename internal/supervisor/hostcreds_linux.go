//go:build linux

package supervisor

import (
	"encoding/json"
	"os"
	"os/exec"

	"github.com/schubydoo/claustrum/internal/childrec"
	"github.com/schubydoo/claustrum/internal/sysproc"
)

// The pipe of a hostCreds spawn (13047a51, measured on Linux). The child gets
// CLAUDE_CODE_HOST_CREDS_FILE=/proc/self/fd/99, and fd 99 is the read end of a pipe.
// It reads the env of the hostCreds member there as one JSON object, and the pipe then
// ends. The names of that env do not reach the environment of the child (measured).
const (
	hostCredsFD      = 99
	hostCredsFileVar = "CLAUDE_CODE_HOST_CREDS_FILE"
	hostCredsFile    = "/proc/self/fd/99"
)

// openHostCreds makes the pipe of a spawn with the hostCreds env, or gives two nil
// files for a spawn with none.
func openHostCreds(env map[string]string) (r, w *os.File, err error) {
	if len(env) == 0 {
		return nil, nil, nil
	}
	return os.Pipe()
}

// hostCredsEnv adds the file entry of the pipe to env when r is a pipe. Its place at
// the end of the env before the trampoline adds its markers is claustrum's own, not
// measured. So is the replace of an entry of the same name.
func hostCredsEnv(env []string, r *os.File) []string {
	if r == nil {
		return env
	}
	return sysproc.ReplaceOrAppendEnv(env, hostCredsFileVar, hostCredsFile)
}

// placeHostCreds puts the read end r at fd 99 of the child of cmd. The entries of
// ExtraFiles before it stay as they are, and a nil entry is a closed fd in the child.
func placeHostCreds(cmd *exec.Cmd, r *os.File) {
	if r == nil {
		return
	}
	i := hostCredsFD - 3
	for len(cmd.ExtraFiles) <= i {
		cmd.ExtraFiles = append(cmd.ExtraFiles, nil)
	}
	cmd.ExtraFiles[i] = r
}

// credsMessage is what the child reads at fd 99. The member order and a null
// expiresAt are measured on Linux. Never is left nil, so it is always null: the row
// shows no other value. pid and procStart are numbers in the row, and their values
// are claustrum's own, not measured: the pid of the child and its start in clock
// ticks, as in CLAUDE_SSH_CHILD.
type credsMessage struct {
	Names map[string]string `json:"env"`
	Never any               `json:"expiresAt"`
	Child int               `json:"pid"`
	Ticks int64             `json:"procStart"`
}

// sendHostCreds writes the payload of the child pid to w and closes w, so the child
// reads to the end. The write runs in its own goroutine, because a payload past the
// pipe buffer waits for the read of the child. A child that ends without a read
// closes the read end, and the write fails. The bytes of the JSON text (no newline,
// the escapes of json.Marshal) are claustrum's own, not measured.
func sendHostCreds(w *os.File, env map[string]string, pid int) {
	if w == nil {
		return
	}
	b, err := json.Marshal(credsMessage{Names: env, Child: pid, Ticks: childrec.ProcStartTicks(pid)})
	go func() {
		if err == nil {
			_, _ = w.Write(b)
		}
		_ = w.Close()
	}()
}
