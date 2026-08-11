package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// tokenPipeEnv names the file descriptor the daemonized child reads its auth
// token from when the token was supplied via -token-fd. The parent reads the
// caller's fd (only valid pre-daemonize) and forwards the token to the child
// over an inherited pipe; this env carries only the fd number, never the token.
const tokenPipeEnv = "CLAUSTRUM_TOKEN_PIPE"

// daemonChildEnv is the *internal* sentinel that tells a freshly-exec'd process
// "you are the detached child, do not re-daemonize." It is deliberately
// claustrum-namespaced (not the reference daemon's CLAUDE_SSH_DAEMON_CHILD)
// because a host that is itself running inside a real claude-ssh session exports
// CLAUDE_SSH_DAEMON_CHILD=1 to every descendant — including the claustrum
// launcher — which would make the launcher mistake itself for the child, skip
// the parent/daemonize+token-forward path, and die with "read --token-file:
// open :". This sentinel is purely a re-exec marker and never crosses the wire,
// so namespacing it is free. The separate reference-parity marker that
// process.spawn children must still inherit is daemonChildMarker, set in
// daemonizeWithToken. See docs/PROTOCOL.md.
const daemonChildEnv = "CLAUSTRUM_DAEMON_CHILD"

// daemonChildMarker is the env var the *reference* daemon carries in its environ
// after its own self-daemonize re-exec, and which it propagates verbatim into
// every process.spawn child. We set it on our re-exec purely to preserve that
// observable parity (pinned by TestSpawnInheritsDaemonChildMarker) — it is NOT
// used to detect the re-exec (that is daemonChildEnv), so an ambient copy
// inherited from a surrounding claude-ssh session is harmless.
const daemonChildMarker = "CLAUDE_SSH_DAEMON_CHILD"

// osExit is os.Exit behind a seam so the daemon-lifecycle shells (runServe,
// daemonizeWithToken, teardown, main) are testable in-process: production never
// reassigns it, and a test stub must panic (never return) because every call
// site relies on it not coming back.
var osExit = os.Exit

// server is the running -serve daemon: the AF_UNIX listener, the auth token, the
// connected clients, and the process manager.
type server struct {
	token string
	ln    net.Listener
	procs *procManager

	mu           sync.Mutex
	conns        map[*conn]struct{}
	shutdown     chan struct{}
	once         sync.Once
	metricsLn    net.Listener // optional Prometheus listener; nil unless -metrics-addr set
	pipeLn       net.Listener // optional Windows named-pipe listener; nil unless -listen-pipe set (Windows-only)
	keepChildren bool         // -keep-children: leave child processes running on graceful shutdown (POSIX-only)
}

// conn is one connected client. The write mutex serializes the interleaving of
// JSON-RPC responses and id-less stream notifications on the same socket.
type conn struct {
	nc     net.Conn
	wmu    sync.Mutex
	closed bool

	// process.stdin is the one method whose ARRIVAL ORDER is part of its meaning:
	// the chunks are a byte stream, and delivering them out of order corrupts it.
	// Everything else on a connection stays concurrent (the reference dispatches
	// concurrently and replies can return out of order — that is contract), but
	// stdin requests take a ticket in read order and are admitted one at a time.
	//
	// Measured at 5db5e4a: 20 stdin chunks pipelined back-to-back on one
	// connection arrive in order on the reference and scrambled on claustrum
	// (L02 L03 L00 L08 L04 …), because each request ran in its own goroutine and
	// raced to the queue.
	stdinMu     sync.Mutex
	stdinCond   *sync.Cond
	stdinNext   uint64 // next ticket to hand out
	stdinServed uint64 // ticket currently admitted
}

// nextStdinTicket reserves this request's place in the stdin stream. Called from
// the connection's read loop, so tickets follow wire order exactly.
func (c *conn) nextStdinTicket() uint64 {
	c.stdinMu.Lock()
	defer c.stdinMu.Unlock()
	if c.stdinCond == nil {
		c.stdinCond = sync.NewCond(&c.stdinMu)
	}
	t := c.stdinNext
	c.stdinNext++
	return t
}

// awaitStdinTurn blocks until every earlier stdin request on this connection has
// finished.
func (c *conn) awaitStdinTurn(t uint64) {
	c.stdinMu.Lock()
	defer c.stdinMu.Unlock()
	for c.stdinServed != t {
		c.stdinCond.Wait()
	}
}

