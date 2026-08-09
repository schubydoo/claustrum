# claustrum architecture

A single Go binary, mode-switched by flag. Static (`CGO_ENABLED=0`). One
cross-platform dependency, `klauspost/compress` (zstd); two more compiled **only
into Windows builds** — `golang.org/x/sys` (Job Object teardown) and
`github.com/Microsoft/go-winio` (the opt-in `-listen-pipe` named-pipe transport).

## Source layout (flat `package main`)

| file | responsibility |
|---|---|
| `main.go` | flag parsing, mode dispatch, version resolution |
| `server.go` | `-serve` daemon: AF_UNIX listener, per-connection loop, concurrent dispatch, graceful shutdown (kills children, or leaves them with `-keep-children`) |
| `rpc.go` | JSON-RPC request/response types, error codes, dispatch + params gate |
| `results.go` | result structs (field order is part of the wire contract) |
| `methods_server.go` / `methods_files.go` / `methods_git.go` / `methods_process.go` | the 19 method handlers |
| `process.go` | process manager: registry, per-process seq, replay buffer, subscribers; captures the immutable `pid`/`startTime` pair behind the CT-1 `wantPid` opt-in |
| `bridge.go` | `-bridge` relay and `-stop` |
| `install.go` | `-install`: download/verify/extract/prune + `__INSTALL_RESULT__` facts |
| `fetch`-style helpers live in `install.go` | HTTP GET + SHA-256 + in-process zstd |
| `logging.go` | leveled stderr logger (`CLAUSTRUM_LOG_LEVEL`); level tag precedes the byte-intact `[Component]` prefixes |
| `metrics.go` | opt-in Prometheus counters at `/metrics` (`-metrics-addr`; no listener by default) |
| `sysproc_unix.go` / `sysproc_windows.go` | whole-tree kill: process group (setpgid + negative-pid signal) vs Windows Job Object (`KILL_ON_JOB_CLOSE`); the `-keep-children` POSIX-only policy (`honorKeepChildren`) |
| `pipetransport.go` | `-listen-pipe` shared helpers: `rpc.pipe` name-file lifecycle (atomic write / remove), owner-only SDDL builder, pipe-name + instance-id generation (all platform-neutral) |
| `pipetransport_windows.go` / `pipetransport_other.go` | the optional Windows named-pipe listener (`startPipeTransport` via go-winio, owner-only DACL) vs the non-Windows no-op stub + `honorListenPipe` warning |
| `detach_unix.go` / `detach_windows.go` | daemonize attr (setsid vs DETACHED_PROCESS) |
| `shellenv_unix.go` / `shellenv_windows.go` | login-shell PATH extraction (Unix) / no-op (Windows) |

The JSON-RPC surface is identical on every OS; only the `*_unix.go` / `*_windows.go`
files differ.

## The three runtime roles

### 1 · CLI-version manager (`-install`)

Ensures the pinned `claude` CLI is present under `-cli-dir`.

- If `<cli-dir>/<cli-version>` exists *and* is runnable (`<cli> --version`
  exits 0), it's kept as-is. The probe has **no deadline by default**, matching the
  reference on every input measured (it was still running at 45 s on a CLI that
  never answers, and installed one that answered at 90 s; above that, unprobed); `-cli-probe-timeout` / the `cli-probe-timeout` config key opts into
  one, and then a slower but working CLI fails this guard — see divergence D11.
- Otherwise the blob is acquired from one of two sources:
    - `-cli-zst` — a local `.zst` (consumed once **decompression** succeeds, even if the install then fails the runnability probe); checksum-verified
      **only when a `-cli-checksum` is supplied** — a conditional divergence from
      the reference, activated by the caller; see [PROTOCOL.md](PROTOCOL.md).
    - `-cli-url` — downloaded, SHA-256-verified against `-cli-checksum`
      *unconditionally* (even an empty checksum fails).
- The blob is zstd-decompressed, `chmod 0755`, and re-checked for runnability
  **at a temp path, then atomically renamed into place** — an interrupted
  install never leaves a half-written CLI.
- The directory is pruned to the `-cli-keep` most-recent files (by mtime,
  default 3).
