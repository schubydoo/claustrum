//go:build windows

package main

import (
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// driveType returns the GetDriveType answer for a drive root such as `Z:\`. It
// is a seam for the tests.
var driveType = func(root string) uint32 {
	name, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return windows.DRIVE_UNKNOWN
	}
	return windows.GetDriveType(name)
}

// onNetwork is true for a UNC path and for a path on a network drive. A link
// onto a `net use` drive and a link to a `\\?\UNC\` path were measured. The
// test of a drive by GetDriveType (DRIVE_REMOTE) is claustrum's own. A `\\?\`
// or `\\.\` path with no UNC share and no drive letter is local (claustrum's
// own, not measured).
func onNetwork(p string) bool {
	switch {
	case hasPrefixFold(p, `\\?\UNC\`), hasPrefixFold(p, `\\.\UNC\`):
		return true
	case isLocalDevicePath(p):
		p = p[4:]
	case len(p) >= 2 && os.IsPathSeparator(p[0]) && os.IsPathSeparator(p[1]):
		return true
	}
	if len(p) < 2 || !isDriveLetter(p[0]) || p[1] != ':' {
		return false
	}
	return driveType(strings.ToUpper(p[:2])+`\`) == windows.DRIVE_REMOTE
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}
