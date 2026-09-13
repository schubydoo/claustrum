//go:build linux

package main

import (
	"os"
	"strconv"
	"strings"
)

// The linux-specific start-time source for the exec-child trampoline's CLAUDE_SSH_CHILD
// marker. The shared trampoline logic lives in execchild_unix.go. ownStartTicks and
// parseStartTicks are also used by the linux child registry (childrecord_linux.go).

// ownStartToken returns this process's start-time token for the CLAUDE_SSH_CHILD marker on
// linux: field 22 of /proc/self/stat (clock ticks since boot) as a decimal string.
func ownStartToken() string {
	return strconv.FormatInt(ownStartTicks(), 10)
}

// ownStartTicks returns this process's start-time in clock ticks since boot — field 22 of
// /proc/self/stat, the value the reference embeds in CLAUDE_SSH_CHILD. Field 2 (comm) is
// parenthesized and may contain spaces or ')', so parsing resumes after the last ')': the
// remaining fields start at 3 (state), so starttime (field 22) is index 22-3 = 19.
func ownStartTicks() int64 {
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
// by ownStartTicks and procStartTicks.
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
