//go:build unix && !darwin

package sessiondirs

// matchByIdentity is false on Linux: the paths match by text.
const matchByIdentity = false
