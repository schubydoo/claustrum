package main

import (
	"bufio"
	"errors"
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
	"sync/atomic"
	"syscall"
	"time"

	"github.com/schubydoo/claustrum/internal/conn"
	"github.com/schubydoo/claustrum/internal/files"
	"github.com/schubydoo/claustrum/internal/git"
	"github.com/schubydoo/claustrum/internal/hostsweep"
	"github.com/schubydoo/claustrum/internal/logging"
	"github.com/schubydoo/claustrum/internal/metrics"
	"github.com/schubydoo/claustrum/internal/rpc"
	"github.com/schubydoo/claustrum/internal/runlock"
	"github.com/schubydoo/claustrum/internal/sessiondirs"
	"github.com/schubydoo/claustrum/internal/shellenv"
	"github.com/schubydoo/claustrum/internal/sockroute"
	"github.com/schubydoo/claustrum/internal/supervisor"
	"github.com/schubydoo/claustrum/internal/tokenfile"
	"github.com/schubydoo/claustrum/internal/wirelog"
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
// daemonizeWithToken. See docs/protocol/flags.md → "The re-exec marker".
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
	procs *supervisor.Manager

	// socket is the path that ln was bound for. A long path is bound by a shorter
	// name (sockroute.Listen), so ln.Addr() can differ from it.
	socket string

	// instanceID is a per-boot 32-hex identity and startedAt is the boot time in
	// unix milliseconds. Both are stamped once at construction and echoed on the
	// server.capabilities reply (instanceId / startedAt), matching the reference
	// daemon since 4534d86, which emits both fields there. claustrum generates
	// instanceID at startup and advertises both for parity.
	instanceID string
	startedAt  int64

	mu           sync.Mutex
	conns        map[*conn.Conn]struct{}
	stopsGone    int // connections that sent server.shutdown and left conns, for the cleanup count
	shutdown     chan struct{}
	once         sync.Once
	dropOnce     sync.Once          // makes dropping, on first use
	dropping     chan struct{}      // closed when dropConns starts (see dropStarted)
	replyRule    *shutdownReplyRule // test seam: nil uses shutdownReplyAwaitsDrop and shutdownReplyWait
	metricsLn    net.Listener       // optional Prometheus listener; nil unless -metrics-addr set
	pipeLn       net.Listener       // optional Windows named-pipe listener; nil unless -listen-pipe set (Windows-only)
	keepChildren bool               // -keep-children: leave child processes running on graceful shutdown

	wlog    *wirelog.Log  // optional JSON-RPC frame recorder; nil unless -wire-log set
	connSeq atomic.Uint64 // per-daemon connection counter, only to correlate wire-log records

	// The per-daemon counters of the sampled log lines (logSampled). Each line kind
	// has its own counter, and none resets while the daemon runs.
	unauthLines atomic.Uint64 // refused requests (Unauthorized request number)
	parseLines  atomic.Uint64 // lines that are not request JSON (Parse error number)
	tokenlessCl atomic.Uint64 // closes of a connection that never showed the token

	// lastCounted is monoNow at the last counted request, or at the start of the
	// daemon. The idle exit reads it (orphanexit.go).
	lastCounted atomic.Int64

	// offGitRequests counts the git requests with "shadowDirOff":true that have
	// not answered yet (gitWideLimit and gitCall.offLive).
	offGitRequests atomic.Int32

	// managedLog holds the last [LauncherHandler] line, so a repeat of the same
	// launcher.resolve answer writes no second line (see managedLauncherLog).
	managedLog managedLauncherLog

	// idleTimeout closes a connection with no read/write activity for this long.
	// The reference closes a connection idle for 5 minutes (conn.IdleConnTimeout),
	// measured against f6010b97. So this is always-on to match. A test overrides
	// it to a short value.
	idleTimeout time.Duration

	// sockInfo / tokenInfo / pipeInfo are the identities of the socket, daemon.token
	// and rpc.pipe files this daemon published, so teardown unlinks each only if it is
	// still the inode this daemon wrote (unlinkOwnSocket / tokenfile.DropOwn /
	// removePipeNameFileIfOwned) — a restart's successor may have republished any of
	// them to a new inode, and deleting a successor's file would break its handoff.
	// tokenInfo/pipeInfo are nil when nothing was published (persist failed / no pipe).
	sockInfo  os.FileInfo
	tokenInfo os.FileInfo
	pipeInfo  os.FileInfo

	// releaseRunDir drops the run-dir lock (daemon.lock) on graceful shutdown if
	// this daemon holds it: it truncates the owner record and unlocks the fd,
	// leaving the file in place.
	// A no-op on Windows (which has no run-dir lock). See internal/runlock.
	releaseRunDir func()
	// stopLateLock ends the late tries of the run-dir lock when the shutdown begins
	// (signalShutdown). A no-op unless the daemon started without the lock while
	// another process held it (Linux and macOS, see internal/runlock).
	stopLateLock func()

	// gitShadow is the settings control of the git methods: the state that git.info
	// reports, the folder of this daemon beside the socket and the start test
	// (internal/git/gitshadow.go). It is nil for a server with no run folder.
	gitShadow *git.ShadowHost

	// peerCheck is the peer check of a daemon that started with
	// CLAUDE_SSH_PEER_CHECK=1, and nil otherwise. It sets the peerCheck members of
	// server.capabilities and checks each accepted connection (see peercheck.go).
	peerCheck *peerGate
}

