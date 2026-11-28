//go:build !linux

package main

// hostCredsPipe is false on macOS and Windows, where 13047a51 refuses every hostCreds
// of the right form (measured).
const hostCredsPipe = false

// hostCredsFeatures is empty on macOS and Windows. 13047a51 lists no
// process.spawn.host_creds_pipe there (measured on macOS and Windows).
var hostCredsFeatures []string
