//go:build !linux

package main

import "errors"

// hostLimitPath is empty off Linux. 13047a51 reads no host settings file on macOS or
// Windows, and logs no line of it there (measured on macOS and Windows).
var hostLimitPath = ""

// hostLimitFeatures is empty off Linux. 13047a51 lists no server.unattended_limit and has
// no unattendedLimitMinutes member on macOS or Windows (measured).
var hostLimitFeatures []string

// limitEndRuns is false off Linux. No limit is read there, so no end at a limit runs.
const limitEndRuns = false

// readHostLimitFile reads nothing off Linux.
func readHostLimitFile(string) ([]byte, error) { return nil, errors.ErrUnsupported }
