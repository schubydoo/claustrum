//go:build !linux

package main

// launcherOSFeatures is empty off Linux. macOS runs no launcher file check. 4c111e86
// lists neither launcher name on macOS or Windows (measured).
var launcherOSFeatures []string