// doneStdinTurn admits the next stdin request.
func (c *conn) doneStdinTurn(t uint64) {
	c.stdinMu.Lock()
	c.stdinServed = t + 1
	c.stdinCond.Broadcast()
	c.stdinMu.Unlock()
}

// writeJSON is ONE OF THREE outbound encodes — see also conn.writeResponse
// below (every JSON-RPC reply) and managedProc.emit (every live stream frame).
// This one carries reattach REPLAY frames only, so it is the narrowest of the
// three; the comment used to call it "the single outbound encode", which would
// send someone doing an encoder cleanup looking in exactly one place and missing
// the two that matter more.
//
// All three must stay on json.Marshal, not an Encoder: Marshal HTML-escapes
// `< > &` with no opt-out, which is what the reference emits, and
// SetEscapeHTML(false) would look like a cleanup and move the wire — see
// docs/ARCHITECTURE.md → "Inherited wire bytes".
func (c *conn) writeJSON(v interface{}) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.writeLine(append(b, '\n'))
}

// writeLine writes an already-marshaled, newline-terminated JSON line under the
// same lock/closed semantics as writeJSON. It lets a frame fanned out to many
// subscribers be marshaled once (see managedProc.emit) instead of once per conn.
// Write does not mutate b, so the same slice is safe to hand to every conn.
func (c *conn) writeLine(b []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	_, err := c.nc.Write(b)
	return err
}

// writeResponse writes a JSON-RPC response and, on a write error (e.g. the
// client dropped the connection mid-reply), emits the reference daemon's
// writeResponse/Failed-to-write log lines with the partial byte count.
func (c *conn) writeResponse(v interface{}) {
	// json.Marshal, not an Encoder: this is the encode that carries EVERY
	// JSON-RPC reply, and its HTML escaping is inherited wire behaviour — see
	// docs/ARCHITECTURE.md → "Inherited wire bytes" and the note on writeJSON.
	b, err := json.Marshal(v)
	if err != nil {
		logErrorf("[Server] Failed to write response: %v", err)
		return
	}
	b = append(b, '\n')
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed {
		return
	}
	n, err := c.nc.Write(b)
	if err != nil {
		logDebugf("[Server] writeResponse: wrote %d/%d bytes, error=%v", n, len(b), err)
		logErrorf("[Server] Failed to write response: %v", err)
	}
}

// runServe self-daemonizes (reparenting to init) then runs the RPC server. The
// child is marked with daemonChildEnv (CLAUSTRUM_DAEMON_CHILD) so we re-exec
// exactly once — see that const for why it is not CLAUDE_SSH_DAEMON_CHILD.
func runServe(socket, tokenFile string, tokenFd int, metricsAddr string, keepChildren, listenPipe bool) {
	if os.Getenv(daemonChildEnv) != "1" {
		// Parent. An fd is only valid in this process and would not survive the
		// re-exec, so read it now and forward the token to the child over an
		// inherited pipe — never via disk, argv, or environ. The -token-file path
		// is unchanged: the file persists across the re-exec for the child to read.
		if tokenFd >= 0 {
			token, err := readTokenFD(tokenFd)
			if err != nil {
				fmt.Fprintf(os.Stderr, "claustrum: read --token-fd: %v\n", err)
				osExit(1)
			}
			daemonizeWithToken(socket, token)
			return
		}
		daemonizeWithToken(socket, "")
		return
	}

	// We are the detached child.

	// The missing-token-source check lives HERE, in the child, not in the parent.
	//
	// That looks like the worse place to put it and it is what the reference
	// does. Measured 2026-08-02 against 5db5e4a, `-serve` with no token flags:
	//
	//	reference : exit 1 after 10.07s
	//	            "claude-ssh: timeout waiting for daemon to accept on <sock>"
	//	claustrum : exit 1 after 0.03s
	//	            "claustrum: daemonized child requires --token-file or …"
	//
	// So the reference's parent daemonizes regardless, its child refuses to
	// start, and the operator sees the launcher's accept timeout — the real
	// reason is only in the child's own log. claustrum answered 300x faster and
	// said exactly what was wrong. Matching costs both of those.
	//
	// This is parity on purpose. Failing fast in the parent is recorded as a
	// candidate divergence rather than kept — recorded in docs/DIVERGENCES.md
	// under "Candidates considered but not taken", which is a record, not a
	// decision. Note
	// the zero-byte -token-file case ALREADY behaved this way (the child rejects
	// an empty token and the parent times out), so this only aligns the one path
	// that short-circuited early.
	if tokenFile == "" && tokenFd < 0 {
		fmt.Fprintln(os.Stderr, "claustrum: daemonized child requires --token-file or --token-fd")
		osExit(1)
	}

	token, err := childToken(tokenFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "claustrum: %v\n", err)
		osExit(1)
	}

	// Extract a real interactive PATH from the login shell so spawned children
	// resolve tools the way an interactive session would. Run in a goroutine so
	// a stalling login shell does not delay the daemon socket opening (matches
	// reference binary behavior; extractLoginPATH has its own internal timeout).
	// buildEnv awaits it, so no child is built from a pre-extraction PATH.
	// Kept in this shell — not newServerOnSocket — so tests booting a server
	// never fork a login shell (which mutates the test process's PATH).
	startLoginPATH()

	s, err := newServerOnSocket(socket, token, metricsAddr, keepChildren, listenPipe)
	if err != nil {
		fmt.Fprintf(os.Stderr, "claustrum: %v\n", err)
		osExit(1)
	}

	fmt.Printf("Claustrum remote server listening on %s\n", socket)
	s.run(socket)
}