- It prints one line: `__INSTALL_RESULT__{json}`.

### 2 · Daemon / process supervisor (`-serve`)

Self-daemonizes (detaches from the controlling session, reparents to init on
Unix), opens the `0600` socket, and supervises spawned children.

- The auth token arrives via `-token-file` (read once, unlinked) or
  `-token-fd` (read from an open descriptor and forwarded to the detached
  child over a pipe — never touches disk).
- Once listening, the daemon **persists the token to `daemon.token` (`0600`)
  beside the socket** (atomic temp-write + rename) so a reconnecting client can
  re-authenticate after that source is gone, and **unlinks it on graceful
  shutdown** — behavioral parity with reference `5db5e4a` (`tokenpersist.go`; see
  [PROTOCOL.md → Token persistence](PROTOCOL.md#token-persistence-daemontoken)).
- The self-daemonize re-exec is gated by an **internal** sentinel,
  `CLAUSTRUM_DAEMON_CHILD` — deliberately claustrum-namespaced, *not* the
  reference's `CLAUDE_SSH_DAEMON_CHILD`, which a surrounding claude-ssh session
  exports to every descendant and would make the launcher skip its own
  daemonize/token-forward path. The child unsets the sentinel after reading it.
  Reference parity is kept separately: `daemonizeWithToken` still sets
  `CLAUDE_SSH_DAEMON_CHILD=1` in the daemon's environ so `process.spawn` children
  inherit it exactly as the reference does (see [PROTOCOL.md](PROTOCOL.md)).
- On Unix, interactive PATH extraction from the login shell runs concurrently
  (goroutine), so a slow login shell does not delay socket availability.
- With **`-listen-pipe`** (Windows-only, opt-in), it *additionally* opens a Windows
  named pipe serving the identical JSON-RPC dispatch (a second `acceptLoop` over
  the same `serveConn`), publishes the chosen pipe name to `rpc.pipe` beside the
  socket before accepting, and removes it on graceful shutdown. Strictly additive
  — the socket path is unchanged.
- It makes **no** outbound network connections, and no inbound listener beyond
  the socket unless the operator opts into `-metrics-addr` (TCP) or `-listen-pipe`
  (a local, owner-only Windows named pipe).

### 3 · JSON-RPC multiplexer + replay

One persistent socket, concurrent request dispatch, in-band token auth, and a
per-process frame buffer (`process.*`) that lets a late or reconnecting client
catch up via `reattach`.

(`-bridge` is a fourth, trivial mode: a dumb stdio↔socket relay — what an SSH
session attaches to. It injects no auth.)

## Inherited wire bytes

Some of claustrum's byte-identical output is produced by **Go's standard library,
not by claustrum's code**. The reference is also Go, so its stdlib is doing
unpaid parity work for us. That is a real asset — and it is worth being explicit
about, because inherited agreement is agreement nobody verified.

Two things follow. A Go upgrade that changes an escaping rule moves the wire with
no diff in this repo. And a reimplementation in another language would have to
re-derive every row below by hand; the list is the honest cost estimate for that.

The rows marked **inherited** are decided by the stdlib. **Deliberate** means
claustrum chose the mechanism *because* it reproduced the reference — inherited
behaviour that someone measured and adopted, rather than inherited behaviour
nobody looked at.

| Surface | Where | Source | Status |
|---|---|---|---|
| `id` echo: `1.0`→`1`, `1e2`→`100`, big-int precision loss, map keys sorted | `rpc.go` (`request.ID interface{}`) | `encoding/json` round-trip through `interface{}` | **deliberate** — `json.RawMessage` reproduced none of it; measured |
| Invalid UTF-8 in a result string → one `\ufffd` escape per bad byte; NUL → `\u0000` | `files.read` `content`, any string field | `encoding/json` encode | inherited |
| `<` `>` `&` → `\u003c` `\u003e` `\u0026` | every string field and error message | `encoding/json` HTML escaping, on by default | inherited |
| Invalid UTF-8 in a **request** param replaced with U+FFFD before dispatch sees it | every path param | `encoding/json` **decode** | inherited |
| `chdir <p>: stat <p>: no such file or directory` | error messages built from `err.Error()` | `os` `*PathError` (`op + " " + path + ": " + errno`) | inherited |
| Frame `data` alphabet and `=` padding | `process.*` stdout/stderr frames | `base64.StdEncoding` | inherited |
| Result field ORDER | `results.go` | `encoding/json` emits struct fields in declaration order | **deliberate** — order is chosen to match, and results are structs, never maps, precisely because maps sort |
| Path cleaning for `~`-prefixed paths | `expandpath.go` | `filepath.Join`/`Clean` | **deliberate** — the lexical clean was measured and matched (#205) |

The decode-side row is the one with a user-visible consequence: **a file whose
name is not valid UTF-8 cannot be addressed through the protocol at all**, on
either daemon. The request decoder substitutes U+FFFD before any method runs, so
the daemon operates on a name that does not exist. This is not a claustrum
limitation to fix; it is the reference's behaviour, inherited by the same route.

`inherited_encoding_test.go` and `inherited_encoding_unix_test.go` pin these
against regression. They assert escape TEXT (`\ufffd`) for encode-side rules and
the U+FFFD CHARACTER for the decode-side one — which side substituted is exactly
what distinguishes them.

## Operational logging

Mirrors the reference daemon:

- The readiness banner (`… remote server listening on <socket>`) goes to
  **stdout**, without a prefix. Only its product name differs (rebranded).
- Every operational/diagnostic line — `[Server]` connection lifecycle,
  `[process.Manager]` spawn/stream/exit, `[shellenv]`, `[frameSink]` — goes to
  **stderr** via the standard `log` package (a `2006/01/02 15:04:05` timestamp
  prefix).
- These logs are not part of the JSON-RPC wire contract, but they are kept
  byte-faithful (sans timestamp/PID) so anything tailing the daemon log behaves
  identically.

A tiny leveled logger
([`logging.go`](https://github.com/schubydoo/claustrum/blob/main/logging.go))
sits in front of those calls so operators can quiet the daemon:

- Each line carries a level (`DEBUG`/`INFO`/`WARN`/`ERROR`), emitted as a short
  tag *before* the `[Component]` prefix — `INFO  [Server] New connection
  from: …` — so the prefixes stay byte-intact and any grep for `[Server]`,
  `[process.Manager]`, `[frameSink]`, or `[shellenv]` keeps matching.
- The threshold is set once at startup from `CLAUSTRUM_LOG_LEVEL`
  (`debug`|`info`|`warn`|`error`).
- It **defaults to `debug`** — an unset (or unrecognized) value emits exactly
  what the daemon always has; raising it drops everything below the chosen
  level.
- Purely a local diagnostic knob: it touches stderr only, never the wire.

## `__INSTALL_RESULT__` facts

```jsonc
{
  "serverVersion": "<daemon id>",
  "os":   "linux",            // GOOS
  "arch": "amd64",            // GOARCH
  "libc": "glibc",            // or "musl"; "" off linux (no probe). On linux an ldd
                              // slower than -libc-probe-timeout falls back to glibc
                              // when that is set; no deadline by default (D14).
                              // The driver uses
                              // this field to pick which CLI build to download —
                              // a third-binary claim; see the provenance note below.
  "cliPath": "<cli-dir>/<cli-version>",
  "cliWasPresent": false,     // true only if it existed AND answered --version — within
                              // -cli-probe-timeout when that is set; no deadline by default (D11)
  "cliError": "…"             // omitted on success
}
```

**`cliError` strings.** Every error `ensureCLI` returns lands here verbatim,
**with the wrapping prefix its phase adds** — that is the form a driver actually
sees. Grouped by phase rather than left as prose, and no count is quoted; re-derive
from `ensureCLI` if you need one:

| phase | string |
|---|---|
| version check | `cli version "<v>" must be a single path component` |
| | `cli version "<v>" collides with the install temp sweep` |
| | `cli version "<v>" collides with the install download blob` |
| source | `cli <v> missing and no --cli-url or --cli-zst provided` — also reached when a **present, working** CLI answers `--version` more slowly than an opted-in `-cli-probe-timeout` and no source flag was given (D11; unreachable at the default, which has no deadline) |
| | `opening input: <err>` (`-cli-zst` read) |
| download | `download failed: <err>` — **transport** failure only, plus `context deadline exceeded (…)` when the opt-in `-cli-download-timeout` bound fires (D12; off by default, so unreachable unless asked for) |
| | `download failed with status <code>` — **non-200**, no URL, no reason phrase |
| | `download failed: response exceeds <n> bytes` |
| verify | `checksum mismatch: expected=<a>, actual=<b>` |
| install | `mkdir cli dir: <err>` |
| | `staging cli: <err>` |
| | `decompressing: <err>` |
| | `decompressing: decompressed CLI exceeds <n> bytes` |
| | `installed cli at <path> is not runnable` |
| | `clearing stale dir at <path>: <err>` |
| | `staging file vanished before install: <err>` |

The two download forms are deliberately worded differently, matching the
reference: only the transport failure carries the `download failed: ` prefix, and
the status form omits the URL so a signed URL cannot reach whatever captures the
`__INSTALL_RESULT__` line. A bare `chmod`/`rename` failure propagates as the raw
Go error.

⚠️ **These strings are not free-form diagnostics — the driver reads them.** Claude
Desktop classifies `cliError` by *text*: a message shaped like a disk-full failure
is surfaced as a terminal error with an actionable code, and messages that do not
match are treated as retryable rather than terminal. ⚠️ **The claim is about that
classification only** — *how* the driver retries is unobserved, so do not read a
particular retry shape out of it. So changing the wording of a
row above — or adding a guard whose error pre-empts one — can change what a user is
told, even when no JSON-RPC frame moves. D10's opt-in cap is the worked example.

### Driver claims and their provenance

⚠️ **These are a different class of claim from the rest of these docs.** Three are
load-bearing: the `cliError` classification above, **`libc` deciding which CLI build
the driver downloads**, and **"Desktop owns the argv"**. All three describe a
**third binary** — the driver — not the reference daemon and not claustrum, so the
reference-vs-claustrum harness cannot confirm or refute any of them and no
`scratch/` fixture covers them.

⚠️ **"The harness cannot settle it" is not the same as "unverifiable", and none of
the three has been settled.** Each has a fixture that would settle it — run against
the **driver**, so the arms are two inputs to one client, never two daemons. Each
needs a control that could actually come out wrong; an arm entailed by the claim
proves nothing:

| claim | fixture | control that must fire |
|---|---|---|
| `cliError` classification | two `-install` failures whose **messages** straddle the disk-full shape; observe **a retry of any shape** vs terminal report — the claim is about classification, so keying the discriminator to one retry shape would read a differently-shaped retry as a falsification | a genuine disk-full failure — the shape the claim is built on — observed as **terminal with an actionable code**, proving the terminal side is reachable at all |
| `libc` build selection | a stub `ldd` printing a musl banner and **exiting 0**, with `/lib/ld-musl-*.so.*` absent or masked (otherwise the glob short-circuits and `ldd` never runs); then see which build the client fetches | point the client at an ordinary glibc host, where the daemon reports `glibc`, and confirm it fetches the **glibc** build — otherwise the musl arm cannot be told apart from the client's default. *(That the stub took effect is a precondition on the fixture, not the control.)* |
| argv | the setup UI, **a capture of the argv Desktop actually passes**, and an enumeration of Desktop's own config files | a setting the client is *known* to read must turn up in the enumeration — otherwise a null result means the enumeration missed everything. ⚠️ UI and capture done (below); the enumeration is still unrun, and the UI half is one look at one unrecorded build |

**Evidence for the argv claim, scoped to what was looked at:** the shipped client's
"Add SSH connection" dialog offers *Name*, *SSH Host*, *SSH Port* and *Identity
File*, and its folder step is a remote directory browser — **no field for daemon
arguments in either**. Reported by the maintainer as a daily user of the shipped
client, 2026-08-07; the client build was not recorded.

**Measured 2026-08-08, one cold start:** the argv Desktop passes was recorded by
logging every invocation on the host it drives — seven in all, covering the five
modes below (`--install` and `--bridge` came twice each, byte-identical both
times). The log also kept each invocation's parent chain, and for `-install` the
`__INSTALL_RESULT__` line it printed:

| mode | argv |
|---|---|
| `--version` | *(no arguments)* |
| `--bridge` | `--socket <sock>` |
| `--serve` | `--socket <sock> --token-file <file>` |
| `--stop` | `--socket <sock>` |
| `--install` | `--cli-dir <dir> --cli-version <v> --cli-keep 3` |

All seven descend from the SSH session Desktop opened, and all spell their flags
with a double dash. The log also holds three single-dash `-stop` runs whose parent
is a login shell instead; those are excluded on the parent chain, which is what
justifies it — the dash spelling only corroborates.

⚠️ **Read what this does and does not discriminate.** "No claustrum opt-in flag
appeared" would be an **entailed arm** — those flags are claustrum's own additions,
absent from the daemon Desktop was built against, so a client that knew nothing of
them and a client that refused to pass them produce the same log. It proves
nothing, and the rule at the top of this section says so. What *could* have come
out otherwise, and did not, is the **shape**: **no argument appears that Desktop
has no use for** — no free-form token, no `--` pass-through, no residue of an
extra-arguments slot. Every flag present is one the daemon needs to run. ⚠️ That is
a claim about which arguments appear, **not** about where their values came from:
`--cli-keep 3` is exactly the shape a settings field would fill, and the capture
cannot tell a Desktop-computed value from an operator-edited one.

Two further limits follow from that. Whether Desktop reads a config file of its own
was **not enumerated**, and a config file it turns into argv is **one of the two
routes** this claim's own reopen trigger names, so it would change the argv above
without contradicting a single row. (Forwarded environment is *not* such a route —
the trigger disqualifies it below, since nothing reads the environment for these
knobs.) And **both `-install` runs were cache hits** — each answered
`cliWasPresent:true`, and neither argv carried a source flag, so neither could
have fetched anything (`install.go:49-66`). The argv of an install that actually
fetches is unobserved.

So the coverage is uneven and worth stating exactly. The **`-serve` argv** behind
D3, D4 and D5 was seen whole. On `-install`, only the two **cache-hit**
invocations' argv was seen; a fetching one was never observed at all. That is not a clean split
by D-number: D11 and D14 run on **both** paths (`isRunnable` has a second site
after decompression, and `detectLibc` sits above the branch entirely), so they were
exercised on the shape that was observed and also on the shape that was not. D10,
D12 and D13 become reachable only on the fetching path, so nothing about their
argv was observed.

⚠️ One cold start, one host, one unrecorded build: the evidence supports "Desktop
passed no such argument on the occasions observed", not "Desktop cannot".

- **Reopen trigger for the argv claim:** Claude Desktop gaining a way for an
  operator to influence the daemon's argv — a settings field, or a config file it
  reads and turns into argv. That would make a flag-only opt-in sufficient **for
  Desktop-driven hosts**. ⚠️ It would *not* moot the `claustrum.conf` key: the key
  is read from the executable's own directory (`os.Executable`), so it serves any
  other driver — including `clauster`, named as a supported one below — regardless
  of what Desktop grows. An env var Desktop forwarded would not qualify either;
  nothing in `config.go` or `main.go` reads the environment for these knobs, so
  forwarding one changes nothing.
- **What rests on it:** **D3, D4, D5, D10, D11, D12 and D14** — every "why it
  stopped being always-on" argument turns on the person who pays having no way to
  decline. The claim is load-bearing on both argv surfaces: D3, D4 and D5 are
  `-serve` flags, D10, D11, D12 and D14 are `-install` ones — plus
  the **"(opt-in)" tagging convention** in IMPROVEMENTS, which *defines* opt-in as a
  flag **and** a config key on exactly this ground, and so binds every future
  **D-numbered** entry. (The CT block uses the tag in a looser "off unless asked
  for" sense — CT-1 is caller-activated and CT-3 is the config mechanism itself —
  so it does not rest on this claim.)
  The trigger is recorded once here rather than eight times, because it is one
  claim shared by all of them.

*(Not the only other driver claims in these docs — D6's and D7's clause-(b)
evidence rests on what Desktop emits as `-cli-version`. That one is still untracked
and unprovenanced.)*

Treat them as design constraints worth respecting, not as measured parity results;
anything that depends on one should say so and carry a reopen trigger **that would
falsify the claim it rests on** — a trigger that fires on something else does not
count. ⚠️ **The dependents list below is maintained by hand and has been incomplete
every time it was checked.** Treat it as the best-known set, not a proof of completeness, and add
to it rather than trusting it.

Below are the dependents of the `cliError` and `libc` claims, where each entry needs
its own trigger. *(The argv claim's dependents are recorded with the claim above
instead, because one trigger covers all of them. D10, D11 and D14 appear in both
roles — below for the `cliError` (D10, D11) or `libc` (D14) claim, above for the
argv one. D14 joined the argv list when it was flipped on 2026-08-08.)*

- **D13's cost-free reading** rests on the `cliError` claim (D13 has no accepted
  always-on justification — it is in IMPROVEMENTS' unresolved group).
- **D13's 2026-08-08 reopen measurement** rests on it a second time, and this is a
  *different* dependency from the one above: the fixture modelled the driver's next
  move after a failed install as a `-cli-zst` retry. ⚠️ **That was an assumption of
  the fixture's design, not something the `cliError` claim establishes and not
  something anyone has observed** — the claim says Desktop treats a non-disk-full
  `cliError` as retryable, which does not say *how* it retries. If the follow-up is
  some other shape, that run measured the wrong one and its "same end state either
  way" result says nothing about the driver. The result still stands as a claim
  about **the two binaries**; only its bearing on the reopen condition depends on
  this. 🔴 **Reopen trigger:** any
  observation of what Desktop actually does after an `-install` reports a
  `cliError` — D13's own trigger fires on how Desktop *classifies* the string, which
  is a different half and would not catch a Desktop that classifies as assumed but
  retries some other way.
- **D14's residual delta** is only more than cosmetic because of the `libc` claim.
  🔴 **No falsifying reopen trigger:** its entry has one, but it fires on a musl host
  the loader glob misses and on a measurement of the reference's bound — neither
  bears on whether Desktop uses `libc` to pick the build.
- **D10's opt-in cap** — the worked example above — rests on the `cliError` claim
  too: what a caller loses by capping below free space is the disk-full
  *classification*, not the string alone. ⚠️ One plausible Desktop change —
  broadening its terminal match to any `decompressing:` error — fires this entry's
  trigger and D13's at once, since the cap's own message is a `decompressing:` error.
- **Clause (c)'s "error strings are not free" rider** in IMPROVEMENTS — a
  *rule-level* dependent rather than a D-number, since it binds every future
  clause-(c) entry. 🔴 **No reopen trigger at all.**
- **D11's retraction of "a client reads this field, it does not parse prose"** — if
  Desktop turned out not to parse `cliError`, half of that retraction collapses.
  (Only half: the other half rests on a separate absence — nothing on record shows
  any client behaving differently when `cliWasPresent` changes.)

## Deployment lifecycle (how a driver uses it)

A driver (Claude Desktop, or your own tool such as clauster) typically:

1. probes the remote OS/arch,
2. ensures the daemon binary is present on the remote (e.g. uploads it),
3. runs `claustrum -install …` to ensure the agent CLI is present,
4. starts `claustrum -serve -socket … -token-file …`,
5. attaches per session with `claustrum -bridge -socket …` and speaks JSON-RPC
   (in-band auth) through it,
6. drives the agent and any MCP servers as `process.spawn` children, feeding each
   child's stdin via `process.stdin` (base64) and reading its stdout via the
   stream notifications.

## Concurrency & replay model

- Each connection's requests run in their own goroutine; the per-connection writer
  is mutex-serialized so responses and stream frames interleave safely.
- Each managed process has: a monotonic `seq`, an append-only frame buffer, and a
  set of subscriber connections. `spawn` subscribes the spawning connection;
  `reattach` subscribes the requester and replays buffered frames with
  `seq > fromSeq`. A dead subscriber is detached (`[frameSink] replay write
  failed, detaching`).
