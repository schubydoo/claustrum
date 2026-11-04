package main

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/schubydoo/claustrum/internal/sessiondirs"
)

// The session folders of process.spawn (internal/sessiondirs). A spawn of a Claude
// Code shape records its folders, and a spawn of a bare command name passes over a
// git in such a folder.

// bareGitName reports whether command is git by its bare name. On Windows `git.exe`
// and any letter case count too (claustrum's own, not measured).
func bareGitName(command string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(command, "git") || strings.EqualFold(command, "git.exe")
	}
	return command == "git"
}

// bareCommandStart applies the session folders to cmd, a spawn of command with no
// launcher. A bare git passes over a git in a session folder, and fails to start
// when PATH holds no other one (measured on Linux, macOS and Windows). git is
// found on the PATH of the daemon. Its env loses the daemon's
// sessiondirs.EditorNames, and the session folders are cut from it, the spawn env
// PATH too (sessiondirs.CutPath, measured on Linux and macOS). Another bare name
// that PATH found in a session folder still runs, with the warning line (measured
// on Linux and macOS). A command with a path keeps its env (measured on Linux).
func bareCommandStart(cmd *exec.Cmd, command string, env map[string]string) {
	if command == "" || filepath.Base(command) != command {
		return
	}
	if !bareGitName(command) {
		if cmd.Err == nil {
			sessiondirs.WarnBareCommand(command, cmd.Path)
		}
		return
	}
	cmd.Env = sessiondirs.TraceOff(sessiondirs.CutPath(sessiondirs.WithoutEditors(cmd.Env, env)))
	sessiondirs.PickGit(cmd)
}

// sessionFoldersOf is the list of session folders of a spawn of command with args
// in cwd, or nil for a spawn that is no Claude Code shape or has no cwd.
//
// A Claude Code shape is a command whose folder is named ccd-cli (the CLI path), or
// args with a stream-json arg as cliSessionKey reads it. Both record cwd (measured
// on Linux, macOS and Windows). Of the stream-json args only the bare `stream-json`
// is measured, and the two `--...-format=stream-json` forms are claustrum's own. The CLI path also records each --add-dir folder
// before a `--` arg (measured on Linux and Windows): the value of `--add-dir=<d>`,
// and each arg after `--add-dir` up to the next arg that starts with `-`. An empty
// value is skipped (measured on Linux). A relative --add-dir folder is taken in cwd,
// and a spawn with no cwd records nothing (claustrum's own, not measured). A spawn
// that carries the off switch records nothing (measured on Windows). Another spawn
// records also while such a request runs (measured on Linux).
func sessionFoldersOf(command string, args []string, cwd string) []string {
	if cwd == "" {
		return nil
	}
	cliPath := filepath.Base(filepath.Dir(command)) == "ccd-cli"
	if !cliPath && !streamJSONArgs(args) {
		return nil
	}
	folders := []string{cwd}
	if !cliPath {
		return folders
	}
	add := func(d string) {
		if d == "" {
			return
		}
		if !filepath.IsAbs(d) {
			d = filepath.Join(cwd, d)
		}
		folders = append(folders, d)
	}
	values := false
	for _, a := range args {
		switch {
		case a == "--":
			return folders
		case a == "--add-dir":
			values = true
		case strings.HasPrefix(a, "--add-dir="):
			values = false
			add(strings.TrimPrefix(a, "--add-dir="))
		case strings.HasPrefix(a, "-"):
			values = false
		case values:
			add(a)
		}
	}
	return folders
}

// streamJSONArgs reports whether args hold a stream-json arg, by the rule of
// cliSessionKey.
func streamJSONArgs(args []string) bool {
	for _, a := range args {
		if a == "--input-format=stream-json" || a == "--output-format=stream-json" || a == "stream-json" {
			return true
		}
	}
	return false
}