// runServe self-daemonizes (reparenting to init) then runs the RPC server. The
// child is marked with daemonChildEnv (CLAUSTRUM_DAEMON_CHILD) so we re-exec
// exactly once — see that const for why it is not CLAUDE_SSH_DAEMON_CHILD.
func runServe(socket, tokenFile string, tokenFd int, metricsAddr string, wlopt wirelog.Options, keepChildren, listenPipe bool) {
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
	// So on the reference the operator sees the launcher's accept timeout, and
	// the real reason is only in the child's own log. claustrum answered 300x faster and
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

	// Set this daemon's RLIMIT_NOFILE soft limit to min(hard, 65536) so process.spawn
	// children inherit a high open-file limit, matching reference build 19f30c46. (The
	// Go runtime pre-raises the daemon's own soft limit near the hard limit at startup,
	// so for a hard limit above 65536 this lowers the daemon's own soft limit; the point
	// is the value the child inherits.) syscall.Setrlimit for RLIMIT_NOFILE also updates the
	// value the Go runtime restores across exec, so the raised limit reaches the
	// exec'd child. Unix only (Windows has no RLIMIT_NOFILE) and a no-op when the
	// limit cannot be read or raised. Kept in this shell — not newServerOnSocket — so
	// in-process tests never mutate the test process's own limit.
	raiseInheritedFileLimit()

	// The handler for SIGTERM and SIGINT is in place before the claim of the run dir,
	// so a signal during the start does not end the start (see signalWatch).
	sigc := make(chan os.Signal, 1)
	notifySignals(sigc, syscall.SIGTERM, syscall.SIGINT)
	watch := watchSignals(sigc, keepChildren)

	s, err := newServerOnSocket(socket, token, metricsAddr, wlopt, keepChildren, listenPipe)
	if err != nil {
		fmt.Fprintf(os.Stderr, "claustrum: %v\n", err)
		osExit(1)
	}
	// A signal that came during the start requests the shutdown here, before the
	// listening line.
	watch.attach(s)

	fmt.Print(s.listeningLine(socket))
	// Arm the read of the login-shell PATH, so spawned children resolve tools the
	// way an interactive session does, and start it in the background unless
	// CLAUDE_SSH_NO_LOGIN_PATH_WARM is 1 (internal/shellenv/shellenv.go). Measured against 52d82f0a: the
	// read starts with no spawn on Linux and macOS, 9 to 28 ms after the start line on
	// Linux. It comes after the listening line, so a start that fails before it leaves no shell. Kept
	// in this shell, not in newServerOnSocket, so a test that boots a server never
	// forks a login shell.
	shellenv.WarmAtStart()
	// Start the host cleaner. It is a background sweep that ends orphaned Claude Code process groups under this
	// install's roots. It also retires abandoned daemons and tidies their stale run dirs. It runs
	// unconditionally at startup, right after the listening line. It stays off, with one line, when the socket
	// is not a deployed run-dir socket. It is a no-op off linux and darwin (see internal/hostsweep). DESTRUCTIVE
	// and host-wide: validated only on a throwaway test system, never on a host that runs siblings. Kept in
	// this shell, not in newServerOnSocket, so an in-process test boot starts no cleaner.
	hostsweep.Start(socket)
	// The start test of files.read real, once and before the first connection line.
	// Its place beside the banner and the cleaner line is claustrum's own, not measured.
	files.LogReadRealStart()
	// The session folders: the record folder beside run, with the records of
	// earlier daemons (internal/sessiondirs). It comes before the first request. Its
	// place after the lines above is claustrum's own, not measured. Kept in this
	// shell, so an in-process test boot keeps its own store.
	sessiondirs.Open(socket)
	s.run(socket)
}

// listeningLine is the line the daemon prints on stdout when it is ready. It has no time and
// no level tag. The suffix with the pid and the instance is the reference's, measured on
// Linux, macOS and Windows against f6010b97 and 89cb6289. The instance is the instanceId of
// server.capabilities. The name "Claustrum" is claustrum's own: the reference prints
// "Claude" there.
func (s *server) listeningLine(socket string) string {
	return fmt.Sprintf("Claustrum remote server listening on %s (pid %d, instance %s)\n", socket, os.Getpid(), s.instanceID)
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
			logging.Warnf("[daemon] failed to remove --token-file %s: %v", tokenFile, err)
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
	// Drop our internal re-exec sentinel now that we have consumed it: supervisor.BuildEnv
	// bases process.spawn children on os.Environ(), and the reference daemon's
	// environ has no CLAUSTRUM_DAEMON_CHILD — leaking it would be a (detectable)
	// divergence. The reference-parity marker (daemonChildMarker) stays set.
	_ = os.Unsetenv(daemonChildEnv)
	return token, nil
}

// chmodSocket is os.Chmod behind a seam over the 0600 tightening of the socket
// newServerOnSocket has just bound: a fresh AF_UNIX node owned by the caller does
// not refuse chmod; without the seam the failure arm (socketChmodFailed) fires
// only for a Linux abstract socket (-socket @name), which has no filesystem node
// to chmod and is not a shape Desktop passes.
// Distinct from chmodStaged in internal/install/install.go, which seams the staged CLI binary.
var chmodSocket = os.Chmod

