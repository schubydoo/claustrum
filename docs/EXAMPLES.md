# claustrum examples

These snippets run against a local daemon. They assume that `claustrum` is on
your `PATH`. A connection stays open, because stream notifications come after a
response. Thus the streaming examples use a small Node helper that reads for a
fixed time window. The simple request/response examples use `socat`.

## Start a private daemon

```sh
D=$(mktemp -d /tmp/claustrum.XXXXXX)
TOK=$(uuidgen)                 # or: head -c24 /dev/urandom | base64
printf '%s' "$TOK" > "$D/token"
claustrum -serve -socket "$D/rpc.sock" -token-file "$D/token"   # self-daemonizes
# the token file is now unlinked; keep $TOK in your client
```

## One-shot request/response (socat)

```sh
req() { printf '%s\n' "$1" | socat -t2 - UNIX-CONNECT:"$D/rpc.sock"; }

req "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"server.ping\",\"auth\":\"$TOK\"}"
# {"jsonrpc":"2.0","id":1,"result":{"pong":true}}

req "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"server.capabilities\",\"auth\":\"$TOK\"}"
req "{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"files.stat\",\"params\":{\"path\":\"/etc/hostname\"},\"auth\":\"$TOK\"}"
req "{\"jsonrpc\":\"2.0\",\"id\":4,\"method\":\"git.info\",\"params\":{\"path\":\"$PWD\"},\"auth\":\"$TOK\"}"
```

## A reusable client (Node)

```js
// client.js  — SOCK and TOK from env; pass a JSON array of requests as argv[2]
const net = require('net');
const c = net.connect(process.env.SOCK, () => {
  for (const r of JSON.parse(process.argv[2])) {
    if (r.auth === undefined) r.auth = process.env.TOK;
    c.write(JSON.stringify(r) + '\n');
  }
});
let buf = '';
c.on('data', d => {
  buf += d;
  let i;
  while ((i = buf.indexOf('\n')) >= 0) {
    const line = buf.slice(0, i); buf = buf.slice(i + 1);
    if (!line.trim()) continue;
    const m = JSON.parse(line);
    if (m.type === 'stream' && m.data) m.decoded = Buffer.from(m.data, 'base64').toString();
    console.log(JSON.stringify(m));
  }
});
setTimeout(() => { c.destroy(); process.exit(0); }, Number(process.env.WINDOW || 1500));
```

```sh
run() { SOCK="$D/rpc.sock" TOK="$TOK" node client.js "$1"; }
```

## Spawn a process and read its output stream

```sh
run '[{"jsonrpc":"2.0","id":1,"method":"process.spawn",
       "params":{"id":"p1","command":"sh","args":["-c","echo hello; echo oops 1>&2; exit 3"]}}]'
# {"jsonrpc":"2.0","id":1,"result":{"success":true}}
# {"type":"stream","processId":"p1","stream":"stdout","seq":1,"data":"aGVsbG8K","decoded":"hello\n"}
# {"type":"stream","processId":"p1","stream":"stderr","seq":2,"data":"b29wcwo=","decoded":"oops\n"}
# {"type":"stream","processId":"p1","stream":"exit","seq":3,"exitCode":3}
```

## Feed stdin to a running process

```sh
DATA=$(printf 'ping\n' | base64)
run "[{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"process.spawn\",\"params\":{\"id\":\"cat1\",\"command\":\"cat\"}},
     {\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"process.stdin\",\"params\":{\"id\":\"cat1\",\"data\":\"$DATA\"}},
     {\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"process.kill\",\"params\":{\"id\":\"cat1\",\"signal\":\"SIGTERM\"}}]"
# the stdin reply is {"id":2,"result":{"success":true,"applied":5}} (5 = bytes of "ping\n");
# stdout frame decodes to "ping\n"; exit frame has exitCode -1 (signalled)
```

`process.stdin` also accepts an `offset`, the byte position at which the data
starts. The `offset` makes replay across reconnects idempotent. If you send only
bytes that the daemon applied before, the daemon does nothing and flags the reply
`"duplicate":true`. If the `offset` is past the applied count, the daemon returns
a `-32003 stdin offset gap` error. See [PROTOCOL.md](PROTOCOL.md).

## Reattach / catch up via the replay buffer

```sh
# spawn an emitter, then later reattach from seq 0 to replay everything buffered:
run '[{"jsonrpc":"2.0","id":1,"method":"process.spawn",
       "params":{"id":"em","command":"sh","args":["-c","for i in 1 2 3; do echo $i; done"]}},
      {"jsonrpc":"2.0","id":2,"method":"process.reattach","params":{"id":"em","fromSeq":0}}]'
# the reattach replays the buffered stdout+exit frames, then returns
# {"id":2,"result":{"found":true,"running":false,"firstSeq":1,"lastSeq":4,"stdinApplied":0}}
```

The `firstSeq` and `lastSeq` values are examples only. The daemon frames stdout
in chunks, not one frame per line. Thus the three `echo` outputs can arrive in
one frame and change `lastSeq`. A process continues to run when the connection
that spawned it disconnects. A *new* connection can `reattach` to that process
and continue to read the stream. This is the reconnect path.

## Opt into `pid` + `startTime` — `wantPid` (claustrum extension)

!!! note "An addition, not reference behavior"
    The reference daemon has no `wantPid`. This is an opt-in **claustrum
    extension** (CT-1; see [DIVERGENCES.md](DIVERGENCES.md)). If you omit it,
    which is the default, every frame stays byte-identical to the reference.
    `wantPid` does not change the original `spawn` and `reattach` behavior. It
    only *adds* fields when you ask for them.

