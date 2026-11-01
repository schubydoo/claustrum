package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// The ssh door of the checkout of git.worktree_create, in the state shadow.
// The read-tree call gets GIT_SSH_COMMAND set to sshDoorScript, so that git
// and git-lfs start ssh through it. The door refuses a Git LFS request whose
// host or path has a space, a special character or an unexpected word, and
// starts no ssh then. Every other request goes to the person's own ssh
// command (`be1c1ca5`, measured on Linux and macOS).
//
// The person's ssh command comes from the daemon's GIT_SSH_COMMAND, from
// `core.sshCommand` of the user's own config, or from the daemon's GIT_SSH.
// The config read names the settings copy (GIT_COMMON_DIR of the pin), and
// the copy carries no `core.sshCommand` and no `ssh.variant`. So a value of
// the repository's own config never reaches the person's ssh slot. With only
// a repository value `be1c1ca5` runs plain ssh, and the program of the
// repository never runs (measured on Linux).

// sshDoorRegexp is the pattern of the config read of the door.
const sshDoorRegexp = `^(core\.sshcommand|ssh\.variant)$`

// sshDoorRefusedText follows "git worktree add failed (checkout): " when the
// mark of the door is in the stderr of the checkout (measured on Linux and
// macOS, 194 bytes).
const sshDoorRefusedText = "Couldn't download this repository's Git LFS files. Its Git LFS address has a space or special character, so Claude didn't start ssh. Fix lfs.url in .lfsconfig, or the remote URL, then try again."

// sshDoorScript is the value of GIT_SSH_COMMAND in the checkout, one line. git
// runs it as `sh -c '<value> "$@"'`. It is claustrum's own shell code. A request is a
// Git LFS request when one of its words starts with `git-lfs-`. Such a request
// must be the options, one host word and one command word, as git-lfs sends
// it. The options are `-oControlMaster=<letters>`, `-oControlPath=<path>`
// and `-p <port>`. With GIT_SSH_VARIANT putty or tortoiseplink, `-P <port>` and
// `-batch` pass too (not measured). The command word is `git-lfs-authenticate`
// or `git-lfs-transfer`, one path and `download` or `upload`, with one space
// between them. A host or a path that starts with a dash is refused. Before it
// runs the person's ssh, the door removes its three CLAUDE_SSH_ names from the
// environment (measured on Linux).
const sshDoorScript = "claude_ssh_door() { " +
	"for claude_ssh_word do " +
	"case $claude_ssh_word in git-lfs-*) " +
	"if ! ( " +
	"LC_ALL=C; " +
	"while [ $# -gt 2 ]; do " +
	"case $1 in " +
	"-oControlMaster=*) case ${1#-oControlMaster=} in ''|*[!A-Za-z]*) exit 1 ;; esac; shift; continue ;; " +
	"-oControlPath=*) case ${1#-oControlPath=} in ''|-*|*[!A-Za-z0-9._/~+-]*) exit 1 ;; esac; shift; continue ;; " +
	"-p) ;; " +
	"-P) case $GIT_SSH_VARIANT in putty|tortoiseplink) ;; *) exit 1 ;; esac ;; " +
	"-batch) case $GIT_SSH_VARIANT in putty|tortoiseplink) ;; *) exit 1 ;; esac; shift; continue ;; " +
	"*) exit 1 ;; " +
	"esac; " +
	"case $2 in ''|*[!0-9]*) exit 1 ;; esac; " +
	"shift 2; " +
	"done; " +
	"[ $# -eq 2 ] || exit 1; " +
	"case $1 in ''|-*|*[!A-Za-z0-9._@-]*) exit 1 ;; esac; " +
	"case $2 in " +
	"'git-lfs-authenticate '*) rest=${2#\"git-lfs-authenticate \"} ;; " +
	"'git-lfs-transfer '*) rest=${2#\"git-lfs-transfer \"} ;; " +
	"*) exit 1 ;; " +
	"esac; " +
	"path=${rest% *}; " +
	"op=${rest##* }; " +
	"[ \"$path $op\" = \"$rest\" ] || exit 1; " +
	"case $op in download|upload) ;; *) exit 1 ;; esac; " +
	"case $path in ''|-*|*[!A-Za-z0-9._/~+-]*) exit 1 ;; esac " +
	"); then " +
	"printf '%s %s\\n' '" + sshDoorRefusalLine + "' \"$CLAUDE_SSH_DOOR_MARK\" >&2; " +
	"exit 1; " +
	"fi; " +
	"break ;; " +
	"esac; " +
	"done; " +
	"claude_ssh_cmd=$CLAUDE_SSH_PERSONS_SSH; " +
	"unset CLAUDE_SSH_PERSONS_SSH CLAUDE_SSH_PERSONS_ENV CLAUDE_SSH_DOOR_MARK; " +
	"eval \"$claude_ssh_cmd\"' \"$@\"'; " +
	"}; " +
	"claude_ssh_door"

