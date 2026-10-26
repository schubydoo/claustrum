package main

import (
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// The git calls that run outside any repository: the `git version` run, the
// `--attr-source` probe, the version gate of the include scan, the excludes read
// and the read of config text. The rules are claustrum's own, not
// measured. Every one of them starts from the filtered
// environment of gitChildEnv and runs in gitVersionDir.

// gitVersionDropped are the names that the `git version` run removes. The match is
// exact on every system, so a lower-case spelling stays on Windows (not measured).
var gitVersionDropped = []string{
	"GIT_DIR", "GIT_CONFIG", "GIT_NAMESPACE", "GIT_WORK_TREE", "GIT_COMMON_DIR",
	"GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_CONFIG_COUNT", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_PARAMETERS",
	"GIT_CEILING_DIRECTORIES", "GIT_DISCOVERY_ACROSS_FILESYSTEM",
}

// gitVersionEnv is the environment of the `git version` run. A nil env is the
// filtered environment of the daemon (gitChildEnv). A given list stays as it is and
// is not filtered. Then the names of gitVersionDropped go, and every name that
// starts with GIT_CONFIG_KEY_ or GIT_CONFIG_VALUE_. GIT_CONFIG_NOSYSTEM stays.
// The result is never nil.
func gitVersionEnv(env []string) []string {
	if env == nil {
		env = gitChildEnv()
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name := envName(kv)
		if slices.Contains(gitVersionDropped, name) ||
			strings.HasPrefix(name, configKeyPrefix) || strings.HasPrefix(name, configValPrefix) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// windowsCallDir is the working folder of the calls outside a repository on
// Windows: SystemRoot if it is an absolute Windows path, else the folder that holds
// the executable of the daemon, else "", which is the working folder of the
// daemon. exeErr is the error of the lookup of the executable. The rule runs on
// every system so that a test can reach it.
func windowsCallDir(systemRoot, exe string, exeErr error) string {
	if absoluteFor(systemRoot, true) {
		return systemRoot
	}
	if exeErr == nil && exe != "" {
		return filepath.Dir(exe)
	}
	return ""
}

// exitedWithCode reports whether err is the exit of a process with a code. A
// process that a signal ended, and a process that did not start, give false.
func exitedWithCode(err error) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() >= 0
}

// gitOutsideCmd is the command for a call outside a repository: the filtered
// environment, then the entries add, in gitVersionDir.
func gitOutsideCmd(add []string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Dir = gitVersionDir
	cmd.Env = append(gitChildEnv(), add...)
	return cmd
}

// gitVersionPlain runs `git version` as the `git version` run does: the folder of
// gitVersionDir and the environment of gitVersionEnv(nil).
func gitVersionPlain() *exec.Cmd {
	cmd := exec.Command("git", "version")
	cmd.Dir = gitVersionDir
	cmd.Env = gitVersionEnv(nil)
	return cmd
}
