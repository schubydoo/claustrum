package install

// SetLauncherFactsForTest replaces the owner, mode and file system facts of the file
// check, and the uid of the account that signs in. Only tests call it, so a test runs
// as any user. facts gets a path and returns its owner, its 12 mode bits and whether
// it lies on NFS. The returned func puts the old seams back.
func SetLauncherFactsForTest(facts func(p string) (uid, mode uint32, nfs bool, err error), ownUID int) (restore func()) {
	oldFacts, oldOwn := launcherPathFacts, launcherOwnUID
	launcherPathFacts = func(p string) (launcherFacts, error) {
		uid, mode, nfs, err := facts(p)
		return launcherFacts{uid: uid, mode: mode, nfs: nfs}, err
	}
	launcherOwnUID = func() int { return ownUID }
	return func() { launcherPathFacts, launcherOwnUID = oldFacts, oldOwn }
}
