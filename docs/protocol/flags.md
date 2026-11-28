# Flags and modes of the binary

This page gives the flags of the claustrum binary, except `-cli-dir`, `-cli-version`, `-cli-url`, `-cli-checksum` and `-cli-zst` of `-install`, and the config keys that match them. It then describes the modes `-serve`, `-bridge`, `-stop`, `-version` and `-check-place`, and the rules that every mode shares. The modes `-install` and `-probe-cli` are on the [install page](../INSTALL.md).

An untagged rule on this page matches build `5fd08069` or an earlier build. A section that names its systems or its build keeps that scope.

## Daemon lifecycle (flags)

One binary, seven modes: `-serve`, `-bridge`, `-stop`, `-version`, `-install`,
`-probe-cli` and `-check-place`. `-install` and `-probe-cli` are on
[their own page](../INSTALL.md).

### Flags and config keys

[DIVERGENCES.md → Conventions for opt-in
entries](../DIVERGENCES.md#conventions-for-opt-in-entries) has the default,
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
| `-max-extract-bytes <n>` | `max-extract-bytes` | `0` | cap `files.extract_tar` bytes (CT-6) | -serve |
| `-git-timeout <dur>` | `git-timeout` | none | deprecated. It sets nothing and logs one warning. The git calls keep their time limits (retired D5) | -serve |
| `-files-read-regular-only` | `files-read-regular-only` | none | deprecated. It sets nothing and logs one warning. `files.read` refuses a file that is not regular with or without it (retired D4) | -serve |
| `-max-cli-bytes <n>` | `max-cli-bytes` | `0` | cap CLI decompress + download (CT-7) | -install |
| `-cli-probe-timeout <dur>` | `cli-probe-timeout` | none | deprecated. It sets nothing and logs one warning. The direct `<cli> --version` run always has its 30 s and 120 s bounds (retired D11) | -install |
| `-cli-download-timeout <dur>` | `cli-download-timeout` | `0` | download deadline (CT-8) | -install |
| `-libc-probe-timeout <dur>` | `libc-probe-timeout` | none | deprecated. It sets nothing and logs one warning. The `ldd` probe always has its 5 s bound (retired D14) | -install |
| `-cli-keep <n>` | none | `3` | version files of the cli-dir to retain on the prune after a good install, the new CLI among them ([other prunes](../INSTALL.md#the-prune-of-a-cache-miss)). The new CLI always stays. `0` and a negative value remove every version file but the new CLI | -install |
| none (config only) | `version-override` | none | `-version` stdout rebrand (CT-3), and the `<version>` of [the place check](#the-place-check-13047a51) | -version, -check-place, -probe-cli, -serve, -bridge, -install |

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
- `-token-fd <n>` is claustrum-only, and on Windows it starts no daemon: `-token-fd 0` gives
  exit `1` and `claustrum: read --token-fd: read token-fd: The handle is invalid.`
  (measured on Windows). On Linux and macOS the launcher reads the token from an open fd
  (`0` is stdin). It passes it to the daemon, never on disk. A failed read there
  gives `claustrum: read --token-fd: <err>` on the launcher's stderr and exit `1` (from the code). With an empty token the
  daemon logs `claustrum: read --token-file: open : <err>`, and the launcher gives up at
  12 s (from the code).

#### The re-exec marker

The re-exec marker is `CLAUSTRUM_DAEMON_CHILD`, not the reference's
`CLAUDE_SSH_DAEMON_CHILD`. [ARCHITECTURE.md](../ARCHITECTURE.md) has the reason.
claustrum unsets its own marker before it spawns. The daemon still sets
`CLAUDE_SSH_DAEMON_CHILD=1` in its environment, so `process.spawn` children
inherit it as on the reference.

The daemon drops `CLAUDE_SSH_CHILD` and `CLAUDE_SSH_RUN_DIR` of the launcher from its
environment. On Linux and macOS `CLAUDE_SSH_DAEMON_CHILD=1` is its last entry. A launcher value `0`
gives way to `1` there. This is measured on Linux against `c9125eb8` and `4c111e86`. On
macOS `87375cc7` and `13047a51` do the same (measured), and the place is not measured. On
Windows `87375cc7` and `13047a51` drop both names (measured). claustrum drops them in the exact
upper case only (claustrum's own, not measured). On Linux and macOS a
`process.spawn` child still gets both names from the spawn.
On Linux and macOS `CLAUSTRUM_DAEMON_CHILD=1` stands right before the last entry, because the re-exec test
reads it. With `-token-fd`, `CLAUSTRUM_TOKEN_PIPE=3` stands before both (claustrum's own).

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
  graceful shutdown ends the children by the
  [rules of the stop](server.md#what-the-stop-does). With it, the children stay alive across a restart,
  and the daemon logs `[Server] -keep-children: leaving <n> running child
  process(es) alive across shutdown`. A survivor loses its stdio. Its stdin
  reaches EOF, and a stdout or stderr write gets SIGPIPE, or EPIPE for a child
  that ignores SIGPIPE, such as Node. On Windows the flag keeps the whole tree
  alive after `-stop` and `server.shutdown` (measured with no job, claustrum's
  own with jobs on, not measured). The stdio of a survivor is not measured there.
- `-listen-pipe` is CT-5 and Windows-only. See [Named-pipe
  transport](../PROTOCOL.md#named-pipe-transport-windows-opt-in). The daemon logs a setup failure
  (`[Server] named-pipe transport: …`), which is non-fatal. The socket still serves.

The opt-in flag of this mode on the wire is `-max-extract-bytes`
([CT-6](../DIVERGENCES.md#ct-6)). Off is parity. Its frame is in
[files.extract_tar](files-extract-tar.md).

### -bridge — stdio↔socket relay

```text
claustrum -bridge -socket <p>
```

An SSH session attaches to this relay. It adds no auth to a relayed line, so the client supplies `"auth"`. A dial
failure is a hard error: `claustrum: dial server: <err>` on stderr, exit `1`.

At stdin EOF the bridge half-closes its socket write side, so the daemon reads
EOF, and it keeps copying the socket to stdout. It relays the
parse-error frame of a final partial line, then exits `0` (measured on Linux
and Windows). It prints every frame that arrived before the daemon closed, and exits `0` with stdin still open (measured on Linux). If the half-close fails, claustrum closes the socket fully.

A write to a closed stdout gives exit `0` (build `52d82f0a`, measured on Linux and macOS). Claustrum's bridge was not measured.

On Windows the daemon can miss the half-close of the bridge (see
[Transport](../PROTOCOL.md#transport)). The bridge then does not exit.

With `CLAUDE_SSH_ADOPT_ENV=1` the bridge first sends [a frame of its own](server-adopt-env.md#the-frame-of-the-bridge).

#### The listener check (build 13047a51)

`CLAUDE_SSH_LISTENER_CHECK` checks the listener after a good dial, before the bridge sends anything. No variable,
`0` and an empty value turn it off. `1` passes the answers `on` and `unavailable`. `2`, `strict`, `yes` and `on` pass
only `on` (measured on Linux, macOS and Windows). `off`, `false`, `no`, `true`, `3`, ` 1` and `00` count as strict
too (measured on Linux). Linux
answers `on` for a daemon in the own user and net namespaces, and refuses one in another user namespace alone
(measured). macOS and Windows answer `unavailable`. A pass prints `claustrum: listener check (<answer>)` on stderr. A refusal prints one line and exits `79`:

```text
claustrum: refusing the listener (other_namespace): process <pid> listens in user:[<n>] net:[<n>], not user:[<n>] net:[<n>]; nothing was sent to it
claustrum: refusing the listener (other_user): user <uid> listens; nothing was sent to it
claustrum: refusing the listener (unreadable): readlink /proc/<pid>/ns/user: permission denied; nothing was sent to it
claustrum: refusing the listener (too_weak): here the check can say no more than "unavailable"; nothing was sent to it
```

The first is a daemon in another net namespace, and the second a daemon of another account. The third is a client in
another user namespace, which cannot read the namespaces of the daemon. Each comes with `1`, `2` and
`strict` (measured on Linux). The last is a strict value (measured on macOS and Windows). A client in another pid namespace
gets `unavailable` on Linux (measured). 13047a51 prints `claude-ssh:` in front. Another mount, uts or ipc namespace
passes (measured on Linux). A refused listener gets nothing (measured on macOS, claustrum's own on Linux and Windows).
Claustrum's own: a failed read of the peer or of the own namespaces answers `unavailable`. The uid test takes the
effective uid, before the pid test and the namespace reads. So a peer of another uid with pid 0 gets `other_user` and
exit `79`, also with `1`. Each new connection after the first gets the check before the bridge writes on it, and a pass
there prints no line (claustrum's own, not measured).

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
changes this stdout and the version of [the place check](#the-place-check-13047a51) only. No JSON-RPC frame changes, and `server.capabilities`
still reports claustrum's own `<id>`.

### The place check (13047a51)

`CLAUDE_SSH_RUN_FROM` names the file that the binary must run from. `-check-place`
checks that place and does nothing else. A good place gives exit `0` and no output.
A refused place gives one stderr line and exit `78`:

```text
claustrum: refusing to run (<kind>) from <path>: <reason>
```

13047a51 writes `claude-ssh:` first. The line names the cleaned path (measured on Windows,
from the code elsewhere). An unset or empty
variable gives the kind `not_asked` and no path:

```text
claustrum: refusing to run (not_asked): CLAUDE_SSH_RUN_FROM is not set, or is empty, so there is nothing to check.
```

The other modes check a set place first (an empty value checks nothing: claustrum's own). These rules are measured on Linux. macOS shows
`-serve`, and Windows shows `-serve`, `-bridge`, `-probe-cli` and no mode.

- `-version` and `-stop` alone do not check.
- These rules are claustrum's own, not measured. `-check-place` wins over every mode but
  `-version`. A `-stop` with `-serve`, `-bridge` or `-install` checks too. The check comes
  before the home folder, so a refused place wins over a missing `HOME`.
- `-probe-cli` prints the line, then `__CLI_BAD__`, and exits `0`.
- `-serve`, `-bridge`, `-install` and no mode exit `78`. A refused `-serve` makes no socket.
- A refused `-serve` first removes its `-token-file`, unless it is a folder. A link goes
  as a link, and its target stays. A FIFO goes. macOS and Windows show a regular file.
  On Windows a folder link or junction stays (claustrum's own, not measured).

On macOS and Windows each set place gets the kind `host`, with the reason
`Only a Linux host's file can be checked.`

On Linux the check walks from `/` down to the file. The first entry that fails names the
reason. claustrum tests each entry from the top for `missing`, `cannot_tell`, `owner` and
`writable`, in that order. Then it tests `another_file`, `mode` and `shape`. The rows show
`writable` before `shape`, and `another_file` before `shape`. On NFS the NFS reason comes
before an owner of uid 65534, a write mode of `0775` and `another_file`:

| kind | the place fails when |
|---|---|
| `not_absolute` | the path is not absolute |
| `missing` | an entry does not exist |
| `cannot_tell` | an entry is on FUSE, virtiofs or NFS, shows uid 65534 or cannot be read, or the check takes over 8 s |
| `owner` | root does not own an entry |
| `writable` | a group or another account can write an entry, also with the sticky bit |
| `another_file` | the program that runs is not that file |
| `mode` | the file has a setuid or setgid bit or file capabilities |
| `shape` | the path does not end in `srv/<version>/server`, or one of those three parts is a link |

The reasons, with `<p>` for the entry, `<uid>` for its owner, `<error>` for the text of a
failed call and `<mode>` for four octal digits:

```text
the helper must be an absolute path, not a bare name resolved via PATH
`<p>`, on the way to the helper, does not exist
`<p>` is on FUSE or virtiofs, so who can change it can't be checked. Keep the helper and every folder above it on a local disk, such as ext4 or XFS, or in a container's image.
`<p>` is on NFS, so who can change it can't be checked. Keep the helper and every folder above it on a local disk, such as ext4 or XFS, or in a container's image.
`<p>` shows as uid 65534 (nobody), which usually means this login can't see its real owner, as in a sandbox that doesn't map root or on a squashed network export. This login must see root as the owner of the helper and every folder above it.
`<p>`, on the way to the helper, cannot be checked: <error>
The file could not be checked within 8 seconds. If a folder on the way to it is on a network share, check that its server is reachable.
`<p>` belongs to the account that signs in (uid <uid>). Root must own the helper and every folder above it.
`<p>` belongs to uid <uid>. Root must own the helper and every folder above it.
`<p>` can be changed by accounts other than its owner (mode <mode>). Remove that write access.
The program that runs is not the file at that path. If the file was replaced while the helper ran, connect again.
It must be a regular file that gives the helper no more privilege than the account has: no setuid or setgid bit, no file capabilities (`setcap -r` removes them).
The path must end in `srv/<version>/server`, this helper's own version, with no link among those three parts.
```

`<version>` is the version word of [`-version`](#-version): the `version-override` of
`claustrum.conf`, or claustrum's own version (from the code).

`<error>` is measured for an entry on the way: `permission denied`, and `transport endpoint
is not connected` on FUSE. For the other calls it is claustrum's own.

A check past 8 s gets the 8 seconds reason. It gets it also when it passes later
(claustrum's own, not measured). On a stalled FUSE mount 13047a51 prints the line at 8 to
9 s and exits when the stalled call returns (measured on Linux for `-check-place` and
`-serve`). claustrum prints the line and waits for the check too. The wait of the other
modes is claustrum's own. A stalled `-serve` removes its `-token-file` at the bound, before
the wait (measured on Linux, a stall past 8 s).

A known difference (measured on Linux): after one stall of 5 s on FUSE, 13047a51 still
gives the 8 seconds reason. claustrum waits for the answer and gives the FUSE reason.

Not measured: the order of `owner` and `writable` on one entry and of `another_file` and
`mode`, a link above the three parts, and a file that is not regular (claustrum: `another_file`).

### Behavior shared by every mode

- The default socket is `~/.claude/remote/rpc.sock`. When `-socket` is omitted, all
  modes fall back to it. If the parent directory is missing, `-serve` creates it
  with mode `0700`, so a bare `-serve` on a fresh machine works. `-bridge`
  and `-stop` do not create it. When no daemon has run, `-bridge` fails with
  `connect: no such file or directory`, and `-stop` prints `none`.
- With no mode given, claustrum prints
  `claustrum: one of --version/--install/--probe-cli/--serve/--bridge/--stop/--check-place is required`
  (the list of `13047a51`, measured on Linux and Windows, not on macOS. Its line starts with
  `claude-ssh:`, so the name is claustrum's own)
  on stderr and exits `2`, with no usage dump. An *unknown flag* gets the stdlib
  `flag` error plus the usage, and exit `2`.

See [-install](../INSTALL.md#__install_result__-facts) for the facts schema and
[ARCHITECTURE.md](../ARCHITECTURE.md) for the deployment lifecycle. See [`DIVERGENCES.md`](../DIVERGENCES.md) for the full
divergence catalog and rules. See [EXAMPLES.md](../EXAMPLES.md) for runnable
snippets.
