package main

import (
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// childTempDir is the temp dir of the child: the first value that is not empty of
// CLAUDE_CODE_TMPDIR, TMPDIR, TMP and TEMP, or /tmp. Measured on Linux:
// CLAUDE_CODE_TMPDIR comes before TMPDIR, an empty TMPDIR goes on to TMP, and TMP
// or TEMP alone is taken. The rest of the order and /tmp are claustrum's own, not
// measured.
func childTempDir(env []string) string {
	for _, k := range []string{"CLAUDE_CODE_TMPDIR", "TMPDIR", "TMP", "TEMP"} {
		if v := tempEnvValue(env, k); v != "" {
			return v
		}
	}
	return "/tmp"
}

// tempRootRegistryName is the name of the registry file of the temp dir dir, with the
// boot id and the pid namespace (measured on Linux). A part that cannot be read is
// empty (claustrum's own, not measured).
func tempRootRegistryName(dir string) string {
	boot, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	ns, _ := os.Readlink("/proc/self/ns/pid")
	return tempRootRegistryNameOf(strings.TrimRight(string(boot), "\n"), ns, dir)
}

// statDev and statMode give the device and the mode of st in the types of the step.
func statDev(st *unix.Stat_t) uint64  { return st.Dev }
func statMode(st *unix.Stat_t) uint32 { return st.Mode }
