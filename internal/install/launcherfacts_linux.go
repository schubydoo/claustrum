//go:build linux

package install

import "syscall"

// nfsSuperMagic is the f_type of statfs on NFS.
const nfsSuperMagic = 0x6969

// statLauncherFacts reads the owner, the mode bits and the file system type of p. It
// follows a link, so a folder of the path as written that is a link gives the facts
// of its target. The walk tests the holder folder of each link too.
func statLauncherFacts(p string) (launcherFacts, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(p, &st); err != nil {
		return launcherFacts{}, err
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(p, &fs); err != nil {
		return launcherFacts{}, err
	}
	return launcherFacts{uid: st.Uid, mode: st.Mode & 0o7777, nfs: fs.Type == nfsSuperMagic}, nil
}
