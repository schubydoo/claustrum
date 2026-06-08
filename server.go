package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
)

// server is the running -serve daemon: the AF_UNIX listener, the auth token, the
// connected clients, and the process manager.
type server struct {
	token string
	ln    net.Listener
	procs *procManager

	mu       sync.Mutex
	conns    map[*conn]struct{}
	shutdown chan struct{}
	once     sync.Once
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
		log.Printf("[Server] Failed to write response: %v", err)
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
		log.Printf("[Server] writeResponse: wrote %d/%d bytes, error=%v", n, len(b), err)
		log.Printf("[Server] Failed to write response: %v", err)
	}
}

// runServe self-daemonizes (reparenting to init) then runs the RPC server. The
// child is marked with CLAUDE_SSH_DAEMON_CHILD so we re-exec exactly once.
func runServe(socket, tokenFile string) {
	if socket == "" {
		fmt.Fprintln(os.Stderr, "-socket is required")
		os.Exit(1)
	}
	if os.Getenv("CLAUDE_SSH_DAEMON_CHILD") != "1" {
		daemonize()
		return
	}

	// We are the detached child. Read the token (env or -token-file) and unlink
	// the file so the token never lingers in /proc/<pid>/environ or on disk.
	token := os.Getenv("CLAUDE_RPC_TOKEN")
	if tokenFile != "" {
		b, err := os.ReadFile(tokenFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read --token-file: %v\n", err)
			os.Exit(1)
		}
		token = string(b)
		_ = os.Remove(tokenFile)
	}

	// Extract a real interactive PATH from the login shell so spawned children
	// resolve tools the way an interactive session would.
	extractLoginPATH()

	_ = os.Remove(socket) // clear a stale socket
	ln, err := net.Listen("unix", socket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen %s: %v\n", socket, err)
		os.Exit(1)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "chmod socket: %v\n", err)
	}

	s := &server{
		token:    token,
		ln:       ln,
		procs:    newProcManager(),
		conns:    make(map[*conn]struct{}),
		shutdown: make(chan struct{}),
	}
	fmt.Printf("Claustrum remote server listening on %s\n", socket)
	s.run(socket)
}

func daemonize() {
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
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "daemonize: %v\n", err)
		os.Exit(1)
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
				log.Printf("[Server] accept error (retrying): %v", err)
				continue
			}
		}
		c := &conn{nc: nc}
		log.Printf("[Server] New connection from: %s", c.nc.RemoteAddr())
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
		log.Printf("[Server] Connection closed: %s", c.nc.RemoteAddr())
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
	_ = os.Remove(socket)
	s.procs.killAll()
	s.mu.Lock()
	for c := range s.conns {
		c.nc.Close()
	}
	s.mu.Unlock()
	os.Exit(0)
}
