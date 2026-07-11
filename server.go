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
}

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
	// -serve requires a token source, checked BEFORE the socket (probe-verified).
	// The CLAUDE_RPC_TOKEN env is NOT accepted here: the daemon's token always
	// comes from a file (read once, then unlinked) or an fd (read by the parent,
	// forwarded over a pipe), so it never lingers in /proc/<pid>/environ. (env is
	// only for the -bridge/-stop clients.)
	if tokenFile == "" && tokenFd < 0 {
		fmt.Fprintln(os.Stderr, "claustrum: daemonized child requires --token-file or --token-fd")
		os.Exit(1)
	}
	if os.Getenv(daemonChildEnv) != "1" {
		// Parent. An fd is only valid in this process and would not survive the
		// re-exec, so read it now and forward the token to the child over an
		// inherited pipe — never via disk, argv, or environ. The -token-file path
		// is unchanged: the file persists across the re-exec for the child to read.
		if tokenFd >= 0 {
			token, err := readTokenFD(tokenFd)
			if err != nil {
				fmt.Fprintf(os.Stderr, "claustrum: read --token-fd: %v\n", err)
				os.Exit(1)
			}
			daemonizeWithToken(token)
			return
		}
		daemonizeWithToken("")
		return
	}

	// We are the detached child. The token arrives either over the forwarded pipe
	// (-token-fd path) or from --token-file (read once, then unlinked).
	var token string
	if fdStr := os.Getenv(tokenPipeEnv); fdStr != "" {
		fd, err := strconv.Atoi(fdStr)
		if err == nil {
			token, err = readTokenFD(fd)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "claustrum: read token pipe: %v\n", err)
			os.Exit(1)
		}
		_ = os.Unsetenv(tokenPipeEnv)
	} else {
		b, err := os.ReadFile(tokenFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "claustrum: read --token-file: %v\n", err)
			os.Exit(1)
		}
		token = normalizeToken(b)
		_ = os.Remove(tokenFile)
	}
	_ = os.Unsetenv("CLAUDE_RPC_TOKEN") // prevent token propagation through daemonize → os.Environ()
	// Drop our internal re-exec sentinel now that we have consumed it: buildEnv
	// bases process.spawn children on os.Environ(), and the reference daemon's
	// environ has no CLAUSTRUM_DAEMON_CHILD — leaking it would be a (detectable)
	// divergence. The reference-parity marker (daemonChildMarker) stays set.
	_ = os.Unsetenv(daemonChildEnv)

	// Extract a real interactive PATH from the login shell so spawned children
	// resolve tools the way an interactive session would. Run in a goroutine so
	// a stalling login shell does not delay the daemon socket opening (matches
	// reference binary behavior; extractLoginPATH has its own internal timeout).
	go extractLoginPATH()

	_ = os.Remove(socket) // clear a stale socket
	ln, err := net.Listen("unix", socket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "claustrum: listen unix: %v\n", err)
		os.Exit(1)
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

	fmt.Printf("Claustrum remote server listening on %s\n", socket)
	s.run(socket)
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

// daemonizeWithToken re-execs the binary detached from the terminal. If
// forwardToken is non-empty (the -token-fd path) it is handed to the child over
// an inherited pipe (child fd 3, named by tokenPipeEnv) so the token never lands
// on disk, in argv, or in the environment; an empty forwardToken means the child
// reads its own -token-file as before.
func daemonizeWithToken(forwardToken string) {
	self, err := os.Executable()
	if err != nil {
		self = os.Args[0]
	}
	cmd := exec.Command(self, os.Args[1:]...)
	// daemonChildEnv is our private re-exec sentinel (detected in runServe);
	// daemonChildMarker is set only to mirror the reference daemon's environ so
	// it propagates into process.spawn children (TestSpawnInheritsDaemonChildMarker).
	cmd.Env = append(os.Environ(), daemonChildEnv+"=1", daemonChildMarker+"=1")
	// Detach from the controlling terminal/session (OS-specific); inherit stdio so
	// a wrapping `> serve.out` redirect still captures the daemon's startup line.
	cmd.SysProcAttr = detachSysProcAttr()
	cmd.Stdin = nil
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	var pipeW *os.File
	if forwardToken != "" {
		pr, pw, err := os.Pipe()
		if err != nil {
			fmt.Fprintf(os.Stderr, "daemonize: token pipe: %v\n", err)
			os.Exit(1)
		}
		pipeW = pw
		// ExtraFiles[0] becomes fd 3 in the child; tell it where to read.
		cmd.ExtraFiles = []*os.File{pr}
		cmd.Env = append(cmd.Env, tokenPipeEnv+"=3")
		defer pr.Close() // parent's copy of the read end; the child has its own
	}

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "daemonize: %v\n", err)
		os.Exit(1)
	}
	if pipeW != nil {
		_, _ = io.WriteString(pipeW, forwardToken)
		_ = pipeW.Close() // closing the write end gives the child's read an EOF
	}
	_ = cmd.Process.Release()
	os.Exit(0)
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
		// The real daemon dispatches a connection's requests concurrently, so
		// responses can return out of order; match that.
		go func() {
			if resp := s.dispatch(c, raw); resp != nil {
				c.writeResponse(*resp)
			}
		}()
	}
}

// signalShutdown requests a graceful stop exactly once.
func (s *server) signalShutdown() { s.once.Do(func() { close(s.shutdown) }) }

// teardown closes the listener, stops child processes (per -keep-children), drops
// clients, and exits.
func (s *server) teardown(socket string) {
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
	s.mu.Lock()
	for c := range s.conns {
		c.nc.Close()
	}
	s.mu.Unlock()
	os.Exit(0)
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
