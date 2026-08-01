package main

import "sync"

// Login-shell PATH extraction is started once at daemon boot and awaited by the
// first child-env build. Splitting it into start/await keeps both properties the
// reference has: the socket opens without waiting for a slow login shell, *and*
// no spawned child is built from a pre-extraction PATH.
//
// The reference completes extraction before the first process.spawn builds its
// child env (its first-spawn latency is 250ms-1s where claustrum's was <50ms).
// Running the goroutine fire-and-forget let the first spawn race it and lose:
// booted with PATH=/usr/bin:/bin, the first child saw only that while the second
// saw the full login PATH. In a real session the first spawn *is* the agent CLI,
// so binaries under ~/.local/bin, nvm or cargo failed on attempt 1 and worked on
// retry.
//
// The wait is bounded by extractLoginPATH's own loginPATHTimeout, so a hung
// login shell delays the first spawn rather than blocking it forever.
var (
	loginPATHMu   sync.Mutex
	loginPATHWait chan struct{} // non-nil only while an extraction is in flight or done

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

// startLoginPATH begins login-shell PATH extraction in the background. Callers
// that later build a child env must call awaitLoginPATH first.
func startLoginPATH() {
	ch := make(chan struct{})
	loginPATHMu.Lock()
	loginPATHWait = ch
	loginPATHMu.Unlock()
	go func() {
		defer close(ch)
		loginPATHExtractor()
	}()
}

// awaitLoginPATH blocks until a started extraction finishes. It returns
// immediately when extraction was never started — the case for every test that
// boots a server through newServerOnSocket, which deliberately does not fork a
// login shell.
func awaitLoginPATH() {
	loginPATHMu.Lock()
	ch := loginPATHWait
	loginPATHMu.Unlock()
	if ch != nil {
		<-ch
	}
}