// newServerOnSocket performs the daemonized child's startup in its exact
// order: claim the run dir, reap the children of a dead predecessor, clear the
// stale socket, listen, chmod, complete the owner record, persist the token, gate
// the Windows-only flag, construct the server, start the optional
// metrics and pipe listeners. It hands back the ready-to-run server. Split
// out of runServe (same pattern as enablePipe/startAcceptLoops) so the whole
// boot sequence is testable without the daemonize/osExit shell.
func newServerOnSocket(socket, token, metricsAddr string, wlopt wirelog.Options, keepChildren, listenPipe bool) (*server, error) {
	// Opened before the listener so a bad -wire-log path fails the boot rather
	// than producing a daemon that serves happily and records nothing — an empty
	// capture reads as "the client sent nothing", the worst failure for a
	// diagnostic tool.
	wlog, err := wirelog.Open(wlopt)
	if err != nil {
		return nil, err
	}
	// Close the capture if boot fails after this point. The daemon exits on any
	// such error, but the in-process boot tests — and any future caller that
	// recovers from a boot failure — would otherwise leak the open file. Close is
	// nil-safe, so this is a no-op when logging is off.
	ok := false
	// The order of a start is: claim the run dir, reap, bind, complete the owner
	// record, persist the token. Measured on Linux and macOS against 89cb6289. On
	// Linux the order of the calls is the open of daemon.lock, the signals of the
	// reap, the unlink of the old socket file, bind and listen (measured with
	// strace). The strace logs hold no write to the lock. The place of the last step
	// comes from the startedAt of the full record (see below).
	//
	// The claim evicts a prior live sibling serve daemon, so a restart
	// deterministically replaces it. Best-effort: runlock.LockRunFolder never fails the boot,
	// only logs and serves without ownership. It writes the short owner record. On
	// Windows it takes no lock and prints one line (see internal/runlock).
	instanceID := newDaemonInstanceID()
	claim := runlock.LockRunFolder(socket, "serve")
	procs := supervisor.NewManager()
	defer func() {
		if !ok {
			wlog.Close()
			claim.Release()
			procs.Close()
		}
	}()
	// The run dir (CLAUDE_SSH_RUN_DIR) is the folder of the socket path, on every
	// socket shape. spawn launches children through the exec-child trampoline (see
	// internal/supervisor/execchild.go).
	procs.RunDir = supervisor.ExecChildRunDir(socket)
	// Hold the run dir open from here on, so a child record follows the folder
	// through a rename (linux and darwin, a no-op on windows).
	procs.HoldRunDir()
	// The daemon instance id, stamped into each child's orphan-registry record so a
	// later daemon can tell this daemon's children from its own.
	procs.InstanceID = instanceID
	// Reap children a since-exited predecessor daemon of this run dir left orphaned. The
	// run-dir claim above evicts a live predecessor (unless eviction is refused), so its
	// recorded children become candidates. ReapOrphans independently re-checks that each
	// owning daemon is gone before it touches a child. Synchronous, and BEFORE the bind:
	// while the reap waits for a group to end, the old socket file stays as it is and
	// nothing listens. A client that connects in that wait gets ECONNREFUSED from a
	// stale socket file, or ENOENT when there is none (measured: the first connection
	// worked 2.2 to 2.4 s after the start, for a child that ignores SIGTERM). A no-op
	// off linux and darwin (see internal/childrec/childreap_*.go).
	//
	// A daemon that does not hold the run-dir lock runs no reap and writes no child
	// record. A measurement shows that for a held flock (`5fd08069`, measured on
	// Linux).
	procs.NoRecords = !claim.Holder
	if claim.Holder {
		procs.ReapOrphans()
	}

	_ = os.Remove(socket) // clear a stale socket
	ln, err := sockroute.Listen(socket)
	if err != nil {
		return nil, fmt.Errorf("listen unix: %v", err)
	}
	sockroute.KeepSocketFileAtClose(ln)
	// The identity of the socket that this start bound. It is nil only when the stat
	// itself failed, and unlinkOwnSocket then removes the path with no test.
	bound, _ := os.Stat(socket)
	if err := chmodSocket(socket, 0o600); err != nil {
		// On Linux and macOS a failed chmod ends the start. On Windows the start
		// goes on (see socketChmodFailed).
		if err := socketChmodFailed(ln, socket, bound, err); err != nil {
			return nil, err
		}
	}
	// The owner record gets its instanceId and startedAt now. Both are the values that
	// server.capabilities answers, and the instance is the one of the listening line
	// and of the child records (measured: the four instance values are equal, and the
	// startedAt of the lock equals the one of server.capabilities). startedAt is read
	// right after the bind. Measured on Linux against 89cb6289 (8 of 8 runs): the
	// startedAt of the lock was within 1.5 ms of the listen call.
	startedAt := time.Now().UnixMilli()
	claim.Complete(instanceID, startedAt)

	// Persist the token beside the now-listenable socket so a client can
	// reconnect to this daemon and re-authenticate after the original -token-file
	// was unlinked / the -token-fd pipe closed. Removed again in teardown, but only if
	// still our inode (tokenInfo). Matches the reference daemon (upstream 5db5e4a);
	// best-effort and non-fatal.
	tokenFI := tokenfile.Save(socket, token)

	// -listen-pipe is Windows-only. honorListenPipe returns the flag unchanged on
	// Windows and false-with-a-warning elsewhere (the named-pipe transport has no
	// meaning off Windows), evaluated
	// in the daemonized child so any warning logs once from the running daemon.
	listenPipe = honorListenPipe(listenPipe)

	sockFI, _ := os.Stat(socket) // identity of the inode we just bound (for unlinkOwnSocket)
	// The job mode of the children, with its one line on Windows, before the peer-check
	// line and the listening line. CLAUDE_SSH_NO_JOBS leaves the daemon environment here
	// on every system (see internal/supervisor/childjobs.go). With -keep-children a job does not end its
	// child when the daemon dies.
	procs.Jobs = supervisor.StartChildJobs(keepChildren)
	// The one line of a start with CLAUDE_SSH_PEER_CHECK=1 comes here, before the
	// listening line. The daemon also removes the variable from its own environment
	// here, before it serves a request (see startPeerCheck).
	gate := startPeerCheck()
	s := &server{
		token:         token,
		ln:            ln,
		socket:        socket,
		procs:         procs,
		conns:         make(map[*conn.Conn]struct{}),
		shutdown:      make(chan struct{}),
		keepChildren:  keepChildren,
		wlog:          wlog,
		idleTimeout:   conn.IdleConnTimeout,
		sockInfo:      sockFI,
		tokenInfo:     tokenFI,
		releaseRunDir: claim.Release,
		stopLateLock:  claim.StopLate,
		instanceID:    instanceID,
		startedAt:     startedAt,
		gitShadow:     git.NewShadowHost(supervisor.ExecChildRunDir(socket)),

		peerCheck: gate,
	}
	s.noteCountedRequest()
	// Optional Prometheus metrics endpoint (opt-in via -metrics-addr). A bind
	// failure is non-fatal — the daemon's job is the socket, not the metrics.
	if metricsAddr != "" {
		if ln, err := metrics.StartServer(metricsAddr); err != nil {
			logging.Errorf("[Server] metrics: listen %s: %v", metricsAddr, err)
		} else {
			s.metricsLn = ln
			logging.Infof("[Server] metrics: serving Prometheus counters on %s/metrics", metricsAddr)
		}
	}

	// Optional additional Windows named-pipe listener (opt-in via -listen-pipe).
	s.enablePipe(socket, listenPipe)
	ok = true // boot succeeded; keep the capture open (server owns it now)
	return s, nil
}

// normalizeToken mirrors the reference daemon's token-file handling: it reads the token as a line, so every trailing `\r`
// and `\n` from the SFTP-uploaded file is stripped (the probe covered one `\n` or `\r\n`), while spaces and other
// interior/surrounding whitespace are preserved verbatim (probe-verified). Without this, a token file that ends in a
// newline would make every client request fail auth even though the client sent the correct token.
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

