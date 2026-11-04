//go:build unix

package git

// gitEnvWindowsForm is false on Linux and macOS: the cleaning of the git child
// environment uses ':' and a leading '/' there (gitchildenv.go).
const gitEnvWindowsForm = false

// sessionOffDropsControl is false on Linux and macOS: git.info keeps its
// settingsControl member under "sessionFoldersOff":true (b3081b51, measured on
// Linux, macOS not measured).
var sessionOffDropsControl = false
