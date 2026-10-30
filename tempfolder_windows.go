package main

// dropUnusableTempVars keeps every inherited name on Windows. A TMP that names a
// missing folder stays (683cc6a9, measured on Windows). No folder is tested
// (claustrum's own for a folder that exists, not measured).
func dropUnusableTempVars(env []string) []string { return env }