// childToken obtains the detached child's auth token — over the forwarded pipe
// (-token-fd path, fd named by tokenPipeEnv) or from --token-file (read once,
// then unlinked) — and scrubs the env vars that must not leak into
// process.spawn children. Split out of runServe so the token plumbing is
// testable without the daemonize/osExit shell.
func childToken(tokenFile string) (string, error) {
	var token string
	if fdStr := os.Getenv(tokenPipeEnv); fdStr != "" {
		fd, err := strconv.Atoi(fdStr)
		if err == nil {
			token, err = readTokenFD(fd)
		}
		if err != nil {
			return "", fmt.Errorf("read token pipe: %v", err)
		}
		_ = os.Unsetenv(tokenPipeEnv)
	} else {
		b, err := os.ReadFile(tokenFile)
		if err != nil {
			return "", fmt.Errorf("read --token-file: %v", err)
		}
		token = normalizeToken(b)
		// An unlink failure is worth a line: the token file is supposed to be
		// consumed, and one left on disk is a credential the operator does not
		// know is still there.
		if err := os.Remove(tokenFile); err != nil {
			logWarnf("[daemon] failed to remove --token-file %s: %v", tokenFile, err)
		}
	}
	// An empty token must be fatal. Otherwise the daemon comes up healthy and
	// listening while nothing can ever authenticate to it: every request fails
	// -32001 forever, and the operator sees a running service. Measured at
	// 5db5e4a with a zero-byte -token-file — the reference refuses to start (its
	// launcher reports "timeout waiting for daemon to accept" and exits 1) where
	// claustrum happily served a permanently unauthenticatable socket.
	if token == "" {
		return "", fmt.Errorf("token is empty")
	}
	_ = os.Unsetenv("CLAUDE_RPC_TOKEN") // prevent token propagation through daemonize → os.Environ()
	// Drop our internal re-exec sentinel now that we have consumed it: buildEnv
	// bases process.spawn children on os.Environ(), and the reference daemon's
	// environ has no CLAUSTRUM_DAEMON_CHILD — leaking it would be a (detectable)
	// divergence. The reference-parity marker (daemonChildMarker) stays set.
	_ = os.Unsetenv(daemonChildEnv)
	return token, nil
}

