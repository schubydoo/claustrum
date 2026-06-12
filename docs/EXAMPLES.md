# claustrum examples

Runnable snippets against a local daemon. They assume `claustrum` is on your
`PATH`. Because a connection stays open (stream notifications follow a response),
the streaming examples use a tiny Node helper that reads for a fixed window; the
simple request/response ones use `socat`.

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
# stdout frame decodes to "ping\n"; exit frame has exitCode -1 (signalled)
```

## Reattach / catch up via the replay buffer

```sh
# spawn an emitter, then later reattach from seq 0 to replay everything buffered:
run '[{"jsonrpc":"2.0","id":1,"method":"process.spawn",
       "params":{"id":"em","command":"sh","args":["-c","for i in 1 2 3; do echo $i; done"]}},
      {"jsonrpc":"2.0","id":2,"method":"process.reattach","params":{"id":"em","fromSeq":0}}]'
# the reattach replays the buffered stdout+exit frames, then returns
# {"id":2,"result":{"found":true,"running":false,"firstSeq":1,"lastSeq":4}}
```

A process survives the disconnect of the connection that spawned it — a *new*
connection can `reattach` to it and keep streaming. That is the reconnect path.

## Opt into `pid` + `startTime` — `wantPid` (claustrum extension)

!!! note "An addition, not reference behavior"
    The reference daemon has no `wantPid`; this is an opt-in **claustrum
    extension** (CT-1). Omit it — the default — and every frame is byte-identical
    to the reference. It does not change the original `spawn`/`reattach`
    behavior; it only *adds* fields when explicitly requested.

`process.spawn` and `process.reattach` accept `"wantPid":true`. When set, the
result carries the child's OS `pid` plus a `startTime` token, returned
identically on spawn and reattach for the same `id`, for PID-reuse / orphan
detection:

```sh
run '[{"jsonrpc":"2.0","id":1,"method":"process.spawn",
       "params":{"id":"w1","command":"sh","args":["-c","echo hi"],"wantPid":true}},
      {"jsonrpc":"2.0","id":2,"method":"process.reattach",
       "params":{"id":"w1","fromSeq":0,"wantPid":true}}]'
# {"id":1,"result":{"success":true,"pid":12345,"startTime":1718040000.12}}
# … stdout + exit stream frames …
# {"id":2,"result":{"found":true,"running":false,"firstSeq":1,"lastSeq":2,"pid":12345,"startTime":1718040000.12}}
```

Without `wantPid`, those same two replies are exactly `{"success":true}` and
`{"found":…,"running":…,"firstSeq":…,"lastSeq":…}` — the `pid`/`startTime` fields
are simply absent (`omitempty`).

`startTime` is an **opaque token**: persist it, then compare a daemon value
against a *later* daemon value for the **same `id`** to detect PID reuse. Do
**not** equality-compare it against an OS-read process start time (e.g. psutil
`create_time`) — it is the daemon's spawn-moment wall clock, not the kernel's
process-creation time.

## Extract a plugin tarball

```sh
run "[{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"files.extract_tar\",
       \"params\":{\"archivePath\":\"/path/to/plugin.tar.gz\",\"destDir\":\"/abs/out/dir\"}}]"
# {"id":1,"result":{"success":true,"fileCount":N}}   (expects a gzip tarball; destDir must be absolute, non-root)
```

## Shut it down

```sh
CLAUDE_RPC_TOKEN="$TOK" claustrum -stop -socket "$D/rpc.sock"
rm -rf "$D"
```

## Install/ensure the agent CLI (offline-verifiable)

```sh
claustrum -install -cli-dir "$D/cli" -cli-version 1.2.3 \
  -cli-url https://example.invalid/cli.zst -cli-checksum <sha256-of-the-zst>
# prints: __INSTALL_RESULT__{"serverVersion":"…","os":"linux","arch":"amd64","libc":"glibc",
#                            "cliPath":"…/cli/1.2.3","cliWasPresent":false,"cliError":"…"}
```

## Operational knobs (claustrum-only, all off the wire)

Start the daemon with the token on a file descriptor instead of a temp file —
the token never touches disk (fd `0` works too, for piping it on stdin):

```sh
claustrum -serve -socket "$D/rpc.sock" -token-fd 3 3< <(printf '%s' "$TOK")
```

Opt into Prometheus counters (connections, spawns/exits, reattaches,
stream/stdin bytes). No listener exists unless the flag is set; it serves
counts only and has no auth, so bind it to loopback:

```sh
claustrum -serve -socket "$D/rpc.sock" -token-file "$D/token" -metrics-addr 127.0.0.1:9090
curl -s http://127.0.0.1:9090/metrics | grep claustrum_
# claustrum_connections_total 2
# claustrum_process_spawns_total 1
# …
```

Quiet the daemon's stderr diagnostics (default emits everything, matching the
reference; the `[Server]`/`[process.Manager]`/… prefixes stay grep-able at any
level):

```sh
CLAUSTRUM_LOG_LEVEL=warn claustrum -serve -socket "$D/rpc.sock" -token-file "$D/token"
```
