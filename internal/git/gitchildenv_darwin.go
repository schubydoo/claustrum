//go:build darwin

package git

// gitEnvDarwinForm is true on macOS: SSH_SK_PROVIDER needs the value internal or an
// absolute path there (gitchildenv.go).
const gitEnvDarwinForm = true
