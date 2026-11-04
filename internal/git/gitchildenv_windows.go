//go:build windows

package git

// gitEnvWindowsForm is true on Windows: the cleaning of the git child
// environment uses ';', the quote rule and a drive letter there (gitchildenv.go).
const gitEnvWindowsForm = true

// sessionOffDropsControl is true on Windows: a git.info with
// "sessionFoldersOff":true alone answers with no settingsControl member (b3081b51,
// measured on Windows).
var sessionOffDropsControl = true
