//go:build linux

package childrec

import (
	"os"
	"strconv"
	"strings"
)

// The linux start-time source of the child records and of the exec-child trampoline's
// CLAUDE_SSH_CHILD marker (execchild_linux.go of the daemon).

// OwnStartTicks returns this process's start-time in clock ticks since boot — field 22 of
// /proc/self/stat, the value the reference embeds in CLAUDE_SSH_CHILD. Field 2 (comm) is
// parenthesized and may contain spaces or ')', so parsing resumes after the last ')': the
// remaining fields start at 3 (state), so starttime (field 22) is index 22-3 = 19.
func OwnStartTicks() int64 {
	b, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0
	}
	return parseStartTicks(string(b))
}

// parseStartTicks extracts field 22 (starttime, clock ticks since boot) from the contents
// of a /proc/<pid>/stat line. Field 2 (comm) is parenthesized and may contain spaces or
// ')', so parsing resumes after the LAST ')': the remaining fields start at 3 (state), so
// starttime (field 22) is index 22-3 = 19. Returns 0 when the line cannot be parsed. Shared
// by OwnStartTicks and procStartTicks.
func parseStartTicks(stat string) int64 {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return 0
	}
	fields := strings.Fields(stat[i+1:])
	if len(fields) <= 19 {
		return 0
	}
	v, _ := strconv.ParseInt(fields[19], 10, 64)
	return v
}