// newServerOnSocket performs the daemonized child's startup in its exact
// original order — clear stale socket, listen, chmod, persist the token, gate
// the POSIX/Windows-only flags, construct the server, start the optional
// metrics and pipe listeners — and hands back the ready-to-run server. Split
// out of runServe (same pattern as enablePipe/startAcceptLoops) so the whole
// boot sequence is testable without the daemonize/osExit shell.
func newServerOnSocket(socket, token, metricsAddr string, keepChildren, listenPipe bool) (*server, error) {
	_ = os.Remove(socket) // clear a stale socket
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return nil, fmt.Errorf("listen unix: %v", err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "claustrum: chmod socket: %v\n", err)
	}

	// Persist the token beside the now-listenable socket so a client can
	// reconnect to this daemon and re-authenticate after the original -token-file
	// was unlinked / the -token-fd pipe closed. Removed again in teardown. Matches
	// the reference daemon (upstream 5db5e4a); best-effort and non-fatal.
	persistToken(socket, token)

	// -keep-children is POSIX-only. honorKeepChildren returns the flag unchanged on
	// Unix and false-with-a-warning on Windows (where children live in a Job Object
	// the OS tears down on daemon exit regardless) — so we never claim to keep them
	// while the OS kills them. Evaluated here in the daemonized child so the warning
	// logs once, from the running daemon.
	keepChildren = honorKeepChildren(keepChildren)

	// -listen-pipe is Windows-only. honorListenPipe returns the flag unchanged on
	// Windows and false-with-a-warning elsewhere (the named-pipe transport has no
	// meaning off Windows) — the same shape as honorKeepChildren above, evaluated
	// in the daemonized child so any warning logs once from the running daemon.
	listenPipe = honorListenPipe(listenPipe)

	s := &server{
		token:        token,
		ln:           ln,
		procs:        newProcManager(),
		conns:        make(map[*conn]struct{}),
		shutdown:     make(chan struct{}),
		keepChildren: keepChildren,
	}
	// Optional Prometheus metrics endpoint (opt-in via -metrics-addr). A bind
	// failure is non-fatal — the daemon's job is the socket, not the metrics.
	if metricsAddr != "" {
		if ln, err := startMetricsServer(metricsAddr); err != nil {
			logErrorf("[Server] metrics: listen %s: %v", metricsAddr, err)
		} else {
			s.metricsLn = ln
			logInfof("[Server] metrics: serving Prometheus counters on %s/metrics", metricsAddr)
		}
	}

	// Optional additional Windows named-pipe listener (opt-in via -listen-pipe).
	s.enablePipe(socket, listenPipe)
	return s, nil
}

// normalizeToken mirrors the reference daemon's token-file handling: it reads
// the token as a line, so a single trailing newline (`\n` or `\r\n`) from the
// SFTP-uploaded file is stripped, while spaces and other interior/surrounding
// whitespace are preserved verbatim (probe-verified — see
// scratch/probe/contract_probe.sh). Without this, a token file that ends in a
// newline would make every client request fail auth even though the client sent
// the correct token.
func normalizeToken(b []byte) string {
	return strings.TrimRight(string(b), "\r\n")
}

// readTokenFD reads (and normalizes) an auth token from an already-open file
// descriptor. It reads to EOF, so the writer closes its end after writing.
func readTokenFD(fd int) (string, error) {
	f := os.NewFile(uintptr(fd), "token-fd")
	if f == nil {
		return "", fmt.Errorf("invalid file descriptor %d", fd)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	return normalizeToken(b), nil
}

// daemonLogName is the file the daemonized child's stdout and stderr are
// redirected to, alongside the socket. The fixed name and location are the
// deployment contract, the same as daemon.token, so neither is configurable.
const daemonLogName = "remote-server.log"

// openDaemonLog creates a FRESH remote-server.log beside the socket, mode 0600,
// returning nil when it cannot get one that is exclusively ours.
//
// Unlink-then-create-exclusively, not open-with-O_TRUNC. Probe-measured against
// the reference at 5db5e4a with the log pre-planted as another user's
// world-writable file:
//
//	planted:   666 root
//	reference: 600 claude   <- the OWNER changed, so it recreated the file
//	claustrum: 666 root     <- before this, it truncated and wrote into it
//
// chmod cannot change an owner, and chmod(2) on another user's file fails with
// EPERM anyway, so truncate-and-chmod cannot reproduce that and cannot secure
// the file. Removing first does both: it matches the reference's fresh-log
// semantics and guarantees the daemon's stdout/stderr land somewhere only this
// user can read.
//
// O_EXCL is the backstop. If the remove failed — a sticky directory holding
// another user's file — the create fails too and this returns nil, so
// daemonizeWithToken falls back to inherited stdio rather than writing the
// daemon's output into a file someone else owns. That case IS measured as of
// 2026-08-06: in a sticky directory the reference truncates the foreign file and
// writes into it. Declining is therefore a deliberate divergence, filed as D8 in
// docs/DIVERGENCES.md. It is always-on because the trigger is unreachable
// on the deployed path (rule 3 clause (b)): the per-user session directory is not sticky, so no
// honest caller reaches this branch at all.
//
// The log is NOT removed on shutdown; unlike the socket and daemon.token it
// outlives the daemon, so a post-mortem is still readable.
func openDaemonLog(socket string) *os.File {
	if socket == "" {
		return nil
	}
	path := filepath.Join(filepath.Dir(socket), daemonLogName)
	_ = os.Remove(path) // best-effort; O_EXCL below is what actually guarantees it
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return nil
	}
	return f
}

