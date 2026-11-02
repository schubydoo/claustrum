package main

import (
	"io"

	"github.com/schubydoo/claustrum/internal/install"
)

// The names of internal/install under the names that the code of package main
// uses.

// runInstallArm is install.Run. It is a var only so a test of the -install arm
// of main captures the Options and installs nothing.
var runInstallArm = install.Run

const (
	managedStatusUnusable   = install.StatusUnusable
	managedStatusUnreadable = install.StatusUnreadable
)

func runProbeCLI(stdout, stderr io.Writer, path string) { install.RunProbeCLI(stdout, stderr, path) }

func resolveManagedLauncherNoted(cliPath string) (managedLauncherResult, string) {
	return install.Resolve(cliPath)
}

func checkManagedLauncher(argv []string, ownPath string) string {
	return install.Check(argv, ownPath)
}

func isExecutableRegular(p string) bool { return install.IsExecutableRegular(p) }

func managedLauncherHostRefusal() string { return install.HostRefusal() }

func stripManagedLauncherEnv(env []string, launched bool) []string {
	return install.StripEnv(env, launched)
}
