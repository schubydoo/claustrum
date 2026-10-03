package main

import "sync"

// The daemon reads the PATH of the login shell once, inside the first process.spawn
// that builds a child environment. It runs no login shell at its start. The result is
// kept for the life of the daemon, and a failed read is kept too: no later spawn
// tries again.
//
// Measured on Linux and macOS against 89cb6289 (rows G1 to G3). A daemon with no
// spawn runs no shell in 20 s. The first spawn runs the login shell and its reply
// comes after the read. The second spawn runs no shell. With a login shell that
// sleeps 10 s, the shell is killed after 4 s (loginPATHTimeout), the first spawn
// answers late, and the second spawn answers at once.
//
// Every spawn waits for the one read, so no child is built from a PATH from before
// the read: a spawn that comes while the read runs waits for its end.
var (
	loginPATHMu sync.Mutex
	// loginPATHOnce is non-nil once the daemon armed the read (armLoginPATH). It runs
	// the read one time.
	loginPATHOnce *sync.Once

	// loginPATH holds the PATH the login shell resolved, for spawned children
	// ONLY. It is deliberately not installed into the daemon's own environment:
	// the reference keeps the two separate, and mutating os.Environ made the
	// daemon resolve its OWN tools through the user's login PATH. Measured at
	// 5db5e4a with a fake `git` placed only on the login PATH — the reference ran
	// the real git, claustrum ran the fake one.
	loginPATH string

	// loginPATHExtractor is a seam for tests; production always uses the real
	// per-platform extractLoginPATH (a no-op on Windows).
	loginPATHExtractor = extractLoginPATH
)

// setLoginPATH records the extracted PATH for later child-env builds.
func setLoginPATH(p string) {
	loginPATHMu.Lock()
	loginPATH = p
	loginPATHMu.Unlock()
}

// currentLoginPATH returns the extracted PATH, or "" when extraction never ran
// or failed.
func currentLoginPATH() string {
	loginPATHMu.Lock()
	defer loginPATHMu.Unlock()
	return loginPATH
}

// armLoginPATH arms the read of the login-shell PATH. It runs no shell. The first
// awaitLoginPATH after it runs the read. Only the daemon arms it (runServe), so a
// test that boots a server through newServerOnSocket never starts a login shell.
func armLoginPATH() {
	loginPATHMu.Lock()
	loginPATHOnce = new(sync.Once)
	loginPATHMu.Unlock()
}

// awaitLoginPATH runs the armed read of the login-shell PATH if no call ran it
// before, and returns when it is done. A call that comes while the read runs waits
// for it. It returns at once when the read is not armed or is done. The wait is
// bounded by the loginPATHTimeout of extractLoginPATH.
func awaitLoginPATH() {
	loginPATHMu.Lock()
	once := loginPATHOnce
	loginPATHMu.Unlock()
	if once != nil {
		once.Do(func() { loginPATHExtractor() })
	}
}
