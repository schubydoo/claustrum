//go:build darwin

package sessiondirs

// matchByIdentity is true on macOS. A PATH entry that names a session folder in
// other letter case is passed over (measured on macOS). The test of a folder above
// the git by its device and inode is claustrum's own.
const matchByIdentity = true
