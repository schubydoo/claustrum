# Flags and modes of the binary

This page gives the flags of the claustrum binary, except `-cli-dir`, `-cli-version`, `-cli-url`, `-cli-checksum` and `-cli-zst` of `-install`, and the config keys that match them. It then describes the modes `-serve`, `-bridge`, `-stop` and `-version`, and the rules that every mode shares. The modes `-install` and `-probe-cli` are on the [install page](../INSTALL.md).

An untagged rule on this page matches build `5fd08069` or an earlier build. A section that names its systems or its build keeps that scope.

## Daemon lifecycle (flags)

One binary, six modes: `-serve`, `-bridge`, `-stop`, `-version`, `-install` and
`-probe-cli`. `-install` and `-probe-cli` are on
[their own page](../INSTALL.md).

### Flags and config keys

[DIVERGENCES.md → Conventions for opt-in
divergences](../DIVERGENCES.md#conventions-for-opt-in-divergences) has the default,
the precedence of a flag and its key, and the bypass rule.

| flag | config key | default | effect when set | mode |
|---|---|---|---|---|
| `-token-file <p>` | none | none | token source (read once, unlinked) | -serve |
| `-token-fd <n>` | none | `-1` | token from an open fd (claustrum-only) | -serve |
| `-metrics-addr <a>` | `metrics-addr` | `""` | Prometheus `/metrics` (claustrum-only) | -serve |
| `-wire-log <p>` | `wire-log` | `""` | append every JSON-RPC frame to `<p>` as JSONL (claustrum-only) | -serve |
| `-wire-log-max-string <n>` | `wire-log-max-string` | `512` | bytes kept per string value. `0` keeps whole payloads | -serve |
| `-keep-children` | `keep-children` | off | survive restart (CT-2) | -serve |
| `-listen-pipe` | `listen-pipe` | off | named-pipe transport, Windows-only (CT-5) | -serve |
| `-max-extract-bytes <n>` | `max-extract-bytes` | `0` | cap `files.extract_tar` bytes (D3) | -serve |
| `-git-timeout <dur>` | `git-timeout` | none | deprecated. It sets nothing and logs one warning. The git calls keep their time limits (retired D5) | -serve |
| `-files-read-regular-only` | `files-read-regular-only` | none | deprecated. It sets nothing and logs one warning. `files.read` refuses a file that is not regular with or without it (retired D4) | -serve |
| `-max-cli-bytes <n>` | `max-cli-bytes` | `0` | cap CLI decompress + download (D10) | -install |
| `-cli-probe-timeout <dur>` | `cli-probe-timeout` | none | deprecated. It sets nothing and logs one warning. The direct `<cli> --version` run always has its 30 s and 120 s bounds (retired D11) | -install |
| `-cli-download-timeout <dur>` | `cli-download-timeout` | `0` | download deadline (D12) | -install |
| `-libc-probe-timeout <dur>` | `libc-probe-timeout` | none | deprecated. It sets nothing and logs one warning. The `ldd` probe always has its 5 s bound (retired D14) | -install |
| `-cli-keep <n>` | none | `3` | version files of the cli-dir to retain on prune, the new CLI among them. The new CLI always stays. `0` and a negative value remove every version file but the new CLI | -install |
| none (config only) | `version-override` | none | `-version` stdout rebrand (CT-3) | -version |

Bool keys accept `true/1/yes/on` and `false/0/no/off`. Any other bool value, and a
negative or unparseable number, leaves the key unset, so the flag value or the
default stands.

### -serve — run the daemon

```text
claustrum -serve -socket <p> {-token-file <p> | -token-fd <n>} [-metrics-addr <a>] \
          [-keep-children] [-listen-pipe] [-wire-log <p> [-wire-log-max-string <n>]] \
          [-max-extract-bytes <n>]
```

The binary self-daemonizes. It re-execs itself as a detached process, which
reparents to init, and that process runs the RPC server.
[Daemon startup](../DAEMON.md#daemon-startup-serve) has what the start does, the ready line,
and the exit codes and texts of the launcher. Unless a rule here says otherwise,
it is measured against `89cb6289` on Linux and macOS. The flags under
[Flags of claustrum alone](#flags-of-claustrum-alone) have no reference run.
`process.spawn` has [the login-shell
PATH](process-spawn.md#the-login-shell-path-linux-and-macos) of the
children.

#### The token source

A token source is required. The detached daemon tests for it, not the launcher.
The daemon writes its reason to its own stderr, which is the
[daemon log](../DAEMON.md#daemon-log-remote-serverlog), and exits. The launcher then gives up at
12 s, as under [The launcher wait](../DAEMON.md#the-launcher-wait). This is deliberate parity.

- With neither flag the reason is
  `claustrum: daemonized child requires --token-file or --token-fd` (not
  measured).
- A zero-byte `-token-file` behaves the same way, with the reason
  `claustrum: token is empty`. The daemon removes the file first.
- A `-token-file` that cannot be read gives `claustrum: read --token-file: <err>`
  (from the code).
- The daemon reads the token as a line. It strips every trailing `\r` and `\n` (a single
  `\n` or `\r\n` is measured). It keeps other whitespace verbatim (from the code). It then removes the
  `-token-file`, as under [Authentication](../PROTOCOL.md#authentication).
- `-token-fd <n>` is claustrum-only. On Windows the start fails (from the code, not
  measured). On Linux and macOS the launcher reads the token from an
  already-open fd, where `0` is stdin, and forwards it to the daemon over an
  inherited pipe. So this handoff never touches disk. A failed read there gives
  `claustrum: read --token-fd: <err>` on the stderr of the launcher, with exit `1`
  (from the code). An empty token there is no read failure. The daemon then logs
  `claustrum: read --token-file: open : <err>`, and the launcher gives up at 12 s
  (from the code).

#### The re-exec marker

The re-exec marker is `CLAUSTRUM_DAEMON_CHILD`, not the reference's
`CLAUDE_SSH_DAEMON_CHILD`. [ARCHITECTURE.md](../ARCHITECTURE.md) has the reason.
claustrum unsets its own marker before it spawns. The daemon still sets
`CLAUDE_SSH_DAEMON_CHILD=1` in its environment, so `process.spawn` children
inherit it as on the reference.

#### Flags of claustrum alone

These flags are off the wire. `-keep-children` and `-listen-pipe` have an entry in
[`DIVERGENCES.md`](../DIVERGENCES.md). `-metrics-addr` and `-wire-log` have none:
- `-metrics-addr <a>` serves Prometheus counters at
  `http://<a>/metrics`, covering connections, spawns and exits, reattaches, and
  stream and stdin bytes. It is off by default, with no listener. It counts only,
  and it has no auth, so bind it to loopback. The daemon logs a bind failure
  (`[Server] metrics: …`), which is non-fatal.
- `-wire-log <p>` appends every JSON-RPC frame, in both directions, to
  `<p>` as JSONL. It observes already-marshaled bytes, so a daemon that logs
  emits frames byte-identical to one that does not. It is off by default, with no
  file and no work. `-wire-log-max-string <n>` bounds each string value. The
  default is 512, and `0` keeps whole payloads, which is needed to reconstruct a
  session from stream frames. Each record carries the frame as a decoded `body`,
  truncated per value, where field order is not significant. At
  `-wire-log-max-string=0` the record carries the frame as `raw` instead,
  verbatim, with the field order and number formatting of the wire. An
  unopenable path is fatal. [SECURITY.md](https://github.com/schubydoo/claustrum/blob/main/SECURITY.md) has the redaction and
  the file mode.
- `-keep-children` is [CT-2](../DIVERGENCES.md#ct-2), off by default. Without it, a
  graceful shutdown ends the children: the whole tree on Linux and macOS, the
  direct child on Windows. With it, the children stay alive across a restart,
  and the daemon logs `[Server] -keep-children: leaving <n> running child
  process(es) alive across shutdown`. A survivor loses its stdio. Its stdin
  reaches EOF, and a stdout or stderr write gets SIGPIPE, or EPIPE for a child
  that ignores SIGPIPE, such as Node. On Windows the flag keeps the whole tree
  alive after `-stop` and after `server.shutdown`, the direct child too
  (measured on Windows). The stdio of a survivor is not measured there.
- `-listen-pipe` is CT-5 and Windows-only. See [Named-pipe
  transport](../PROTOCOL.md#named-pipe-transport-windows-opt-in). The daemon logs a setup failure
  (`[Server] named-pipe transport: …`), which is non-fatal. The socket still serves.

The opt-in divergence of this mode on the wire is `-max-extract-bytes`
([D3](../DIVERGENCES.md#d3)). Off is parity. Its frame is in
[files.extract_tar](files-extract-tar.md).

### -bridge — stdio↔socket relay

```text
claustrum -bridge -socket <p>
```

An SSH session attaches to this relay. It adds no auth, so the client supplies `"auth"`. A dial
failure is a hard error: `claustrum: dial server: <err>` on stderr, exit `1`.

At stdin EOF the bridge half-closes its socket write side, so the daemon reads
EOF, and it keeps copying the socket to stdout. It relays the
parse-error frame of a final partial line, then exits `0` (measured on Linux
and Windows). It prints every frame that arrived before the daemon closed, and exits `0` with stdin still open (measured on Linux). If the half-close fails, claustrum closes the socket fully.

A closed stdout ends the bridge with exit `0` (build `52d82f0a`, measured on Linux and macOS). Claustrum's bridge was not measured.

On Windows the daemon can miss the half-close of the bridge (see
[Transport](../PROTOCOL.md#transport)). The bridge then does not exit.

### -stop — ask a running daemon to shut down

```text
claustrum -stop -socket <p>          # no token needed, and none is read
```

Unless a rule says otherwise, the rules of `-stop` are measured on Linux and
macOS, and the steps after a successful connect on Windows too. A rule marked claustrum's own is not measured.

`-stop` sends `{"jsonrpc":"2.0","id":1,"method":"server.shutdown"}` and a
newline. It sends no `auth` member, because that method is not authenticated
(see [Authentication](../PROTOCOL.md#authentication)). Then it reads once, with a 2 s deadline,
and discards what it reads. It never relays a frame to stdout. A live daemon's
reply does not always arrive (see [The reply races the close](server.md#the-reply-races-the-close)). A reply and an EOF lead to
the same next step. claustrum treats every other read result the same way
(claustrum's own).

`-stop` prints one word on stdout, then a newline, and exits `0` in every state
in this table:

| word | state |
|---|---|
| `stopped` | The connect succeeded. On Linux and macOS `daemon.lock` also came free within 3 s, or there is no usable `daemon.lock` (claustrum's own). On Windows the word comes at once. |
| `none` | The connect failed, and `daemon.lock` is free or missing. If the lock cannot be opened or locked, claustrum prints `none` too. |
| `terminated` | The connect failed, or the lock was still held 3 s after a successful connect. A live serve daemon of this binary for this socket holds `daemon.lock`. `-stop` sent `SIGTERM`, and the lock came free within 4 s. |
| `killed` | As `terminated`, but the lock was still held 4 s after `SIGTERM`. `-stop` sent `SIGKILL`, and the lock came free within 1.5 s. |
| `survivor` | A live process that is not that daemon holds `daemon.lock`, and `-stop` does not signal it. If the lock is still held 1.5 s after `SIGKILL`, the word is `survivor` too, and `-stop` does not wait longer. If a signal cannot reach a holder that is still there, claustrum prints it too. |

After a successful connect, on Linux and macOS, `-stop` tries `daemon.lock` every 50 ms for up
to 3 s. A lock that comes free gives `stopped` and no signal. A lock still held
after 3 s gets the signal steps of a failed connect. A holder whose record names
no daemon role gets no signal. `-stop` tries the lock for 0.5 s more, and then
prints `survivor`. A lock that comes free in that time gives `stopped`
(claustrum's own).

The lock check uses the holder test of the serve eviction. See [Run-dir
lock](../DAEMON.md#run-dir-lock-daemonlock). On Linux the holder test of the reference is
not isolated (not measured). Before `SIGKILL`, `-stop` tests the holder again. A
lock that another process took during the `SIGTERM` wait gives `survivor`. The
new holder gets no signal (measured on Linux after a successful connect. After a
failed connect it is claustrum's own). Windows has no run-dir lock, so a
failed connect there always prints `none`. On macOS claustrum does not signal a
holder that is not a serve process, and prints `survivor`
([D15](../DIVERGENCES.md#d15)).

The lock steps write these lines to stderr. claustrum adds its own level tag
before the reference text, as it does for the serve eviction lines:

```text
<ts> INFO  [daemon] stop: run dir is held by a live daemon, pid <N> (instance "<hex32>"); sending SIGTERM
<ts> INFO  [daemon] stop: previous daemon pid <N> (instance "<hex32>") exited after SIGTERM
<ts> WARN  [daemon] stop: WARNING previous daemon pid <N> (instance "<hex32>") ignored SIGTERM for 4s; sending SIGKILL (its Claude Code children, if any, are ended next, before this daemon serves)
<ts> WARN  [daemon] stop: WARNING <dir>/daemon.lock is held by a live process we will not signal (pid <N> is not a <basename> --serve process for <sock>); leaving it alone
<ts> WARN  [daemon] stop: WARNING previous daemon pid <N> (instance "<hex32>") survived SIGKILL (uninterruptible?); proceeding without run-dir ownership
```

A daemon that has not bound yet has a short record. That holder gives the first
two lines with `pid <N>` and no instance (measured on Linux). claustrum
writes the other lines with `(instance "")` then (claustrum's own).

A holder with no daemon role, after a successful connect, gives this line:

```text
<ts> WARN  [daemon] stop: WARNING <dir>/daemon.lock is held by a live process we will not signal (its holder is not a daemon (role "<role>")); leaving it alone
```

A lock that another daemon took during the `SIGTERM` wait gives this line, with
the pid and instance of the new holder:

```text
<ts> WARN  [daemon] stop: <dir>/daemon.lock changed hands during eviction (now pid <N> (instance "<id>")); leaving the new holder alone
```

This line is claustrum's own. It comes from the second holder test:

```text
<ts> WARN  [daemon] stop: pid <N> is no longer our serve holder before SIGKILL (<reason>); leaving it alone
```

`-stop` never creates or removes `daemon.lock`. It writes a record into it only
after a successful connect that led to `terminated` or `killed`. Then it writes
`{"pid":<pid>,"role":"stop","node":"<node>"}` and empties the file again before
it prints the word.

After a successful connect, `-stop` removes the socket and `daemon.token` only
after `terminated` or `killed` (from the code). A live daemon removes both files itself at its stop. After
a failed connect, `-stop` removes the socket path and the `daemon.token` beside
it, stale or not, unless the word is `survivor`. The socket unlink removes a
stale socket, a regular file and an empty directory. It keeps a non-empty
directory, and a path in a directory that the user cannot write.

The unlink does not check what the path is. So, unless the word is `survivor`, a
live foreign listener that refuses the connect with `EACCES` loses its socket
path, and a client that dials by path cannot reach it. A variant that removes
only a socket is under [Candidates considered but not
taken](../DIVERGENCES.md#candidates-considered-but-not-taken).

`-version`, `-install` and `-probe-cli` win over `-stop`. `-bridge` and `-serve`
win too. With `-stop -bridge` claustrum runs the bridge, and with `-stop -serve`
it runs serve.

A daemon of a build before the auth exemption of `server.shutdown` keeps running
while `-stop` prints `stopped`. [IMPROVEMENTS.md](../IMPROVEMENTS.md) item 37 has
the upgrade step.

### -version

```text
claustrum -version                   # → claustrum <id> (built <time>)
```

With `version-override` set in `claustrum.conf` ([CT-3](../DIVERGENCES.md#ct-3)),
the output is:

```text
claustrum -version                   # → claude-ssh <sha> (via Claustrum <id>, built <time>)
```

The desktop client decides whether to upload the reference again from a
`<bin> --version` output that matches `/claude-ssh\s+(\S+)/`. The override
changes this stdout only. No JSON-RPC frame changes, and `server.capabilities`
still reports claustrum's own `<id>`.

### Behavior shared by every mode

- The default socket is `~/.claude/remote/rpc.sock`. When `-socket` is omitted, all
  modes fall back to it. If the parent directory is missing, `-serve` creates it
  with mode `0700`, so a bare `-serve` on a fresh machine works. `-bridge`
  and `-stop` do not create it. When no daemon has run, `-bridge` fails with
  `connect: no such file or directory`, and `-stop` prints `none`.
- With no mode given, claustrum prints
  `claustrum: one of --version/--install/--probe-cli/--serve/--bridge/--stop is required`
  on stderr and exits `2`, with no usage dump. An *unknown flag* gets the stdlib
  `flag` error plus the usage, and exit `2`.

See [-install](../INSTALL.md#__install_result__-facts) for the facts schema and
[ARCHITECTURE.md](../ARCHITECTURE.md) for the deployment lifecycle. See [`DIVERGENCES.md`](../DIVERGENCES.md) for the full
divergence catalog and rules. See [EXAMPLES.md](../EXAMPLES.md) for runnable
snippets.
