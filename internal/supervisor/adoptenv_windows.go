//go:build windows

package supervisor

import (
	"slices"
	"strings"
)

// osKeptDaemonNames are the names that an adopt takes from the daemon on Windows.
// After an adopt of two entries the set held five, and the TEMP and TMP of the
// daemon reached the child unchanged (c9125eb8, measured on Windows). A sent TEMP
// or TMP wins (claustrum's own, not measured).
var osKeptDaemonNames = []string{"TEMP", "TMP"}

// adoptTakesLoginPATH is false on Windows: after an adopt with no PATH the child
// had no PATH (c9125eb8, measured on Windows).
const adoptTakesLoginPATH = false

// envKey is the name in upper case: names are not case-sensitive on Windows. The
// adopt line lists the names so (c9125eb8, measured on Windows).
func envKey(name string) string { return strings.ToUpper(name) }

// adoptedFixedEnv are the entries that a child of an adopted set got on Windows,
// with values that are not those of the daemon (c9125eb8, measured on Windows).
// They are not in the count of the adopt line. Each one is added only when the
// base has no entry of its name (claustrum's own, not measured).
var adoptedFixedEnv = []string{
	`COMSPEC=C:\Windows\System32\cmd.exe`,
	`PATHEXT=.COM;.EXE;.BAT;.CMD;.VBS;.JS;.WS;.MSC`,
	`PROMPT=$P$G`,
}

// osAdoptedBase adds each entry of adoptedFixedEnv that base lacks.
func osAdoptedBase(base []string) []string {
	for _, e := range adoptedFixedEnv {
		name := envName(e)
		if !slices.ContainsFunc(base, func(b string) bool { return sameEnvName(envName(b), name) }) {
			base = append(base, e)
		}
	}
	return base
}