// sshDoorRefusalLine starts the stderr line of a refusal of the door. The
// mark follows it after one space (measured on Linux and macOS).
const sshDoorRefusalLine = "Claude did not start ssh: the Git LFS request has a space, a special character or an unexpected word in its host or path."

// sshDoorMark is the 16 hex digits of CLAUDE_SSH_DOOR_MARK, one value for the
// life of the daemon process (claustrum's own). A refusal of the door ends with it.
var sshDoorMark = sync.OnceValue(func() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
})

// sshDoorSettings is the answer of the config read of the door.
type sshDoorSettings struct {
	command, variant       string
	hasCommand, hasVariant bool
}

// readSSHDoorSettings runs `git --git-dir=<gitDir> config -z --get-regexp
// ^(core\.sshcommand|ssh\.variant)$` in leaf with env, the env of the
// checkout. Exit 1 means no match. Another failure gives the refusal text
// (measured on Linux with exit 2 and exit 128). The text of a call whose
// repository folder went is its own (from the code). A call that the time
// limit stopped gives that stop to the caller (gitStop.halted). The last value
// of each key counts, as in git (not measured).
func readSSHDoorSettings(u gitUse, leaf, gitDir string, env []string) (sshDoorSettings, string) {
	cmd := exec.Command("git", "--git-dir="+gitDir, "config", "-z", "--get-regexp", sshDoorRegexp)
	cmd.Dir = leaf
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout = &out
	err := gitRun(u.stop.context(), cmd)
	var s sshDoorSettings
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return s, ""
	case copyGone(err):
		return s, err.Error()
	default:
		return s, "Claude could not read your own git config on the remote machine (" + err.Error() +
			"). Run git config --list there to see the problem."
	}
	for _, rec := range strings.Split(out.String(), "\x00") {
		key, value, _ := strings.Cut(rec, "\n")
		switch strings.ToLower(key) {
		case "core.sshcommand":
			s.command, s.hasCommand = value, true
		case "ssh.variant":
			s.variant, s.hasVariant = value, true
		}
	}
	return s, ""
}