// daemonizeWithToken re-execs the binary detached from the terminal. If
// forwardToken is non-empty (the -token-fd path) it is handed to the child over
// an inherited pipe (child fd 3, named by tokenPipeEnv) so the handoff puts the
// token on no disk, argv, or environment; an empty forwardToken means the child
// reads its own -token-file as before.
// daemonStartTimeout caps how long the -serve launcher waits for the daemonized
// child to start accepting before it returns anyway. The reference uses 10s
// (a 1e10 ns deadline in its spawnChild, alongside the string "timeout waiting
// for daemon to accept on %s"). var so tests can shrink it.
var daemonStartTimeout = 10 * time.Second

func daemonizeWithToken(socket, forwardToken string) {
	// Create the socket's directory before anything opens a file in it — the
	// reference does this in its launcher (string "mkdir parent %s: %v") and
	// creates the chain 0700, the same owner-only mode it uses for the cli-dir.
	// Measured: with a missing socket directory the reference starts normally and
	// leaves d sub(700) / rpc.sock(600) / daemon.token(600) / remote-server.log(600)
	// behind, while claustrum refused to start at all.
	//
	// The error is deliberately not reported here: a failure surfaces immediately
	// as the child's bind error, and the reference prints nothing to the
	// launcher's stderr on this path.
	if dir := filepath.Dir(socket); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o700)
	}
	self, err := os.Executable()
	if err != nil {
		self = os.Args[0]
	}
	cmd := exec.Command(self, os.Args[1:]...)
	// daemonChildEnv is our private re-exec sentinel (detected in runServe);
	// daemonChildMarker is set only to mirror the reference daemon's environ so
	// it propagates into process.spawn children (TestSpawnInheritsDaemonChildMarker).
	cmd.Env = append(os.Environ(), daemonChildEnv+"=1", daemonChildMarker+"=1")
	// Detach from the controlling terminal/session (OS-specific).
	cmd.SysProcAttr = detachSysProcAttr()
	cmd.Stdin = nil
	// Both of the child's streams go to remote-server.log beside the socket, the
	// way the reference does it — its own stdout and stderr are 0 bytes. The
	// parent opens the file so the log exists from the moment the daemon starts,
	// and closes its copy once the child owns it.
	//
	// BOTH streams, not just stderr: claustrum splits its output, printing the
	// "listening on …" banner to stdout and log lines to stderr. Redirecting only
	// stderr would leave the banner on the terminal and produce a log missing its
	// first line.
	logFile := openDaemonLog(socket)
	if logFile != nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		defer logFile.Close()
	} else {
		// Could not open the log (missing or unwritable socket directory): fall
		// back to inherited stdio rather than refusing to start. The daemon is
		// about to fail on the socket anyway if the directory is really absent.
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	var pipeW *os.File
	if forwardToken != "" {
		pr, pw, err := os.Pipe()
		if err != nil {
			fmt.Fprintf(os.Stderr, "daemonize: token pipe: %v\n", err)
			osExit(1)
		}
		pipeW = pw
		// ExtraFiles[0] becomes fd 3 in the child; tell it where to read.
		cmd.ExtraFiles = []*os.File{pr}
		cmd.Env = append(cmd.Env, tokenPipeEnv+"=3")
		defer pr.Close() // parent's copy of the read end; the child has its own
	}

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "daemonize: %v\n", err)
		osExit(1)
	}
	if pipeW != nil {
		_, _ = io.WriteString(pipeW, forwardToken)
		_ = pipeW.Close() // closing the write end gives the child's read an EOF
	}
	// Do not return until the child is actually accepting. The reference's
	// launcher blocks here, so a client that runs `-serve` over SSH and connects
	// immediately always finds a listening socket; claustrum returned the moment
	// the fork succeeded and lost that race. Measured at 5db5e4a — socket present
	// at the instant the launcher returned: reference YES, claustrum NO.
	if !waitForDaemonAccept(socket) {
		// The socket never appeared. The reference reports exactly this and exits
		// 1 — measured with a zero-byte -token-file, where its child refuses to
		// start: "claude-ssh: timeout waiting for daemon to accept on <socket>",
		// exit 1, after the full 10.06s deadline.
		fmt.Fprintf(os.Stderr, "claustrum: timeout waiting for daemon to accept on %s\n", socket)
		osExit(1)
	}
	osExit(0)
}

