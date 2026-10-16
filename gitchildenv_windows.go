//go:build windows

package main

// gitEnvWindowsForm is true on Windows: the cleaning of the git child
// environment uses ';', the quote rule and a drive letter there (gitchildenv.go).
const gitEnvWindowsForm = true
