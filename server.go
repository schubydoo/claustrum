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
)

// tokenPipeEnv names the file descriptor the daemonized child reads its auth
// token from when the token was supplied via -token-fd. The parent reads the
// caller's fd (only valid pre-daemonize) and forwards the token to the child
// over an inherited pipe; this env carries only the fd number, never the token.
const tokenPipeEnv = "CLAUSTRUM_TOKEN_PIPE"

// server is the running -serve daemon: the AF_UNIX listener, the auth token, the
// connected clients, and the process manager.
type server struct {
	token string
	ln    net.Listener
	procs *procManager

	mu        sync.Mutex
	conns     map[*conn]struct{}
	shutdown  chan struct{}
	once      sync.Once
	metricsLn net.Listener // optional Prometheus listener; nil unless -metrics-addr set
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
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	b = append(b, '\n')
	_, err = c.nc.Write(b)
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
// child is marked with CLAUDE_SSH_DAEMON_CHILD so we re-exec exactly once.
func runServe(socket, tokenFile string, tokenFd int, metricsAddr string) {
	// -serve requires a token source, checked BEFORE the socket (probe-verified).
	// The CLAUDE_RPC_TOKEN env is NOT accepted here: the daemon's token always
	// comes from a file (read once, then unlinked) or an fd (read by the parent,
	// forwarded over a pipe), so it never lingers in /proc/<pid>/environ. (env is
	// only for the -bridge/-stop clients.)
	if tokenFile == "" && tokenFd < 0 {
		fmt.Fprintln(os.Stderr, "claustrum: daemonized child requires --token-file or --token-fd")
		os.Exit(1)
	}
	if os.Getenv("CLAUDE_SSH_DAEMON_CHILD") != "1" {
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

	s := &server{
		token:    token,
		ln:       ln,
		procs:    newProcManager(),
		conns:    make(map[*conn]struct{}),
		shutdown: make(chan struct{}),
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
	cmd.Env = append(os.Environ(), "CLAUDE_SSH_DAEMON_CHILD=1")
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

func (s *server) run(socket string) {
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGTERM, syscall.SIGINT)
	go func() { <-sigc; s.signalShutdown() }()
	go func() { <-s.shutdown; s.teardown(socket) }()

	for {
		nc, err := s.ln.Accept()
		if err != nil {
			select {
			case <-s.shutdown:
				return
			default:
				logWarnf("[Server] accept error (retrying): %v", err)
				continue
			}
		}
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

// teardown closes the listener, kills child processes, drops clients, and exits.
func (s *server) teardown(socket string) {
	s.ln.Close()
	if s.metricsLn != nil {
		_ = s.metricsLn.Close()
	}
	_ = os.Remove(socket)
	s.procs.killAll()
	s.mu.Lock()
	for c := range s.conns {
		c.nc.Close()
	}
	s.mu.Unlock()
	os.Exit(0)
}