// openDaemonLog opens remote-server.log beside the socket for the daemon child.
// An error fails the start (daemonizeWithToken). A nil file with a nil error, for an
// empty socket path or a Windows open that fails, keeps the stdio of the launcher.
//
// The prior log is renamed to remote-server.log.old first, and an error of that rename
// is ignored, as on 4534d86 and 5fd08069. The rename acts on a link itself. Then an
// exclusive create makes a fresh regular log, mode 0600 (measured on Linux and macOS).
//
// If the create fails, claustrum judges the entry at the path by Lstat, so nothing is
// opened first and a FIFO never blocks the launcher. Measurements show this on Linux
// and macOS:
//   - Nothing at the path fails the start with the error of the create, as on 5fd08069
//     (measured on Linux).
//   - A folder fails the start with "is a directory", as on 5fd08069
//     (measured).
//   - openHeldDaemonLog opens an own regular file and an own link to an own file
//     (measured).
//   - A FIFO and any other kind fail the start with "permission denied" (measured, D8).
//
// On Windows the rename moves a folder onto a regular .old, as on 5fd08069 (measured
// on Windows).
//
// The log is NOT removed on shutdown; unlike the socket and daemon.token it
// outlives the daemon, so a post-mortem is still readable.
func openDaemonLog(socket string) (*os.File, error) {
	if socket == "" {
		return nil, nil
	}
	path := filepath.Join(filepath.Dir(socket), daemonLogName)
	_ = os.Rename(path, path+".old") // rotate prior log to .old (parity)
	// daemonLogAppend is O_APPEND on unix and 0 on Windows (detach_*.go).
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL|daemonLogAppend, 0o600)
	if err == nil {
		return f, nil
	}
	createErr := err
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, createErr
	}
	switch {
	case fi.Mode().Type() == os.ModeDir:
		return nil, &os.PathError{Op: "open", Path: path, Err: syscall.EISDIR}
	case !fi.Mode().IsRegular() && fi.Mode().Type() != os.ModeSymlink:
		return nil, &os.PathError{Op: "open", Path: path, Err: syscall.EACCES}
	}
	return openHeldDaemonLog(path, fi)
}

// daemonizeWithToken re-execs the binary detached from the terminal. If forwardToken is
// non-empty (the -token-fd path) it is handed to the child over an inherited pipe (child
// fd 3, named by tokenPipeEnv), so this handoff writes the token to no disk, no argv, and
// no environment; an empty forwardToken means the child reads its own -token-file as
// before. daemonStartTimeout caps how long the -serve launcher waits for the daemonized
// child to start accepting before it returns anyway. It is 12 s. Measured on Linux against
// 89cb6289: with a zero-byte -token-file the daemon child ends before its bind, and the
// launcher exits 1 after 12.06 s in three forms (a stale socket file, no socket file, an
// empty run folder). macOS gives 12.08 to 12.16 s, and two Windows measurements give
// 12.058 s. 5db5e4a gave up after 10.06 s in the same case, and this value was 10 s until
// that row. var so tests can shrink it.
var daemonStartTimeout = 12 * time.Second

// startDaemonChild is cmd.Start behind a seam over the re-exec below: the
// launcher re-execs its own executable, which fails only on resource exhaustion
// or a binary unlinked since launch, neither of which a fixture can stage, so the
// "daemonize: <err>" + exit 1 arm is otherwise unreachable.
//
// The real start is OS-specific (startDetached). On Windows it starts the child outside
// the job of the launcher, with a second try inside the job when the job refuses that.
var startDaemonChild = startDetached

func daemonizeWithToken(socket, forwardToken string) {
	// Locate the own binary before the first write. On Linux a failure ends the
	// launcher (selfLocateFails). Elsewhere it re-execs os.Args[0].
	self, err := osExecutable()
	if err != nil {
		if selfLocateFails {
			fmt.Fprintf(os.Stderr, "claustrum: locate self: %v\n", err)
			osExit(1)
		}
		self = os.Args[0]
	}
	// Create the socket's directory, mode 0700, before anything opens a file in it.
	// Measured: with a missing socket directory the reference starts normally and
	// leaves d sub(700) / rpc.sock(600) / daemon.token(600) / remote-server.log(600)
	// behind, while claustrum refused to start at all.
	//
	// The error is not reported here: a failure surfaces at once as the error of the
	// log create below.
	if dir := filepath.Dir(socket); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o700)
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
	// "listening on …" banner to stdout and log lines to stderr. If only stderr
	// went to the file, the banner stays on the terminal and the log misses it.
	logFile, err := openDaemonLog(socket)
	if err != nil {
		// 5fd08069 prints "claude-ssh: open log: <err>" and exits 1 before any daemon
		// starts (measured).
		fmt.Fprintf(os.Stderr, "claustrum: open log: %v\n", err)
		osExit(1)
	}
	if logFile != nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		defer logFile.Close()
	} else {
		// No log for an empty socket path or a Windows open that fails: the child
		// keeps the stdio of the launcher.
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

	// The pid of a daemon that answers on the socket path now, before the child
	// starts. The wait below does not take a socket of that pid for the new one.
	predPID := livePredecessorPID(socket)
	if err := startDaemonChild(cmd); err != nil {
		fmt.Fprintf(os.Stderr, "daemonize: %v\n", err)
		osExit(1)
	}
	if pipeW != nil {
		_, _ = io.WriteString(pipeW, forwardToken)
		_ = pipeW.Close() // closing the write end gives the child's read an EOF
	}
	accepted, sawPredecessor := waitForDaemonAccept(socket, predPID)
	if !accepted {
		// The reference reports this and exits 1, measured with a zero-byte -token-file,
		// where its child refuses to start: "claude-ssh: timeout waiting for daemon to
		// accept on <socket>", exit 1, after the full deadline (12.06 s on 89cb6289,
		// measured on Linux). When a connect of the wait reached the predecessor, the text
		// is "daemon did not take over" (measured on Linux and macOS). Otherwise it is the
		// timeout text, also with a socket file that never accepted (measured on Linux and
		// macOS).
		if sawPredecessor {
			fmt.Fprintf(os.Stderr, "claustrum: daemon did not take over %s (predecessor still owns it)\n", socket)
		} else {
			fmt.Fprintf(os.Stderr, "claustrum: timeout waiting for daemon to accept on %s\n", socket)
		}
		osExit(1)
	}
	osExit(0)
}