`process.spawn` and `process.reattach` accept `"wantPid":true`. When you set it,
the result carries the child's OS `pid` and a `startTime` token. The daemon
returns the same two values on spawn and on reattach for the same `id`. Use them
to detect PID reuse and orphans:

```sh
run '[{"jsonrpc":"2.0","id":1,"method":"process.spawn",
       "params":{"id":"w1","command":"sh","args":["-c","echo hi"],"wantPid":true}},
      {"jsonrpc":"2.0","id":2,"method":"process.reattach",
       "params":{"id":"w1","fromSeq":0,"wantPid":true}}]'
# {"id":1,"result":{"success":true,"pid":12345,"startTime":1718040000.12}}
# … stdout + exit stream frames …
# {"id":2,"result":{"found":true,"running":false,"firstSeq":1,"lastSeq":2,"stdinApplied":0,"pid":12345,"startTime":1718040000.12}}
```

Without `wantPid`, those same two replies are exactly `{"success":true}` and
`{"found":…,"running":…,"firstSeq":…,"lastSeq":…,"stdinApplied":…}`. The
`pid` and `startTime` fields are absent (`omitempty`).

`startTime` is an **opaque token**. Store it. Then compare a daemon value
against a *later* daemon value for the **same `id`** to detect PID reuse. Do
**not** compare it for equality against a process start time that you read from
the OS (for example, psutil `create_time`). `startTime` is the daemon's wall
clock at the moment of the spawn, not the kernel's process-creation time.

## Extract a plugin tarball

```sh
run "[{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"files.extract_tar\",
       \"params\":{\"archivePath\":\"/path/to/plugin.tar.gz\",\"destDir\":\"/abs/out/dir\"}}]"
# {"id":1,"result":{"success":true,"fileCount":N}}   (expects a gzip tarball; destDir must be absolute, non-root)
```

The daemon also refuses a `destDir` that *is or contains* your home directory.
This is the `wipesHomeDir` guard, not only the absolute/non-root check that the
comment shows. `extract_tar` deletes `destDir` before it extracts, and `"~"`
expands to `$HOME`. Thus the home guard is what stops a bare `"~"` from deleting
your home directory. See [DIVERGENCES.md](DIVERGENCES.md) (D2).

## Shut it down

```sh
# No token: server.shutdown is the one unauthenticated method.
claustrum -stop -socket "$D/rpc.sock"
rm -rf "$D"
```

## Install/ensure the agent CLI (offline-verifiable)

```sh
claustrum -install -cli-dir "$D/cli" -cli-version 1.2.3 \
  -cli-url https://example.invalid/cli.zst -cli-checksum <sha256-of-the-zst>
# prints: __INSTALL_RESULT__{"serverVersion":"…","os":"linux","arch":"amd64","libc":"glibc",
#                            "cliPath":"…/cli/1.2.3","cliWasPresent":false,"cliError":"…"}
```

`cliError` is `omitempty`. A successful install omits it. It appears above only
because `https://example.invalid` is unreachable. The download thus fails, and
the error goes into the field. `libc` is `glibc` on a glibc linux host and `musl`
on a musl linux host. Off linux, `libc` is empty (`""`).

## Operational knobs (claustrum-only, all off the wire)

### Token on a file descriptor

Start the daemon with the token on a file descriptor instead of a temp file.
You then write no token file (fd `0` also works, to pipe the token on stdin).
The daemon itself still persists `daemon.token` beside the socket
([PROTOCOL.md](PROTOCOL.md)):

```sh
claustrum -serve -socket "$D/rpc.sock" -token-fd 3 3< <(printf '%s' "$TOK")
```

### Prometheus metrics

Opt into Prometheus counters for connections, spawns, exits, reattaches, and
stream and stdin bytes. No listener exists unless you set the flag. The endpoint
serves counts only and has no auth. Thus bind it to loopback:

```sh
claustrum -serve -socket "$D/rpc.sock" -token-file "$D/token" -metrics-addr 127.0.0.1:9090
curl -s http://127.0.0.1:9090/metrics | grep claustrum_
# claustrum_connections_total 2
# claustrum_process_spawns_total 1
# …
```

### Log level

Make the daemon's stderr diagnostics quieter. The default emits everything,
which matches the reference. The `[Server]`, `[process.Manager]` and the other
prefixes stay grep-able at every level:

```sh
CLAUSTRUM_LOG_LEVEL=warn claustrum -serve -socket "$D/rpc.sock" -token-file "$D/token"
```

### `-keep-children`

Use `-keep-children` (CT-2, POSIX-only) to let child processes survive a daemon
restart. A graceful shutdown leaves the spawned children running and does not
kill them. Thus the children outlive a daemon restart or upgrade.

- **Off by default** — a shutdown kills the whole process tree.
- **No re-adoption** — the new daemon does not re-adopt the survivors. Reconcile
  them out-of-band with the CT-1 `pid` and `startTime`.
- **Survivors lose stdio** — stdin gets EOF, and writes to stdout and stderr hit
  a closed pipe (SIGPIPE/EPIPE, see [PROTOCOL.md](PROTOCOL.md)). Thus only
  children that tolerate this condition genuinely outlive the daemon.
- **Windows ignores it** — Windows drops the flag and prints a warning. A Job
  Object kills the children when the daemon exits, in every case.

```sh
claustrum -serve -socket "$D/rpc.sock" -token-file "$D/token" -keep-children
# on graceful shutdown the daemon logs, instead of killing them:
#   [Server] -keep-children: leaving 2 running child process(es) alive across shutdown
```

See [DIVERGENCES.md](DIVERGENCES.md) for the CT-2 catalog entry.
