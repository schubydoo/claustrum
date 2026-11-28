//go:build linux

package main

// hostCredsPipe is true where a hostCreds spawn hands its env to the child through a
// pipe (13047a51, measured on Linux).
const hostCredsPipe = true

// hostCredsFeatures is the last name of the features on Linux. 13047a51 lists it
// last, also with CLAUDE_SSH_LEAVE_GROUPS=1 (measured on Linux). It lists other new
// names before it, which this list does not hold.
var hostCredsFeatures = []string{hostCredsFeature}
