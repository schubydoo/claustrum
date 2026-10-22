//go:build !linux

package main

// selfLocateFails is false on macOS and Windows: a launcher that cannot locate
// its own binary re-execs os.Args[0], as before. Those systems are not measured.
const selfLocateFails = false