// daemonStartPoll is the time between two connects of the launcher wait. 5fd08069
// made 234 to 240 connects in its 12 s wait (measured on Linux and macOS). var so
// tests can shrink it.
var daemonStartPoll = 50 * time.Millisecond

// daemonPeerPID is connPeerPID behind a seam, so a test can stage a peer pid that
// cannot be read.
var daemonPeerPID = connPeerPID

// waitForDaemonAccept waits for the daemonized child to accept, and reports whether
// it did. It connects to the socket path every daemonStartPoll until
// daemonStartTimeout. Only a connect that works ends the wait, and the connection is
// closed at once. On 5fd08069 the log of the daemon that answered holds a
// "New connection from: @" / "Connection closed: @" pair from the launcher. In
// claustrum the connection never shows the token, so it logs neither line and
// counts on the sampled close line (claustrum's own, not measured).
//
// A socket file that does not accept yet, a folder with content at the path and
// a stale socket file do not end the wait. On 5fd08069 a client connect right after the launcher
// exit worked when the listen was late, and the launcher timed out with a folder
// with a file at the path (measured on Linux).
//
// predPID is the pid of the daemon that answered before the child started, or 0.
// A connect to that pid does not end the wait, and sawPredecessor reports one. The
// test is the peer pid, not the identity of the socket file: on ext4 the new
// socket kept the inode of the old one in 40 of 40 starts of 5fd08069 (measured on
// Linux). With predPID 0, as on Windows, the first connect that works ends the
// wait. With predPID set, a connect whose peer pid cannot be read does not end it:
// claustrum's own, not measured. One case is a peer that closed at once on macOS,
// which may give no pid (not measured).
func waitForDaemonAccept(socket string, predPID int) (accepted, sawPredecessor bool) {
	deadline := time.Now().Add(daemonStartTimeout)
	for {
		c, err := sockroute.DialUnixPath(socket, max(time.Until(deadline), time.Millisecond))
		exitOnChangeBack(err)
		if err == nil {
			pid := daemonPeerPID(c)
			_ = c.Close()
			if predPID == 0 || (pid != 0 && pid != predPID) {
				return true, sawPredecessor
			}
			if pid == predPID {
				sawPredecessor = true
			}
		}
		if !time.Now().Before(deadline) {
			return false, sawPredecessor
		}
		time.Sleep(daemonStartPoll)
	}
}

// exitOnChangeBack ends a launcher whose connect could not change back to the
// working folder, with exit 1 (claustrum's own, not measured). Other errors pass.
func exitOnChangeBack(err error) {
	var back *sockroute.ChangeBackError
	if errors.As(err, &back) {
		fmt.Fprintf(os.Stderr, "claustrum: %v\n", err)
		osExit(1)
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
// At each -serve start it clears a stale rpc.pipe, as the stale-socket clear
// (os.Remove(socket)) does for the socket. The pipe name is random for each boot,
// so a leftover rpc.pipe from an unclean exit names a pipe that no longer exists.
// When this boot serves no pipe (flag off, non-Windows, or startPipeTransport
// failed), enablePipe removes any such file. When it serves one,
// startPipeTransport has already written the new name. After an unclean exit, or
// a graceful exit that could not record the identity of rpc.pipe, a client can
// still read a stale rpc.pipe and dial a dead pipe until the next start.
func (s *server) enablePipe(socket string, listenPipe bool) {
	if !listenPipe {
		removePipeNameFile(socket) // not serving a pipe this boot → no stale rpc.pipe
		return
	}
	pln, err := startPipeTransport(socket)
	if err != nil {
		logging.Errorf("[Server] named-pipe transport: %v", err)
		// startPipeTransport writes rpc.pipe only on success, so any file present now
		// is stale from a prior boot — clear it rather than leave a dead pointer.
		removePipeNameFile(socket)
		return
	}
	s.pipeLn = pln
	// Record rpc.pipe's identity so teardown removes it only if it is still ours
	// (removePipeNameFileIfOwned), like the socket and daemon.token.
	s.pipeInfo = pipeNameFileIdentity(socket)
	logging.Infof("[Server] also listening on named pipe %s", pln.Addr())
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
	s.startAcceptLoops()
	// Retire this daemon if its socket path is taken over by a successor and no client
	// is connected (matching 4534d86), or after the idle limit with no client. Exits
	// with the daemon's shutdown. A no-op on Windows, and unless the durations,
	// sockInfo, and instanceId are all set. See orphanexit.go.
	go s.exitWhenOrphaned(socket)

	// Block here until shutdown, then tear down synchronously on the main
	// goroutine. teardown closes the listener (unblocking acceptLoop's Accept), and
	// closeAll has the order of the steps after that. Running it inline — not
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
			logging.Warnf("[Server] accept error (retrying in %v): %v", tempDelay, err)
			time.Sleep(tempDelay)
			continue
		}
		tempDelay = 0
		// The peer check runs before any read, log line, count or tracking of the
		// connection (measured on Linux).
		if !s.peerCheck.admit(nc) {
			continue
		}
		// Wrap in an activity-stamping conn so the idle watcher can see the last
		// read/write. The reference closes a connection idle for 5 minutes (f6010b97).
		ac := conn.NewActivityConn(nc)
		c := conn.New(ac, ac, s.connSeq.Add(1), s.wlog, make(chan struct{}), s.shutdown)
		metrics.Met.Connections.Add(1)
		// The New connection line waits for the first request that shows the token
		// (conn.Conn.ShowToken, claustrum's own, not measured).
		s.mu.Lock()
		s.conns[c] = struct{}{}
		s.mu.Unlock()
		go s.closeWhenIdle(ac, c.Done())
		go s.serveConn(c)
	}
}

// spokeReader notes that a read returned a byte. serveConn reads and checks it
// in one goroutine, so it needs no lock.
type spokeReader struct {
	r    io.Reader
	sent bool
}

