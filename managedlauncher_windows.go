//go:build windows

package main

// managedSettingsDir is empty on Windows, so launcher.resolve answers none and
// -install and -probe-cli run the CLI directly. Measured on a Windows VM: 89cb6289
// answered {"status":"none"} for every fixture tried. The fixtures were a settings
// file in C:\Program Files\ClaudeCode, the E2E variable folder and
// C:\ProgramData\ClaudeCode. The launcher never ran. With CLAUDE_SSH_MANAGED_LAUNCHER=1
// and a CLI path, -install appends "launcherStatus":"none" and -probe-cli shows no
// difference.
func managedSettingsDir() (dir string, ignoredVar bool) { return "", false }

// managedIgnoredVarLine is never logged on Windows: managedSettingsDir never
// ignores the variable there.
func managedIgnoredVarLine() string { return "" }

// managedLauncherHostRefusal is the reason every Windows spawn with a launcher is
// refused, an empty launcher too (VM-measured).
func managedLauncherHostRefusal() string { return "a launcher cannot be applied on a Windows host" }