// sshDoorEnv is env with the five entries of the door at its end, in the
// measured order: GIT_SSH_COMMAND, CLAUDE_SSH_PERSONS_SSH,
// CLAUDE_SSH_PERSONS_ENV, CLAUDE_SSH_DOOR_MARK and GIT_SSH_VARIANT, right after
// LANGUAGE=C (measured on Linux). The daemon's GIT_SSH_COMMAND goes, and so
// does its GIT_SSH_VARIANT when the door sets one. CLAUDE_SSH_PERSONS_ENV is
// 1 when the person's command is the daemon's GIT_SSH_COMMAND, else empty
// (measured on Linux and Windows). With `ssh.variant=simple` there is no
// GIT_SSH_VARIANT (measured on Linux). Other values of `ssh.variant` act the
// same (claustrum's own).
func sshDoorEnv(env []string, s sshDoorSettings) []string {
	persons, variant, fromDaemon := personsSSH(env, s)
	personsEnv := ""
	if fromDaemon {
		personsEnv = "1"
	}
	drop := []string{"GIT_SSH_COMMAND", "CLAUDE_SSH_PERSONS_SSH", "CLAUDE_SSH_PERSONS_ENV", "CLAUDE_SSH_DOOR_MARK"}
	setVariant := !s.hasVariant && variant != ""
	if setVariant {
		drop = append(drop, "GIT_SSH_VARIANT")
	}
	out := slices.DeleteFunc(slices.Clone(env), func(kv string) bool {
		name := envName(kv)
		if gitEnvHere.windows {
			name = strings.ToUpper(name)
		}
		return slices.Contains(drop, name)
	})
	out = append(out,
		"GIT_SSH_COMMAND="+sshDoorScript,
		"CLAUDE_SSH_PERSONS_SSH="+persons,
		"CLAUDE_SSH_PERSONS_ENV="+personsEnv,
		"CLAUDE_SSH_DOOR_MARK="+sshDoorMark())
	if setVariant {
		out = append(out, "GIT_SSH_VARIANT="+variant)
	}
	return out
}

// personsSSH is the person's ssh command as shell text, the variant that git
// gives its name, or "", and whether it is the daemon's GIT_SSH_COMMAND. The
// sources are the daemon's GIT_SSH_COMMAND, `core.sshCommand` of the read,
// the daemon's GIT_SSH, and then `ssh`. Each one alone is measured on Linux,
// and GIT_SSH and `ssh` on Windows too. Their order follows git (not
// measured). GIT_SSH and `ssh` are put in single quotes, so the default is
// `'ssh'` (measured on Linux and Windows). An empty value counts as not set
// (not measured). GIT_SSH comes from the cleaned env of the git child, which
// keeps a bare program name such as `plink.exe` and drops a relative path.
func personsSSH(env []string, s sshDoorSettings) (command, variant string, fromDaemon bool) {
	if v := doorEnvValue(env, "GIT_SSH_COMMAND"); v != "" {
		return v, sshVariantOf(firstShellWord(v)), true
	}
	if s.hasCommand && s.command != "" {
		return s.command, sshVariantOf(firstShellWord(s.command)), false
	}
	if v := doorEnvValue(env, "GIT_SSH"); v != "" {
		return shellQuoted(v), sshVariantOf(v), false
	}
	return shellQuoted("ssh"), "ssh", false
}

// shellQuoted is v in single quotes for sh.
func shellQuoted(v string) string {
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}

// doorEnvValue is the value of the last entry named name in env. On Windows the
// name matches in any letter case.
func doorEnvValue(env []string, name string) string {
	value := ""
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if k == name || (gitEnvHere.windows && strings.EqualFold(k, name)) {
			value = v
		}
	}
	return value
}

// firstShellWord is the first word of a shell command, without the quotes
// around it.
func firstShellWord(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	if q := command[0]; q == '\'' || q == '"' {
		if end := strings.IndexByte(command[1:], q); end >= 0 {
			return command[1 : end+1]
		}
	}
	word, _, _ := strings.Cut(command, " ")
	word, _, _ = strings.Cut(word, "\t")
	return word
}

// sshVariantOf is the variant that git gives the program name, by the base
// name in any letter case: `ssh` for ssh, `putty` for plink (measured on
// Linux and Windows), and `tortoiseplink` for tortoiseplink (not measured).
// Another name gives "", and the door sets no GIT_SSH_VARIANT then (measured
// on Linux and Windows).
func sshVariantOf(program string) string {
	switch strings.ToLower(filepath.Base(program)) {
	case "ssh", "ssh.exe":
		return "ssh"
	case "plink", "plink.exe":
		return "putty"
	case "tortoiseplink", "tortoiseplink.exe":
		return "tortoiseplink"
	}
	return ""
}