func (p *spokeReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.sent = true
	}
	return n, err
}

// closeWhenIdle runs the idle watcher of one connection with the idle timeout and
// the shutdown channel of the server (conn.CloseWhenIdle).
func (s *server) closeWhenIdle(a *conn.ActivityConn, done <-chan struct{}) {
	conn.CloseWhenIdle(a, s.idleTimeout, s.shutdown, done)
}

// dispatchRequest is the seam serveConn dispatches through. A var so a test can
// force a handler panic and prove the per-request recover keeps the daemon alive
// (the panic path is otherwise unreachable, so it cannot be provoked by input).
// Production never reassigns it.
var dispatchRequest = func(s *server, c *conn.Conn, raw []byte, req request) *rpc.Response { return s.route(c, req) }

func (s *server) serveConn(c *conn.Conn) {
	var spoke spokeReader
	defer func() {
		// Logged before the close, as the reference does (docs/PROTOCOL.md, Transport).
		// Claustrum's own: a connection that never showed the token
		// logs no close line. Its close counts on the sampled close line instead,
		// but only when it sent a byte (measured on Linux, 683cc6a9).
		if c.TokenMark().Load() {
			logging.Infof(fmtConnClosed, c.RemoteLabel())
		} else if spoke.sent {
			if n := s.tokenlessCl.Add(1); logSampled(n) {
				logging.Infof(fmtTokenless, n, c.RemoteLabel())
			}
		}
		c.MarkClosed()
		c.NC().Close()
		if c.Done() != nil {
			close(c.Done()) // stop the idle watcher (nil when a test builds a conn directly)
		}
		s.mu.Lock()
		if _, ok := s.conns[c]; ok && c.SentStop() {
			s.stopsGone++
		}
		delete(s.conns, c)
		s.mu.Unlock()
		s.procs.DetachConn(c)
	}()

	spoke.r = c.NC()
	sc := bufio.NewScanner(&spoke)
	// The reference caps a single request line at 1 MiB: a line up to 1048575
	// bytes is accepted, 1048576+ closes the connection with no reply
	// (probe-verified to the exact byte). Clients must chunk large process.stdin
	// payloads to stay under it.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		raw := make([]byte, len(line))
		copy(raw, line)
		// Record inbound here, in the read loop, so the capture preserves WIRE
		// ORDER. Recording inside handleRequest would order frames by goroutine
		// scheduling instead, which is exactly the property a capture is used to
		// study (see the stdin-ticket note above).
		c.WireLog().Record(c.ID(), "in", raw)
		// A line that fails the parse check or the auth check gets its reply
		// here, on the read path, before the next read. A final newline does
		// not change this. Measured against f6010b97 and 90fca6e6 on Linux,
		// the reference wrote these two error classes before it closed at a
		// client half-close. It lost the reply for every later class there.
		// Here every later class still goes to the concurrent dispatch below.
		// At the idle close the connection is already closed, so this write
		// fails and puts 0 bytes on the wire.
		req, resp := s.gate(c, raw)
		if resp != nil {
			// A failed write of this reply ends the read loop and so the
			// connection. Measured on Linux with c8e2f083. A failed write of
			// any other reply, which handleRequest writes, keeps it.
			if c.WriteResponse(*resp) {
				break
			}
			continue
		}
		// Read the method here in the read loop, so a stdin request can take
		// its ticket in wire order before dispatch goes concurrent.
		ordered := req.Method == "process.stdin"
		var ticket uint64
		if ordered {
			ticket = c.NextStdinTicket()
		}
		// The real daemon dispatches a connection's requests concurrently, so
		// responses can return out of order; match that.
		go s.handleRequest(c, raw, req, ordered, ticket)
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
	//
	// Claustrum's own, not measured: the address and the error are quoted, and no
	// line is logged for a connection that never showed the token or during the shutdown.
	if err := sc.Err(); err != nil && !isOurClose(err) && c.TokenMark().Load() && !c.ShuttingDown() {
		logging.Errorf(fmtScannerErr, c.RemoteLabel(), err)
	}
}

// handleRequest runs one connection request in its own goroutine: the
// per-request panic recovery and, for process.stdin, the ordering ticket.
// Extracted from serveConn's read loop so the loop reads as gate -> order ->
// hand off; behavior is unchanged.
func (s *server) handleRequest(c *conn.Conn, raw []byte, req request, ordered bool, ticket uint64) {
	method, id := req.Method, req.ID
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
	// rpc.CodeInternal, the JSON-RPC 2.0 standard "Internal error"; the message
	// and log line follow claustrum's own conventions. Nothing here can be
	// probe-verified, and nothing here needs to be: an unreachable frame
	// cannot diverge from anything. See docs/PROTOCOL.md → "Handler panic
	// recovery".
	// The stdin ticket covers DISPATCH ONLY, never the response write.
	// dispatch appends the chunk to the process's existing stdin FIFO
	// (supervisor.Proc.ApplyStdin) before it returns, so byte order is already fixed by
	// then. Holding the ticket across Conn.WriteResponse would add nothing to
	// ordering and would couple input admission to response delivery: a
	// client that stops reading blocks the socket write, and every later
	// process.stdin on the connection stalls behind it — including input
	// for OTHER processes.
	//
	// The inner func is what keeps the release on a defer, so a panic in
	// dispatch cannot strand the ticket and deadlock the connection.
	var resp *rpc.Response
	func() {
		// Registered FIRST, so it unwinds LAST — after doneStdinTurn below,
		// which means a panicking dispatch cannot strand a stdin ticket and
		// deadlock the connection.
		//
		// Scoped to dispatch on purpose. Covering the response write too
		// would let a panic *inside* Conn.WriteResponse produce a SECOND frame
		// for one id — the one failure mode a wire-contract daemon least
		// wants from its safety net. The recover only sets resp; the single
		// write stays below, outside the recovered region.
		defer func() {
			r := recover()
			if r == nil {
				return
			}
			logging.Errorf("[Server] recovered panic: method=%s id=%v: %v", method, rpc.IDForLog(id), r)
			// The stack goes to DEBUG so the ERROR line keeps its shape while
			// the operator can still get the fault location — a recovered
			// panic here means an unreachable path became reachable, and
			// method+id locate the request but not the fault.
			logging.Debugf("[Server] recovered panic stack: method=%s id=%v\n%s", method, rpc.IDForLog(id), debug.Stack())
			// server.shutdown's own reply is {"ok":true}, produced by the normal
			// dispatch path, not here. Under a panic we emit NO frame for it.
			if method == methodShutdown {
				return
			}
			resp = rpc.Ptr(rpc.ErrResult(id, rpc.CodeInternal, fmt.Sprintf("recovered panic: %v", r)))
		}()
		if ordered {
			c.AwaitStdinTurn(ticket)
			defer c.DoneStdinTurn(ticket)
		}
		resp = dispatchRequest(s, c, raw, req)
	}()
	if resp != nil {
		if awaits, _ := s.shutdownReply(); method == methodShutdown && awaits && isChanClosed(s.shutdown) {
			s.awaitDropStart()
		}
		c.WriteResponse(*resp)
	}
}

