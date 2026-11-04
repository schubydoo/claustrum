package sessiondirs

// UseForTest makes a new store the current one and returns the function that puts
// the old one back. Both steps clear the mark of a logged OFF line. dir is its record folder, or "" for folders in memory only. A
// test outside this package uses it, so that no folder of one test reaches another.
func UseForTest(dir string) (restore func()) {
	old := current.Load()
	s := newStore(dir)
	if dir != "" {
		s.load()
	}
	current.Store(s)
	clearOffLine()
	return func() {
		current.Store(old)
		clearOffLine()
	}
}

func clearOffLine() {
	offMu.Lock()
	offLogged = false
	offMu.Unlock()
}