// waitForDaemonAccept waits for the daemonized child to come up, reporting
// whether it did. It polls for the socket PATH TO EXIST, then dials once to
// confirm, and that dial is what puts a "New connection from: @" /
// "Connection closed: @" pair at the top of a freshly started daemon's log.
//
// Polling for existence rather than for a successful dial is not a shortcut —
// it is what the reference does, and the two are distinguishable. Measured at
// 5db5e4a:
//
//	socket path occupied by a directory  path exists at once -> exit 0 in 0.08s
//	child never binds (empty token file) path never appears  -> exit 1 at 10.06s
//
// A dial-based wait would invert both: it would sit out the full deadline on the
// occupied path (nothing ever accepts there) and it would give up early on any
// child that dies, where the reference keeps waiting. The confirming dial's
// result is deliberately ignored for the same reason.
func waitForDaemonAccept(socket string) bool {
	deadline := time.Now().Add(daemonStartTimeout)
	for {
		if _, err := os.Stat(socket); err == nil {
			if c, err := net.Dial("unix", socket); err == nil {
				_ = c.Close()
			}
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// enablePipe starts the optional Windows named-pipe listener when requested and
// records it on s. Strictly additive: the AF_UNIX socket is untouched, the pipe
// serves the exact same JSON-RPC dispatch, and startPipeTransport publishes the
// pipe's name to rpc.pipe beside the socket *before* it begins accepting. A setup
// failure is non-fatal — the socket still serves. Off Windows honorListenPipe has
// already forced listenPipe false, so this only clears a stale file there. Split
// out of runServe so the enable/guard/error path is unit-testable without
// runServe's daemonize+os.Exit shell (the success arm that stores a live pipe
// listener is Windows-only, covered by the windows-latest CI leg).
//
// It maintains one invariant on every -serve startup — mirroring the stale-socket
// clear (os.Remove(socket)): rpc.pipe exists iff a pipe is actively served this
// boot. The pipe name is per-boot-random, so a leftover rpc.pipe from an unclean
// crash names a pipe that no longer exists; when we do not serve a pipe (flag off,
// non-Windows, or startPipeTransport failed) we remove any such stale file so a
// Windows client can never read it and dial a dead pipe. When we do serve one,
// startPipeTransport has already written the fresh name.
func (s *server) enablePipe(socket string, listenPipe bool) {
	if !listenPipe {
		removePipeNameFile(socket) // not serving a pipe this boot → no stale rpc.pipe
		return
	}
	pln, err := startPipeTransport(socket)
	if err != nil {
		logErrorf("[Server] named-pipe transport: %v", err)
		// startPipeTransport writes rpc.pipe only on success, so any file present now
		// is stale from a prior boot — clear it rather than leave a dead pointer.
		removePipeNameFile(socket)
		return
	}
	s.pipeLn = pln
	logInfof("[Server] also listening on named pipe %s", pln.Addr())
}

// startAcceptLoops launches the accept loop for the socket and, when present, a
// second one for the optional pipe listener. Both feed the same serveConn, so the
// AF_UNIX path is byte-for-byte unchanged. Split out of run so the second-listener
// branch is testable without run's blocking shutdown wait + os.Exit.
func (s *server) startAcceptLoops() {
	go s.acceptLoop(s.ln)
	if s.pipeLn != nil {
		go s.acceptLoop(s.pipeLn)
	}
}

func (s *server) run(socket string) {
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGTERM, syscall.SIGINT)
	go func() { <-sigc; s.signalShutdown() }()
	s.startAcceptLoops()

	// Block here until shutdown, then tear down synchronously on the main
	// goroutine. teardown closes the listener (unblocking acceptLoop's Accept) and
	// then stops children + drops clients before os.Exit. Running it inline — not
	// in a goroutine racing the accept loop's return out of run()/main — guarantees
	// the kill-or-keep decision (and its log line) actually completes: previously
	// the accept loop returned the moment the listener closed and main exited the
	// process first, skipping the child teardown entirely.
	<-s.shutdown
	s.teardown(socket)
}

// acceptLoop accepts connections on ln until it is closed on shutdown. It is
// listener-agnostic so the AF_UNIX socket and the optional Windows named pipe
// share one code path — every accepted conn goes through the same serveConn.
func (s *server) acceptLoop(ln net.Listener) {
	// tempDelay backs off after consecutive non-shutdown accept errors so a
	// listener wedged into returning errors forever — e.g. an optional named pipe
	// in a bad state — can't hot-spin the CPU or flood the log. This is the
	// net/http Server.Serve pattern: start small, double, cap at 1s, reset on a
	// good Accept. The happy path (and the shutdown branch) is unaffected.
	var tempDelay time.Duration
	for {
		nc, err := ln.Accept()
		if err != nil {
			select {
			case <-s.shutdown:
				return
			default:
			}
			if tempDelay == 0 {
				tempDelay = 5 * time.Millisecond
			} else {
				tempDelay *= 2
			}
			if maxDelay := 1 * time.Second; tempDelay > maxDelay {
				tempDelay = maxDelay
			}
			logWarnf("[Server] accept error (retrying in %v): %v", tempDelay, err)
			time.Sleep(tempDelay)
			continue
		}
		tempDelay = 0
		c := &conn{nc: nc}
		met.connections.Add(1)
		logInfof("[Server] New connection from: %s", c.nc.RemoteAddr())
		s.mu.Lock()
		s.conns[c] = struct{}{}
		s.mu.Unlock()
		go s.serveConn(c)
	}
}

// dispatchRequest is the seam serveConn dispatches through. A var so a test can
// force a handler panic and prove the per-request recover keeps the daemon alive
// (the panic path is otherwise unreachable, so it cannot be provoked by input).
// Production never reassigns it.
var dispatchRequest = func(s *server, c *conn, raw []byte) *response { return s.dispatch(c, raw) }

func (s *server) serveConn(c *conn) {
	defer func() {
		c.wmu.Lock()
		c.closed = true
		c.wmu.Unlock()
		c.nc.Close()
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		s.procs.detachConn(c)
		logInfof("[Server] Connection closed: %s", c.nc.RemoteAddr())
	}()

	sc := bufio.NewScanner(c.nc)
	// The reference caps a single request line at 1 MiB (bufio maxTokenSize =
	// 1024*1024): a line up to 1048575 bytes is accepted, 1048576+ closes the
	// connection with no reply (probe-verified to the exact byte). Clients must
	// chunk large process.stdin payloads to stay under it.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		raw := make([]byte, len(line))
		copy(raw, line)
		// Peek at the method while still in the read loop, so a stdin request can
		// take its ticket in wire order before dispatch goes concurrent. A
		// malformed line yields an empty method and is simply not ordered — it has
		// no stdin payload to misplace.
		var peek struct {
			Method string      `json:"method"`
			ID     interface{} `json:"id"`
		}
		_ = json.Unmarshal(raw, &peek)
		ordered := peek.Method == "process.stdin"
		var ticket uint64
		if ordered {
			ticket = c.nextStdinTicket()
		}
		// The real daemon dispatches a connection's requests concurrently, so
		// responses can return out of order; match that.
		go s.handleRequest(c, raw, peek.Method, peek.ID, ordered, ticket)
	}

	// The read loop also ends on a scanner error — a request line over the 1 MiB
	// cap, or a read failure that is not a clean EOF — and that arm is otherwise
	// silent: no reply goes out and the connection just closes. Measured, the
	// reference emits the scanner error *before* its "Connection closed" line, so
	// the check goes at the end of this body: claustrum's own "Connection closed"
	// comes from the defer above, which runs last.
	//
	// isOurClose is excluded because that is OUR close, not a read failure:
	// closeAll drops every client connection on the graceful-shutdown path, and
	// counting that as an error made claustrum log one ERROR line per connected
	// client where the reference's log carries none (measured, both binaries, on
	// a server.shutdown with two connections open). sc.Err() is nil on a clean
	// EOF, so an ordinary client disconnect stays quiet on both. The predicate is
	// per-OS because the -listen-pipe transport's conns are served by this same
	// serveConn but do not report a close as net.ErrClosed.
	if err := sc.Err(); err != nil && !isOurClose(err) {
		logErrorf("[Server] scanner error on %s: %v", c.nc.RemoteAddr(), err)
	}
}

// handleRequest runs one connection request in its own goroutine: the
// per-request panic recovery and, for process.stdin, the ordering ticket.
// Extracted from serveConn's read loop so the loop reads as peek -> order ->
// hand off; behavior is unchanged.
func (s *server) handleRequest(c *conn, raw []byte, method string, id interface{}, ordered bool, ticket uint64) {
	// Per-request panic isolation: without this a panic in any handler
	// crashes the whole daemon (an unrecovered panic in ANY goroutine takes
	// the process down), orphaning managed children and leaving a stale
	// socket. Surviving the request is strictly better than dying on it.
	//
	// THIS FRAME IS CLAUSTRUM'S OWN, and it is not a parity claim. No input
	// is known to reach a handler panic — two fuzz waves found none, and
	// claustrum's own panic sites are each either an unreachable stdlib
	// timer guard or an already-bounds-guarded slice — so the path is
	// unreachable and the frame is unobservable on the wire. -32603 is
	// codeInternal, the JSON-RPC 2.0 standard "Internal error"; the message
	// and log line follow claustrum's own conventions. Nothing here can be
	// probe-verified, and nothing here needs to be: an unreachable frame
	// cannot diverge from anything. See docs/IMPROVEMENTS.md.
	// The stdin ticket covers DISPATCH ONLY, never the response write.
	// dispatch appends the chunk to the process's existing stdin FIFO
	// (applyStdin) before it returns, so byte order is already fixed by
	// then. Holding the ticket across writeResponse would add nothing to
	// ordering and would couple input admission to response delivery: a
	// client that stops reading blocks the socket write, and every later
	// process.stdin on the connection stalls behind it — including input
	// for OTHER processes.
	//
	// The inner func is what keeps the release on a defer, so a panic in
	// dispatch cannot strand the ticket and deadlock the connection.
	var resp *response
	func() {
		// Registered FIRST, so it unwinds LAST — after doneStdinTurn below,
		// which means a panicking dispatch cannot strand a stdin ticket and
		// deadlock the connection.
		//
		// Scoped to dispatch on purpose. Covering the response write too
		// would let a panic *inside* writeResponse produce a SECOND frame
		// for one id — the one failure mode a wire-contract daemon least
		// wants from its safety net. The recover only sets resp; the single
		// write stays below, outside the recovered region.
		defer func() {
			r := recover()
			if r == nil {
				return
			}
			logErrorf("[Server] recovered panic: method=%s id=%v: %v", method, idForLog(id), r)
			// The stack goes to DEBUG so the ERROR line keeps its shape while
			// the operator can still get the fault location — a recovered
			// panic here means an unreachable path became reachable, and
			// method+id locate the request but not the fault.
			logDebugf("[Server] recovered panic stack: method=%s id=%v\n%s", method, idForLog(id), debug.Stack())
			// server.shutdown must produce NO reply (dispatch returns nil for
			// it). Replying here would emit a frame where the daemon otherwise
			// emits none, so the no-reply contract holds even under a panic.
			if method == methodShutdown {
				return
			}
			resp = ptr(errResult(id, codeInternal, fmt.Sprintf("recovered panic: %v", r)))
		}()
		if ordered {
			c.awaitStdinTurn(ticket)
			defer c.doneStdinTurn(ticket)
		}
		resp = dispatchRequest(s, c, raw)
	}()
	if resp != nil {
		c.writeResponse(*resp)
	}
}

// signalShutdown requests a graceful stop exactly once.
func (s *server) signalShutdown() { s.once.Do(func() { close(s.shutdown) }) }

// teardown closes the listener, stops child processes (per -keep-children), drops
// clients, and exits.
func (s *server) teardown(socket string) {
	s.closeAll(socket)
	osExit(0)
}

// closeAll releases every daemon-held resource on the graceful-shutdown path:
// listeners, the socket + daemon.token + rpc.pipe files, child processes (per
// -keep-children), and connected clients. Split out of teardown (same pattern
// as stopChildren) so the whole sequence is testable without the process exit.
func (s *server) closeAll(socket string) {
	s.ln.Close()
	if s.metricsLn != nil {
		_ = s.metricsLn.Close()
	}
	if s.pipeLn != nil {
		_ = s.pipeLn.Close()
	}
	_ = os.Remove(socket)
	removePersistedToken(socket)
	// Remove rpc.pipe on the same graceful path as rpc.sock/daemon.token. No-op if
	// the pipe was never started (unconditional, matching removePersistedToken).
	removePipeNameFile(socket)
	s.stopChildren()
	s.procs.close() // end the prune sweep; idempotent
	s.mu.Lock()
	for c := range s.conns {
		c.nc.Close()
	}
	s.mu.Unlock()
}

// stopChildren implements the -keep-children policy on graceful shutdown. By
// default it kills the whole child tree (matching the reference). With
// -keep-children set (POSIX only — gated at startup by honorKeepChildren), it
// instead leaves every running child alive so they survive a daemon
// restart/upgrade, logging one honest line with the surviving count. The new
// daemon does not re-adopt them; an out-of-band consumer reconciles them via the
// CT-1 pid/startTime. Split out from teardown so the gate is unit-testable
// without teardown's os.Exit.
func (s *server) stopChildren() {
	if s.keepChildren {
		logInfof("[Server] -keep-children: leaving %d running child process(es) alive across shutdown", s.procs.runningCount())
		return
	}
	s.procs.killAll()
}