// shutdownReplyWait bounds how long the server.shutdown reply waits for the teardown
// to start dropping connections (claustrum's own, not measured).
const shutdownReplyWait = 500 * time.Millisecond

// shutdownReplyRule holds the reply rule of one test server, so that a test changes
// no package value that a request goroutine of another server reads.
type shutdownReplyRule struct {
	awaitsDrop bool
	wait       time.Duration
}

// shutdownReply returns whether the server.shutdown reply waits for the drop, and
// for how long at most.
func (s *server) shutdownReply() (bool, time.Duration) {
	if r := s.replyRule; r != nil {
		return r.awaitsDrop, r.wait
	}
	return shutdownReplyAwaitsDrop, shutdownReplyWait
}

// dropStarted returns the channel that dropConns closes when it starts.
func (s *server) dropStarted() chan struct{} {
	s.dropOnce.Do(func() { s.dropping = make(chan struct{}) })
	return s.dropping
}

// awaitDropStart blocks until dropConns starts, or until shutdownReplyWait passes.
// Then a reply to a connection that dropConns marked closed writes nothing.
func (s *server) awaitDropStart() {
	_, wait := s.shutdownReply()
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-s.dropStarted():
	case <-t.C:
	}
}

// isChanClosed reports whether ch is closed.
func isChanClosed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// signalShutdown requests a graceful stop exactly once. From here a spawn that has
// not passed the check before the start starts no child (supervisor.Manager.RefuseStarts). It does not
// block, so the teardown drops the connections at once.
func (s *server) signalShutdown() {
	s.once.Do(func() {
		if s.procs != nil {
			s.procs.RefuseStarts()
		}
		// No late try of the run-dir lock takes the lock once the shutdown began
		// (claustrum's own, not measured).
		if s.stopLateLock != nil {
			s.stopLateLock()
		}
		close(s.shutdown)
	})
}

// signalWatch handles the first SIGTERM or SIGINT of a daemon. runServe starts it
// before the claim of the run dir, so a signal that comes before the bind does not end
// the start. It handles one signal only.
//
// Measured on Linux against 89cb6289 (measured, 5 of 5 runs). A second daemon sent
// SIGTERM to a first daemon that waited in the reap of its start. The first daemon
// logged the "received" line at once. It then finished the reap (the SIGKILL to the
// recorded group and the summary line), bound and listened, wrote its token, logged
// "[Server] shutdown requested", printed the listening line, logged the cleanup line
// and exited 0. The log lines came in that order. macOS gives the same lines.
//
// More Linux rows against 89cb6289, with the same result on claustrum build ac5cadb:
//   - A SIGTERM or a SIGINT to one daemon in its reap: the same flow (measured).
//   - Two SIGTERM 0.3 s apart: one "received" line, the same flow
//     (measured).
//   - A SIGTERM during the eviction wait: the daemon still sends the holder SIGKILL
//     after the grace, takes the lock, reaps, binds and exits 0 (measured).
//   - A -stop during the reap: one SIGTERM, the same flow
//     (measured).
//
// Both lines of the signal are the reference's for SIGTERM, measured on Linux against
// f6010b97 and 89cb6289 for a daemon that serves. "received terminated" is the name
// of that signal. The line for SIGINT uses the same sentence with the name of that
// signal: "received interrupt" (measured). With -keep-children the first line says
// "kept". That flag is claustrum's own.
type signalWatch struct {
	keepChildren bool

	mu    sync.Mutex
	fired bool    // a signal came
	srv   *server // the server, once the start made it
}

// notifySignals is signal.Notify behind a seam, so a test gives the daemon a signal
// through the channel and sends no real one. Production never reassigns it.
var notifySignals = signal.Notify

// watchSignals starts the watch. It takes the first signal of sigc.
func watchSignals(sigc <-chan os.Signal, keepChildren bool) *signalWatch {
	w := &signalWatch{keepChildren: keepChildren}
	go func() { w.handle(<-sigc) }()
	return w
}

// handle logs the "received" line. It requests the shutdown now when the server
// exists. If not, attach requests it.
func (w *signalWatch) handle(sig os.Signal) {
	fate := "killed"
	if w.keepChildren {
		fate = "kept"
	}
	logging.Infof("[daemon] received %v; shutting down (children will be %s)", sig, fate)
	w.mu.Lock()
	w.fired = true
	s := w.srv
	w.mu.Unlock()
	if s != nil {
		s.requestShutdown()
	}
}

// attach gives the watch the server of the finished start. It requests the shutdown
// when a signal came during the start.
func (w *signalWatch) attach(s *server) {
	w.mu.Lock()
	w.srv = s
	fired := w.fired
	w.mu.Unlock()
	if fired {
		s.requestShutdown()
	}
}

// requestShutdown logs the second line of a shutdown by signal and requests the stop.
func (s *server) requestShutdown() {
	logging.Infof("[Server] shutdown requested")
	s.signalShutdown()
}

// teardown closes the listener and removes the socket, then drops clients, then
// stops child processes (per -keep-children) and removes the other files of the
// daemon, then exits.
func (s *server) teardown(socket string) {
	s.closeAll(socket)
	osExit(0)
}

