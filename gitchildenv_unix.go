//go:build unix

package main

// gitEnvWindowsForm is false on Linux and macOS: the cleaning of the git child
// environment uses ':' and a leading '/' there (gitchildenv.go).
const gitEnvWindowsForm = false
