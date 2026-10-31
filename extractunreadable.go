package main

import (
	"errors"
	"io/fs"
	"os"
	"strings"
)

// removeUnreadableDestEntry acts on an old destDir entry that the handle of its
// parent cannot read. On Windows a deny ACE for read and execute fails the lstat
// through that handle with "Access is denied.", before prepareOldDestDir runs.
// On 683cc6a9 an empty folder there goes, and a regular file there goes, and the
// extract goes on. A folder with content gives "clean destDir: RemoveAll <name>:
// Access is denied." and stays (Windows VM, a token with no backup and restore
// privileges). The maintainer approved this remove. unreadableDestDirGoes turns
// the step on, on Windows only.
//
// The step is one plain remove of the name through the parent handle, so a
// folder with content stays. It runs only for a permission error, and only if
// the path view of the entry is a plain folder or a regular file that the home
// tests do not refuse. A folder with another type bit, such as the irregular bit
// of a Windows cloud placeholder, is not a plain folder. A link, a junction and
// an entry of any other kind take no step (claustrum's own, not measured). Under the deny ACE the identity read of that
// folder can fail. Home and its parents then rely on wipesHomeDir and on the
// system, which refuses to remove a folder with content. A name that ends in a
// dot or a space takes no step, because the handle and the path can name two
// entries there (claustrum's own, not measured). True means the entry is gone.
func removeUnreadableDestEntry(parent *os.Root, cleanedDest, destName string, viewErr error) bool {
	if !unreadableDestDirGoes || !errors.Is(viewErr, fs.ErrPermission) || strings.TrimRight(destName, ". ") != destName {
		return false
	}
	byPath, err := lstatDestByPath(cleanedDest)
	if err != nil || byPath.Mode().Type() != fs.ModeDir && !byPath.Mode().IsRegular() || destEntryHoldsHome(cleanedDest, byPath) {
		return false
	}
	return parent.Remove(destName) == nil
}