// closeAll releases every daemon-held resource on the graceful-shutdown path:
// listeners, the socket + daemon.token + rpc.pipe files, child processes (per
// -keep-children), and connected clients. Split out of teardown (same pattern
// as stopChildren) so the whole sequence is testable without the process exit.
//
// Its last log line is the reference's "cleanup:" line. Linux and Windows measured it
// against f6010b97 and 89cb6289 at every clean shutdown (-stop, SIGTERM, the orphan
// self-exit). The first count is the connections that this shutdown closed, plus the
// connection that sent server.shutdown if it already left (see dropConns). The second
// count is the children that got the kill. Not measured: the counts for a child that
// ended by itself just before the shutdown.
func (s *server) closeAll(socket string) {
	s.ln.Close()
	// Unlink the socket only if it still has the file identity that we bound. The
	// test comes right after the listener close and before the connections close,
	// as on 5fd08069 (measured on Linux). On Windows the listener close already
	// removed the path, so this guard does not apply there (measured on Windows).
	unlinkOwnSocket(socket, s.sockInfo)
	// Then drop every client, before the slower cleanup below. The server.shutdown
	// handler returns its reply as soon as the stop has started, so the reply write
	// races this close. On Windows the write waits for the start of this drop.
	closed := s.dropConns()
	if s.metricsLn != nil {
		_ = s.metricsLn.Close()
	}
	if s.pipeLn != nil {
		_ = s.pipeLn.Close()
	}
	// Wait for the spawns that passed the check before the start (supervisor.Manager.StopStarts). It
	// comes after the drop, so the connections still close at once. Then the kill
	// below and the record removals of -keep-children see every child whose start
	// returned before the shutdown began, and no record write is open at the exit.
	s.procs.StopStarts()
	// The children get the kill before the token removal and the lock release, as on
	// 5fd08069 (Linux, 3 of 3 runs). macOS runs this code too, and that order is not
	// measured there.
	killed := s.stopChildren()
	// A read of the login-shell PATH that still runs ends here, measured on Linux and
	// macOS against 52d82f0a. Its shell is no child, so the cleanup line does not
	// count it (measured on Linux). Its place among these steps is claustrum's own
	// (not measured).
	shellenv.Stop()
	tokenfile.DropOwn(socket, s.tokenInfo)
	// Remove rpc.pipe on the same graceful path as rpc.sock/daemon.token, and with the
	// same ownership guard: only if it is still the inode we published, so a successor's
	// pipe survives. No-op if the pipe was never started this boot (pipeInfo nil).
	removePipeNameFileIfOwned(socket, s.pipeInfo)
	// The folder of this daemon below git-shadow goes too, and the empty git-shadow
	// stays (ShadowHost.Close). Its place among these removes is not measured.
	s.gitShadow.Close()
	// With -keep-children the kept children lose their records while this daemon
	// still holds the lock. A successor takes the lock only after the release below,
	// so its reap finds no record of a kept child (linux and darwin, a no-op
	// elsewhere). claustrum's own rule: see supervisor.Manager.ForgetKeptRecords.
	if s.keepChildren {
		s.procs.ForgetKeptRecords()
	}
	// Drop the run-dir lock if this daemon holds it: truncate the owner record and
	// unlock daemon.lock, leaving the file in place.
	if s.releaseRunDir != nil {
		s.releaseRunDir()
	}
	s.procs.Close() // end the prune sweep; idempotent
	// The word "ended" is that of b3081b51, where be1c1ca5 said "killed" (measured on
	// Linux, macOS and Windows).
	logging.Infof("[Server] cleanup: closed %d connection(s), ended %d child process group(s)", closed, killed)
	// Closed last, after every connection is down. Unlike the socket and
	// daemon.token, the file itself is left in place. It is the artifact the
	// operator asked for.
	s.wlog.Close()
}

// dropConns closes every client connection. Each conn whose writer is idle is marked
// closed first. Then a reply that has not started yet makes no write attempt and
// logs no write error. A writer that holds the lock is not marked closed.
// TryLock keeps a writer that is stuck on a full socket buffer from blocking the
// teardown. That writer then fails when the close lands.
//
// It returns the number of connections it closed, for the "cleanup:" line. A
// connection that sent server.shutdown counts, also after its read loop ended.
// 5fd08069 logs "closed 2" for one idle client and the -stop client (measured on
// Windows, 3 of 3 runs). Linux and macOS are measured with the -stop client
// alone only, so the count with another client open there is claustrum's own. A
// client that closes before it reads the reply can still end its read loop
// before the mark (claustrum's own, not measured).
//
// Before the loop, dropConns closes the dropStarted channel. That releases a
// server.shutdown reply that waits in awaitDropStart.
func (s *server) dropConns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch := s.dropStarted(); !isChanClosed(ch) {
		close(ch)
	}
	for c := range s.conns {
		c.MarkClosedIfFree()
		c.NC().Close()
	}
	return len(s.conns) + s.stopsGone
}

// stopChildren implements the -keep-children policy on graceful shutdown. By
// default it kills the children: the whole tree on Unix, the job of each child on Windows
// (the direct child with no job). With
// -keep-children set, it
// instead leaves every running child alive so they survive a daemon
// restart/upgrade, logging one honest line with the surviving count. closeAll
// removes their records before it releases the lock. The new
// daemon does not re-adopt them. An out-of-band consumer reconciles them via the
// CT-1 pid/startTime. Split out from teardown so the gate is unit-testable
// without teardown's os.Exit.
//
// It returns the number of children that got a signal, for the "cleanup:" line.
// With -keep-children that number is 0.
func (s *server) stopChildren() int {
	if s.keepChildren {
		s.procs.EndPendingFarewells()
		logging.Infof("[Server] -keep-children: leaving %d running child process(es) alive across shutdown", s.procs.RunningCount())
		return 0
	}
	killed := s.procs.KillAllCount()
	// Wait for the killed children to end, so the end of each one removes its
	// registry record before the daemon exits (linux and darwin, a no-op
	// elsewhere). The wait is bounded by shutdownRecordWait. It does
	// not cover the exited line of such a child. That line prints only when the exit
	// goroutine reaches it before the daemon exits (see exitLogSuffix). With
	// -keep-children the children stay, and this wait does not run.
	s.procs.AwaitRecordsRemoved()
	return killed
}
