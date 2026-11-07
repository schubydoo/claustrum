//go:build unix && !linux && !darwin

package files

import "golang.org/x/sys/unix"

// openParentPath has no way to read the real path of a handle on this system,
// so every call that opens its parent answers parent_unverified with ENOTSUP
// (claustrum's own, not measured).
func openParentPath(int) (string, error) {
	return "", unix.ENOTSUP
}
