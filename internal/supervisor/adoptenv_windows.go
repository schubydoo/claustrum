//go:build windows

package supervisor

import "strings"

// adoptTakesLoginPATH is false on Windows: after an adopt with no PATH the child
// had no PATH (c9125eb8, measured on Windows).
const adoptTakesLoginPATH = false

// envKey is the name in upper case: names are not case-sensitive on Windows. The
// adopt line lists the names so (c9125eb8, measured on Windows).
func envKey(name string) string { return strings.ToUpper(name) }

// keptOnlyNames are the sent names that reach a child on Windows only when kept
// names them. A sent HOME did not reach the child, and with HOME in kept the sent
// HOME did (c9125eb8, measured on Windows). The adopt line still counts it, and
// its count then matched c9125eb8 in that row.
var keptOnlyNames = []string{"HOME"}
