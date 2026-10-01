//go:build !darwin && !windows

package main

// managedSettingsSystemDir is the Linux managed-settings folder (VM-measured).
const managedSettingsSystemDir = "/etc/claude-code"
