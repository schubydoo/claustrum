//go:build !linux

package main

// launcherOSFeatures is empty off Linux. macOS runs no launcher file check, and the macOS
// list of 4c111e86 is not measured. Windows lists neither launcher name.
var launcherOSFeatures []string
