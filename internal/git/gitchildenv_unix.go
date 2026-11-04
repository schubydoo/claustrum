//go:build unix

package git

// gitEnvWindowsForm is false on Linux and macOS: the cleaning of the git child
// environment uses ':' and a leading '/' there (gitchildenv.go).
const gitEnvWindowsForm = false

// sessionOffDropsControl is true on macOS and false on Linux. Under
// "sessionFoldersOff":true alone, git.info answers a repository with no
// settingsControl member on macOS, and keeps it on Linux (b3081b51, measured on
// Linux and macOS).
var sessionOffDropsControl = gitEnvDarwinForm
