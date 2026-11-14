package main

import (
	"golang.org/x/sys/unix"

	"github.com/schubydoo/claustrum/internal/procinfo"
)

// childTempDir is the temp dir of the child on macOS: the value of
// CLAUDE_CODE_TMPDIR in its env. c9125eb8 and 4c111e86 take no folder for TMPDIR
// alone (measured on macOS). An empty value gives no temp dir, so
// the step adds nothing (claustrum's own, not measured).
func childTempDir(env []string) string {
	return tempEnvValue(env, "CLAUDE_CODE_TMPDIR")
}

// tempRootRegistryName is the name of the registry file of the temp dir dir on macOS:
// the boot session id from kern.bootsessionuuid and an empty pid namespace. The
// registry on macOS is claustrum's own, not measured.
func tempRootRegistryName(dir string) string {
	return tempRootRegistryNameOf(procinfo.NodeID(), "", dir)
}

// statDev and statMode give the device and the mode of st in the types of the step.
func statDev(st *unix.Stat_t) uint64  { return uint64(st.Dev) }
func statMode(st *unix.Stat_t) uint32 { return uint32(st.Mode) }
