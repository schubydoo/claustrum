//go:build windows

package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// maxListLinks is the most links that a chain may hold for the head to count as
// a directory. 31 links give true and 32 give false (build 683cc6a9, Windows).
const maxListLinks = 31

// listIsDir decides isDir of one files.list entry on Windows (build 683cc6a9).
// A link counts as a directory only if the link itself has the directory
// attribute, so a file link to a folder is false. A junction counts only with
// junctionsAsDirs. A chain of more than 31 links is false, and so is a hop to
// a network place (onNetwork). A direct link to `\\localhost\c$`, to
// `\\?\UNC\localhost\c$\o\real` and to a folder of a mapped network drive was
// measured. Other network and redirected-device cases, a junction inside a chain
// and a name that ends in a dot or a space are claustrum's own, not measured.
func listIsDir(full string, junctionsAsDirs bool) bool {
	cur := full
	for links := 0; ; {
		fi, err := os.Lstat(cur)
		if err != nil {
			return false
		}
		if fi.Mode()&(fs.ModeSymlink|fs.ModeIrregular) == 0 {
			return fi.IsDir()
		}
		target, err := os.Readlink(cur)
		if err != nil {
			// A reparse point that is no link (a cloud placeholder, for
			// example) keeps the Stat answer at the entry and is false behind
			// a link (claustrum's own, not measured).
			st, serr := os.Stat(cur)
			return links == 0 && serr == nil && st.IsDir()
		}
		if links == 0 {
			attr, ok := fi.Sys().(*syscall.Win32FileAttributeData)
			if !ok || attr.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY == 0 {
				return false
			}
			if fi.Mode()&fs.ModeSymlink == 0 && !junctionsAsDirs {
				return false
			}
		}
		links++
		if links > maxListLinks {
			return false
		}
		if onNetwork(target) {
			return false
		}
		if !filepath.IsAbs(target) {
			dir := filepath.Dir(cur)
			if strings.HasPrefix(target, `\`) || strings.HasPrefix(target, "/") {
				// A root with no drive joins the volume of the link's folder.
				target = filepath.VolumeName(dir) + target
			} else {
				target = filepath.Join(dir, target)
			}
		}
		cur = target
	}
}

// isLocalDevicePath is true for the `\\?\` and `\\.\` spellings of a local
// path. They are not shares (claustrum's own, not measured).
func isLocalDevicePath(p string) bool {
	return strings.HasPrefix(p, `\\?\`) || strings.HasPrefix(p, `\\.\`)
}
