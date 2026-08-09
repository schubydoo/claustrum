# Changelog

All notable changes to claustrum are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/), and this project adheres to
[Semantic Versioning](https://semver.org/).

## 1.8.0 (2026-08-09)

[Compare with 1.7.3](https://github.com/schubydoo/claustrum/compare/v1.7.3...v1.8.0)

### Features

- `-install` no longer bounds the `-cli-url` download at 5 minutes by default, so a slow-but-honest download the reference completes now succeeds rather than failing with `cliError "download failed: context deadline exceeded (Client.Timeout or context cancellation while reading body)"`; opt-in via `-cli-download-timeout` or the `cli-download-timeout` key in `claustrum.conf`. ([#246](https://github.com/schubydoo/claustrum/pull/246))
- `-install` no longer bounds the `<cli> --version` runnability probe at 15 seconds by default, so a slow-but-working CLI installs instead of failing with `cliError "installed cli at <path> is not runnable"` and having its staged binary deleted; opt-in via `-cli-probe-timeout` or the `cli-probe-timeout` key in `claustrum.conf`. ([#245](https://github.com/schubydoo/claustrum/pull/245))
- `-install` no longer caps the decompressed CLI or the download body at 512 MiB by default, so a large CLI installs instead of failing with a `cliError` the reference daemon never produces; opt-in via `-max-cli-bytes` or the `max-cli-bytes` key in `claustrum.conf`. ([#238](https://github.com/schubydoo/claustrum/pull/238))
- `files.extract_tar` no longer caps extraction at 512 MiB by default, so a large tree extracts instead of failing with an error the reference daemon never produces; the cap is now opt-in via `-max-extract-bytes` or the `max-extract-bytes` key in `claustrum.conf`. ([#236](https://github.com/schubydoo/claustrum/pull/236))
- `files.read` no longer refuses a non-regular path by default, so reading a character device such as `/dev/null` returns `{"content":"","exists":true}` as the reference daemon does instead of a claustrum-only `-32602 "files.read: not a regular file"`; the guard is now opt-in via `-files-read-regular-only` or the `files-read-regular-only` key in `claustrum.conf`, which restores the bound on a writerless FIFO and on unbounded device reads. ([#250](https://github.com/schubydoo/claustrum/pull/250))
- The daemon no longer bounds every `git` invocation at 60 seconds by default, so a slow-but-honest git on a large or cold repository completes instead of being SIGKILLed into a claustrum-only `-32603 "signal: killed"` (or a `git worktree remove timed out after …`) where the reference showed no such deadline at the durations probed; now opt-in via `-git-timeout` or the `git-timeout` key in `claustrum.conf`. ([#248](https://github.com/schubydoo/claustrum/pull/248))
- `-install` no longer bounds the linux `ldd --version` libc probe at 5 seconds by default, so a slow `ldd`'s answer stands instead of being killed into a fallback classification — the reference daemon showed no such deadline against a stalled `ldd` at the durations probed; the deadline is now opt-in via `-libc-probe-timeout` or the `libc-probe-timeout` key in `claustrum.conf`. ([#251](https://github.com/schubydoo/claustrum/pull/251))

### Fixes

- The `exit` stream frame now arrives at most 5 seconds after a spawned process exits, instead of waiting forever when the command left a background grandchild holding its stdout. ([#186](https://github.com/schubydoo/claustrum/pull/186))
- A reply's `id` is now the request's id decoded and re-encoded rather than the raw bytes echoed back, so a non-integer id such as `1.0`, `1e2` or `{"b":1,"a":2}` is canonicalized exactly as the reference returns it. ([#188](https://github.com/schubydoo/claustrum/pull/188))
- `-install` now creates the CLI directory chain owner-only (`0700`) like the reference, instead of `0755` which left the installed CLI world-traversable. ([#191](https://github.com/schubydoo/claustrum/pull/191))
- A non-200 CLI download now reports `download failed with status <code>` like the reference, instead of a differently-worded message that also leaked the download URL into the `__INSTALL_RESULT__` line. ([#197](https://github.com/schubydoo/claustrum/pull/197))
- A zero-byte `-token-file` is now a fatal startup error instead of producing a daemon that listens but can never be authenticated to, and the daemon now logs a failed token-file unlink, a child stream read error, and a discarded stdin queue rather than passing over them in silence. ([#200](https://github.com/schubydoo/claustrum/pull/200))
- Report the reference daemon's error text for `files.list` on a non-directory, `files.extract_tar` failing to open the archive, and `process.spawn` with an unusable `cwd`. ([#175](https://github.com/schubydoo/claustrum/pull/175))
- Expand a leading `~` to the home directory on every path parameter, so `files.*`, `git.*` and `process.spawn` no longer fail or create a literal `~` directory. ([#176](https://github.com/schubydoo/claustrum/pull/176))
- A `~`-prefixed path is now cleaned lexically like the reference — `~/link/../x` means `~/x` even when `link` is a symlink, `~//f` and `~/a/./b` and a trailing `~/wt/` collapse, and bare `~` alone stays verbatim — which is wire-visible on eight frames, most sharply on `files.stat`/`files.validate` where a trailing separator on a regular file previously answered `not a directory` and now succeeds. ([#205](https://github.com/schubydoo/claustrum/pull/205))
- `files.extract_tar` now carries the reference's `create <entry>: ` prefix when an archive entry's target lands on an existing directory, so the error field reads `create ../sub: open <dest>: is a directory` instead of the bare Go error. ([#226](https://github.com/schubydoo/claustrum/pull/226))
- `files.extract_tar` now refuses a Windows volume or UNC share root as `destDir` — previously only the literal `/` was refused, so `C:\` passed the guard and reached the `os.RemoveAll` that clears the destination before extracting. ([#225](https://github.com/schubydoo/claustrum/pull/225))
- `files.read` now applies the reference's 256 KiB (262144-byte) default cap when `maxBytes` is absent, zero, or negative, instead of reading the file without any limit; an explicit positive `maxBytes` is still honored verbatim. ([#189](https://github.com/schubydoo/claustrum/pull/189))
- Wait for the login-shell PATH extraction before the first `process.spawn` builds its child environment, so the first command finds binaries in `~/.local/bin` and nvm. ([#168](https://github.com/schubydoo/claustrum/pull/168))
- `git.list_branches` reads git's stdout only and reports a failed `for-each-ref` as `-32603 exit status <n>`, so a broken ref no longer lists git's `warning:` line as a branch and a corrupt refs database no longer returns that failure as branch names. ([#213](https://github.com/schubydoo/claustrum/pull/213))
- `git.status` and `git.list_branches` now recognize a bare repository and a `.git` directory as repositories, and `git.worktree_create` ignores a `sourceBranch` that is not a local branch instead of failing the request. ([#178](https://github.com/schubydoo/claustrum/pull/178))
- `git.status` now trims the whole porcelain output before splitting it, so the first change loses its leading space and every later one keeps it, matching the reference. ([#185](https://github.com/schubydoo/claustrum/pull/185))
- Keep the leading space on `git.status` porcelain lines so a client can tell a staged change from an unstaged change. ([#167](https://github.com/schubydoo/claustrum/pull/167))
- `git.status` now reads only git's stdout, so warnings git writes to stderr (an unreadable `core.excludesFile`, for example) no longer appear as entries in `changes` and no longer make a clean repo report as dirty. ([#193](https://github.com/schubydoo/claustrum/pull/193))
- The daemon now recovers from a panic in any request handler instead of crashing, replying `-32603 "recovered panic: <v>"` and staying up for other connections, so a handler bug can no longer orphan managed child processes or leave a stale socket behind. ([#243](https://github.com/schubydoo/claustrum/pull/243))
- `-install` no longer executes `ldd` when the musl loader marker already answers the question, and it consumes the local `-cli-zst` blob once decompression succeeds rather than only on a fully successful install — both matching the reference. ([#216](https://github.com/schubydoo/claustrum/pull/216))
- `-install` now removes leftover `.fetch-*` download temporaries, which previously counted against `-cli-keep` and could delete every real CLI version, and it prunes only after an install that succeeded. ([#180](https://github.com/schubydoo/claustrum/pull/180))
- `-install` stages the CLI at `.fetch-<random>` and sweeps stray `*.zst` blobs so leftovers no longer take a `-cli-keep` slot; an occupied `cliPath` is cleared instead of failing with a rename error; a failed cli-dir creation carries the `mkdir cli dir: ` prefix; `-cli-version` must name a single entry inside `-cli-dir`, so a version reaching outside via `..` or a symlink can no longer delete a directory there, and one the orphan sweep claims (`.fetch-*` or `*.zst`) is refused; the new CLI is renamed into place before the old one is cleared, so two installs in one cli-dir cannot leave it empty; if another install reclaims its staging file, it retries once instead of failing. ([#196](https://github.com/schubydoo/claustrum/pull/196))
- `process.killAndWait` clamps `timeoutMs` at 30000 ms instead of 600000 ms, so a caller asking for a longer grace against a signal-ignoring child now gets its exit frame at 30 s, matching the reference. ([#214](https://github.com/schubydoo/claustrum/pull/214))
- The login-shell PATH is now applied only to spawned children, not the daemon's own environment, so the daemon no longer resolves its own `git` through the user's PATH; a non-executable `$SHELL` falls back to a usable shell instead of failing extraction, and a missing PATH sentinel is logged with the shell's output. ([#199](https://github.com/schubydoo/claustrum/pull/199))
- Login-shell PATH extraction now gives up after 4 seconds instead of 10, matching the reference, so a slow login shell no longer delays the first `process.spawn` or hands spawned children a PATH the reference would not have applied. ([#190](https://github.com/schubydoo/claustrum/pull/190))
- `-install` now reports `musl` when a musl loader of any architecture is present, and reports no libc at all on macOS and Windows, matching the reference daemon. ([#183](https://github.com/schubydoo/claustrum/pull/183))
- `process.kill` and `process.killAndWait` now match signal names case-sensitively and no longer map `QUIT` to `SIGQUIT`, so a request naming `quit` sends `SIGTERM` like the reference instead of a `SIGQUIT` that dumps core. ([#203](https://github.com/schubydoo/claustrum/pull/203))
- Reject a negative `offset` on `process.stdin` and a negative `fromSeq` on `process.reattach`, which before silently dropped the first bytes of the child's stdin. ([#173](https://github.com/schubydoo/claustrum/pull/173))
- `files.stat`, `files.read` and `files.validate` now report a stat failure other than "does not exist" instead of the missing-path result — e.g. a path component that is a file. ([#177](https://github.com/schubydoo/claustrum/pull/177))
- Exited processes are now dropped from the process table 15 minutes after they finish, freeing their replay buffers, so `process.reattach` on a long-finished id reports `found:false` as the reference does. ([#187](https://github.com/schubydoo/claustrum/pull/187))
- `process.killAndWait` now waits 7 seconds for a SIGKILL'd process to be reaped instead of 5, matching the reference daemon, so a child that is reaped between those two points is reported as dead rather than still running. ([#242](https://github.com/schubydoo/claustrum/pull/242))
- `process.reattach` now transfers the frame stream to the reattaching connection like the reference, so a previously attached connection stops receiving instead of both getting every frame after a resume. ([#202](https://github.com/schubydoo/claustrum/pull/202))
- `files.extract_tar` now refuses a `destDir` that is or contains the home directory, so a tilde or relative path can no longer reach the recursive delete it performs before unpacking — paths under home, such as `~/.claude/…`, are unaffected. ([#231](https://github.com/schubydoo/claustrum/pull/231))
- `git.worktree_remove` now refuses a `worktreePath` that is or contains the home directory, so a tilde or relative path can no longer reach the recursive delete it performs when git fails — the method previously had no path guard of any kind. ([#232](https://github.com/schubydoo/claustrum/pull/232))
- `-serve` now writes its output to `remote-server.log` beside the socket (mode 0600) instead of the launcher's stdout and stderr, and the file survives a graceful shutdown. ([#181](https://github.com/schubydoo/claustrum/pull/181))
- The per-process replay buffer counts its 16 MiB cap in serialized frame bytes, not base64 payload alone, so `process.reattach`'s `firstSeq` matches the reference on small-frame workloads instead of retaining ~9% too many frames. ([#215](https://github.com/schubydoo/claustrum/pull/215))
- Cap the per-process replay buffer at 16 MiB instead of 50 MiB, so `process.reattach` reports the same `firstSeq` floor as the reference daemon. ([#174](https://github.com/schubydoo/claustrum/pull/174))
- `git.info` emits `repoSlug` only for a canonical `github.com` remote whose owner and repo pass the reference's charset rules, not for any remote URL with two path segments, so GitLab, Bitbucket, self-hosted, and `www.github.com` remotes report `""`. ([#192](https://github.com/schubydoo/claustrum/pull/192))
- `claustrum -serve` with neither `-token-file` nor `-token-fd` now daemonizes and refuses to start in the detached child, so the launcher reports its ~10s accept timeout as the reference does, instead of exiting immediately with the specific reason. ([#217](https://github.com/schubydoo/claustrum/pull/217))
- `-serve` now creates a missing socket directory (mode `0700`) instead of refusing to start, and waits until the daemon is accepting before returning, so a client connecting immediately after launch no longer races an unopened socket. ([#195](https://github.com/schubydoo/claustrum/pull/195))
- claustrum now prefers zsh over bash when `$SHELL` is unusable and discards a login PATH whose extraction timed out, both matching the reference. ([#212](https://github.com/schubydoo/claustrum/pull/212))
- `server.shutdown` is no longer authenticated, matching the reference, so `claustrum --stop --socket <sock>` works with no `CLAUDE_RPC_TOKEN`; every other method still requires auth. ([#206](https://github.com/schubydoo/claustrum/pull/206))
- `process.kill` and `process.killAndWait` now deliver a non-KILL signal to the spawned process alone, not its whole process group, so a graceful stop no longer kills the background jobs the command started, while `escalate:true` still sweeps the group with SIGKILL as the reference does. ([#194](https://github.com/schubydoo/claustrum/pull/194))
- Pipelined `process.stdin` requests on one connection now reach the child in the order they were sent, instead of racing and scrambling the byte stream. ([#201](https://github.com/schubydoo/claustrum/pull/201))
- `-stop` now gives up waiting for the daemon's reply after 2 seconds like the reference instead of blocking forever, and no longer echoes that reply frame to stdout. ([#198](https://github.com/schubydoo/claustrum/pull/198))
- `claustrum -stop` now unlinks the socket path on every exit path — including when the dial fails and no daemon was ever reached — matching the reference, so a stale socket no longer blocks the next `-serve` from binding. ([#217](https://github.com/schubydoo/claustrum/pull/217))
- `git.worktree_create` now copies `.claude/` and the files named by `.worktreeinclude` into the new worktree, which previously came up with tracked files only. ([#182](https://github.com/schubydoo/claustrum/pull/182))
- `git.worktree_remove` now deletes the branch given in `branchName`, including a branch that is not merged, in place of leaving it in the repository. ([#179](https://github.com/schubydoo/claustrum/pull/179))
- `git.worktree_remove` now removes the worktree directory itself when git refuses (e.g. a locked worktree) and reports `success:false` with an `error` when that cleanup also fails, instead of answering `success:true` while leaving the directory in place; a `git worktree remove` that hits claustrum's opt-in git deadline is reported rather than treated as a refusal, so a wedged git no longer triggers that cleanup. ([#204](https://github.com/schubydoo/claustrum/pull/204))

### Performance

- `-install` streams the download to a temporary file and hashes it as it arrives, and hashes and decompresses the local `-cli-zst` blob straight from disk, so peak memory is flat in the blob size (measured 886 MB to 10 MB on a 400 MiB download) with no change to any `cliError` or install outcome. ([#238](https://github.com/schubydoo/claustrum/pull/238))

## 1.7.3 (2026-07-25)

[Compare with 1.7.2](https://github.com/schubydoo/claustrum/compare/v1.7.2...v1.7.3)

### Fixes

- `-version` now reports a build time on `go install pkg@version` builds — such builds embed no `vcs.time`, receive no `-ldflags`, and `debug.BuildInfo` has no timestamp field, so `-version` printed `built unknown`; the release pipeline now bakes the version and a UTC timestamp into the source it tags (`buildstamp.go`, written by `knope prepare-release`) and reports it when the resolved module version matches that release exactly, while a pseudo-version install (`@main`, `@<sha>`) still prints `unknown` and `-ldflags`/`vcs.time` continue to take precedence. ([#163](https://github.com/schubydoo/claustrum/pull/163))

## 1.7.2 (2026-07-25)

[Compare with 1.7.1](https://github.com/schubydoo/claustrum/compare/v1.7.1...v1.7.2)

### Fixes

- `-version` now reports the module version for `go install pkg@version` builds — such builds carry no `vcs.*` build settings (they compile from the module cache, which has no VCS context), so the fallback found nothing and reported the `claustrum-dev` sentinel even for a binary installed at a real tag; it now falls back to `debug.BuildInfo.Main.Version`, keeping the precedence `-ldflags` > `vcs.revision` > module version > sentinel, so downstream version checks that parse `-version` can confirm a `go install` build's version. ([#161](https://github.com/schubydoo/claustrum/pull/161))

## 1.7.1 (2026-07-12)

[Compare with 1.7.0](https://github.com/schubydoo/claustrum/compare/v1.7.0...v1.7.1)

### Bug Fixes

* **test:** give readTokenFD sole fd ownership to stop coverage flake ([#136](https://github.com/schubydoo/claustrum/issues/136)) ([c8e4775](https://github.com/schubydoo/claustrum/commit/c8e4775eb3af194126523b867dbc3896b1dfeff9))

## 1.7.0 (2026-07-11)

[Compare with 1.6.0](https://github.com/schubydoo/claustrum/compare/v1.6.0...v1.7.0)

### Features

* add opt-in Windows named-pipe transport (-listen-pipe) ([#134](https://github.com/schubydoo/claustrum/issues/134)) ([606fb2b](https://github.com/schubydoo/claustrum/commit/606fb2b5265ca85c345b3b2970430ce9201c6221))

## 1.6.0 (2026-07-11)

[Compare with 1.5.0](https://github.com/schubydoo/claustrum/compare/v1.5.0...v1.6.0)

### Features

* persist auth token to daemon.token for client reconnect (ref 5db5e4a) ([#131](https://github.com/schubydoo/claustrum/issues/131)) ([3007358](https://github.com/schubydoo/claustrum/commit/3007358308ec015c5eec2ce950ca75814993af9b))

## 1.5.0 (2026-07-03)

[Compare with 1.4.0](https://github.com/schubydoo/claustrum/compare/v1.4.0...v1.5.0)

### Features

* opt-in claustrum.conf config file for divergences (CT-3) ([#128](https://github.com/schubydoo/claustrum/issues/128)) ([f9b4743](https://github.com/schubydoo/claustrum/commit/f9b474327e5dfdcb89d80ed495e3e0c0a00c251b))

## 1.4.0 (2026-07-03)

[Compare with 1.3.1](https://github.com/schubydoo/claustrum/compare/v1.3.1...v1.4.0)

### Features

* track reference 7c2f88d — killAndWait, stdin offset, git.info repoSlug/defaultBranch ([#120](https://github.com/schubydoo/claustrum/issues/120)) ([3cd5521](https://github.com/schubydoo/claustrum/commit/3cd5521612b6fa94e50a1d85c06bd42a14c4b6f6))

## 1.3.1 (2026-06-12)

[Compare with 1.3.0](https://github.com/schubydoo/claustrum/compare/v1.3.0...v1.3.1)

### Bug Fixes

* namespace the daemonize re-exec sentinel to dodge CLAUDE_SSH_DAEMON_CHILD collision ([#111](https://github.com/schubydoo/claustrum/issues/111)) ([0e60c9c](https://github.com/schubydoo/claustrum/commit/0e60c9cd5856be1f44d4c38b86daf9526cfc651f))

## 1.3.0 (2026-06-12)

[Compare with 1.2.0](https://github.com/schubydoo/claustrum/compare/v1.2.0...v1.3.0)

### Features

* opt-in -keep-children serve flag to survive daemon restart (CT-2) ([#108](https://github.com/schubydoo/claustrum/issues/108)) ([5da6f3f](https://github.com/schubydoo/claustrum/commit/5da6f3faf8326fbec21634bd6085efbd613ad50b))
* opt-in wantPid (pid + startTime) on process.spawn/reattach (CT-1) ([#105](https://github.com/schubydoo/claustrum/issues/105)) ([ae8e0d6](https://github.com/schubydoo/claustrum/commit/ae8e0d680f11995bb331e122c4777fe6a68cd1ba))

## 1.2.0 (2026-06-12)

[Compare with 1.1.0](https://github.com/schubydoo/claustrum/compare/v1.1.0...v1.2.0)

### Features

* enforce "result types are ordered structs, not maps" via ast-grep ([#104](https://github.com/schubydoo/claustrum/issues/104)) ([f5512d7](https://github.com/schubydoo/claustrum/commit/f5512d7eda424c4d02a5ba345e6516418cbd1ba7))


### Bug Fixes

* filter hidden entries from files.list to match the reference ([#98](https://github.com/schubydoo/claustrum/issues/98)) ([23d2732](https://github.com/schubydoo/claustrum/commit/23d27325c413fbe5e3ae0437ad45af7cc42de250))

## 1.1.0 (2026-06-10)

[Compare with 1.0.1](https://github.com/schubydoo/claustrum/compare/v1.0.1...v1.1.0)

### Features

* -token-fd to pass the auth token via a descriptor (IMPROVEMENTS [#18](https://github.com/schubydoo/claustrum/issues/18)) ([#80](https://github.com/schubydoo/claustrum/issues/80)) ([37c81ce](https://github.com/schubydoo/claustrum/commit/37c81ce0ab2190ccfde41eb9ecdbf5420873c2b3))
* async process.stdin with bounded backpressure (IMPROVEMENTS [#9](https://github.com/schubydoo/claustrum/issues/9)) ([#69](https://github.com/schubydoo/claustrum/issues/69)) ([d5434d0](https://github.com/schubydoo/claustrum/commit/d5434d04313c4bb9fb140128ab5665d17ce8afc5))
* kill orphaned process on duplicate-id spawn (IMPROVEMENTS [#17](https://github.com/schubydoo/claustrum/issues/17)) ([#79](https://github.com/schubydoo/claustrum/issues/79)) ([b4493b0](https://github.com/schubydoo/claustrum/commit/b4493b06d96a35973bdcacc79972f07a8a5a8d6b))
* opt-in Prometheus metrics endpoint (IMPROVEMENTS [#16](https://github.com/schubydoo/claustrum/issues/16)) ([#78](https://github.com/schubydoo/claustrum/issues/78)) ([3101063](https://github.com/schubydoo/claustrum/commit/31010634e3f54b37ba5aa3cdbe03aac3bf53e37b))
* tiny leveled logger preserving log prefixes (IMPROVEMENTS [#13](https://github.com/schubydoo/claustrum/issues/13)) ([#72](https://github.com/schubydoo/claustrum/issues/72)) ([3955980](https://github.com/schubydoo/claustrum/commit/395598025efc253dc3c7ef5e30ea958392405f64))
* verify -cli-checksum on the -cli-zst path when supplied (D1) ([#65](https://github.com/schubydoo/claustrum/issues/65)) ([72de5a7](https://github.com/schubydoo/claustrum/commit/72de5a7fdbb67a3e18c9347320931f6571742e39))
* Windows process-tree kill via Job Objects (IMPROVEMENTS [#14](https://github.com/schubydoo/claustrum/issues/14)) ([#73](https://github.com/schubydoo/claustrum/issues/73)) ([7a2ef9b](https://github.com/schubydoo/claustrum/commit/7a2ef9bf3a0aa16c499fd289c77c2f1b56d0a5d2))


### Bug Fixes

* add git.info root field to match reference daemon 7cbfa471 ([#60](https://github.com/schubydoo/claustrum/issues/60)) ([dcca081](https://github.com/schubydoo/claustrum/commit/dcca081c688ffc8f20a706e9ec016513930a82ee))
* add HTTP client timeout and response size cap to httpGet ([#59](https://github.com/schubydoo/claustrum/issues/59)) ([e0af84e](https://github.com/schubydoo/claustrum/commit/e0af84edef5e03ca84ef4c48d99403ec03c592b8))
* bound -install ldd libc probe with a timeout ([#63](https://github.com/schubydoo/claustrum/issues/63)) ([8083a4a](https://github.com/schubydoo/claustrum/commit/8083a4a8d6fd69265bef462b7a29cd8625faa335))
* bound git invocations with a timeout (IMPROVEMENTS [#5](https://github.com/schubydoo/claustrum/issues/5)) ([#66](https://github.com/schubydoo/claustrum/issues/66)) ([e7a0746](https://github.com/schubydoo/claustrum/commit/e7a0746ca7911546be493e931193ebd517c18d98))
* bump x/sys to v0.44.0 + Go 1.25 to clear GO-2026-5024 ([#12](https://github.com/schubydoo/claustrum/issues/12)) ([#86](https://github.com/schubydoo/claustrum/issues/86)) ([4e44305](https://github.com/schubydoo/claustrum/commit/4e44305a52709fb6b0e50aa18979f70defca238a))
* cap per-process replay buffer to prevent unbounded memory growth ([#58](https://github.com/schubydoo/claustrum/issues/58)) ([3ce3035](https://github.com/schubydoo/claustrum/commit/3ce3035236714339555ef529a1296b3ef05f6c93))
* cap zstdDecompress output size to prevent disk exhaustion ([#57](https://github.com/schubydoo/claustrum/issues/57)) ([a22b609](https://github.com/schubydoo/claustrum/commit/a22b609c51a6f9a4c1fa5181f0002f85f96adc20))
* guard files.read against non-regular files; cap extractTarGz size ([#56](https://github.com/schubydoo/claustrum/issues/56)) ([1fcb4e8](https://github.com/schubydoo/claustrum/commit/1fcb4e89c65ba50c6dcda579791f921aa14263b3))
* make -install extract atomic via temp + rename (IMPROVEMENTS [#4](https://github.com/schubydoo/claustrum/issues/4)) ([#68](https://github.com/schubydoo/claustrum/issues/68)) ([6c51cde](https://github.com/schubydoo/claustrum/commit/6c51cde30b041d82fe53afba360ff7fb6e29c13c))
* render Material icon shortcodes on the docs site ([#75](https://github.com/schubydoo/claustrum/issues/75)) ([fd1685a](https://github.com/schubydoo/claustrum/commit/fd1685aa74676d8d3bdad35ec0a84ae78687032c))
* run extractLoginPATH in goroutine; add process-group kill on timeout ([#53](https://github.com/schubydoo/claustrum/issues/53)) ([899c1a6](https://github.com/schubydoo/claustrum/commit/899c1a6ceb39a0d1bb68c1cea696fdf663ca96f2))
* skip group-kill on exited children; kill 5 lived mutants; refresh docs ([#90](https://github.com/schubydoo/claustrum/issues/90)) ([f23de46](https://github.com/schubydoo/claustrum/commit/f23de462109b5a24ab549120d8c72b3df962f3d0))
* strip CLAUDE_RPC_TOKEN from spawned child env to match reference binary ([#55](https://github.com/schubydoo/claustrum/issues/55)) ([d5f0149](https://github.com/schubydoo/claustrum/commit/d5f0149a5403d06547f161ff33f680899b70f022))
* use constant-time comparison for the RPC auth token ([#64](https://github.com/schubydoo/claustrum/issues/64)) ([a7bce19](https://github.com/schubydoo/claustrum/commit/a7bce1923de1004aada53cab66507705b56596b5))


### Performance

* marshal stream frames once per emit, not once per subscriber ([#83](https://github.com/schubydoo/claustrum/issues/83)) ([de2ed22](https://github.com/schubydoo/claustrum/commit/de2ed2285b0c33f46aa09bf29bfbfd74e43059d1))


### Build System & Dependencies

* add pre-commit git hook mirroring CI lint (IMPROVEMENTS [#6](https://github.com/schubydoo/claustrum/issues/6)) ([#71](https://github.com/schubydoo/claustrum/issues/71)) ([f6223cc](https://github.com/schubydoo/claustrum/commit/f6223cc815cac3484a8a4f615a57d983059480e0))
* pin the Go toolchain (IMPROVEMENTS [#12](https://github.com/schubydoo/claustrum/issues/12)) ([#67](https://github.com/schubydoo/claustrum/issues/67)) ([fbf4d75](https://github.com/schubydoo/claustrum/commit/fbf4d75f028a122f720c06d6fa6f772af9e943e6))

## 1.0.1 (2026-06-08)

[Compare with 1.0.0](https://github.com/schubydoo/claustrum/compare/v1.0.0...v1.0.1)

### Bug Fixes

* **ci:** reference SLSA generator by semver tag, not SHA ([#44](https://github.com/schubydoo/claustrum/issues/44)) ([e31b52f](https://github.com/schubydoo/claustrum/commit/e31b52fc7ff75df76f1256090fd1056a911b344f))

## 1.0.0 (2026-06-08)


### Features

* clean-room claustrum daemon (18-method JSON-RPC over AF_UNIX) ([8cd3ceb](https://github.com/schubydoo/claustrum/commit/8cd3ceb8388846fe8f1cc3f30e1af834ce863b12))
* match the reference daemon's operational stderr logging ([#17](https://github.com/schubydoo/claustrum/issues/17)) ([693ca38](https://github.com/schubydoo/claustrum/commit/693ca385a6bbfafcba22de5f0ef91ea780db9c94))


### Bug Fixes

* -serve requires -token-file + defaults the socket ([#34](https://github.com/schubydoo/claustrum/issues/34)) ([a897794](https://github.com/schubydoo/claustrum/commit/a8977949c45f010c15b1423d90226e11ff4b70b9))
* default socket + best-effort -stop + -bridge dial framing ([#28](https://github.com/schubydoo/claustrum/issues/28)) ([0166c5e](https://github.com/schubydoo/claustrum/commit/0166c5e35d877f1ac652e2bd1eeffd5ea962dcd7))
* git.info branch resolution for unborn/detached HEAD ([#33](https://github.com/schubydoo/claustrum/issues/33)) ([2892446](https://github.com/schubydoo/claustrum/commit/28924466c7e4604da2c4ce50a9729b08ce837ad9))
* match reference -install checksum + error framing ([#29](https://github.com/schubydoo/claustrum/issues/29)) ([c3b9d17](https://github.com/schubydoo/claustrum/commit/c3b9d176db5a8c1ce74776c0eebaa52c8dbac118))
* match reference extract_tar side effects (destDir wipe, modes, .synced, archive consume) ([#15](https://github.com/schubydoo/claustrum/issues/15)) ([896fd5c](https://github.com/schubydoo/claustrum/commit/896fd5c93dc7cbf41113f44fd87a6083c29f39e8))
* match reference files.list symlink resolution and extract_tar entry-type rejection ([#16](https://github.com/schubydoo/claustrum/issues/16)) ([5a863d2](https://github.com/schubydoo/claustrum/commit/5a863d23c526fac196efa854d7b7a20aaff844e0))
* match reference process.stdin check order and exited-process error ([#36](https://github.com/schubydoo/claustrum/issues/36)) ([4357b34](https://github.com/schubydoo/claustrum/commit/4357b34c354684c7e052c84121cd4bbb34da088f))
* match reference request-size cap (1 MiB) and git non-repo result shapes ([#18](https://github.com/schubydoo/claustrum/issues/18)) ([ef3983f](https://github.com/schubydoo/claustrum/commit/ef3983fdbf08f9cd6340ada4f5020534d4815281))
* reject mistyped params and check auth before version ([#22](https://github.com/schubydoo/claustrum/issues/22)) ([f5dbd85](https://github.com/schubydoo/claustrum/commit/f5dbd85e30782d259ce7b2028e31a968a83abcf2))
* reject zip-slip paths in files.extract_tar (CodeQL go/zipslip) ([#14](https://github.com/schubydoo/claustrum/issues/14)) ([19adf3b](https://github.com/schubydoo/claustrum/commit/19adf3b0f3d5698eb8dc2ca015b872c5ab432ffb))
* strip trailing newline from -serve token file to match reference ([#37](https://github.com/schubydoo/claustrum/issues/37)) ([d906812](https://github.com/schubydoo/claustrum/commit/d906812f42859aaf51d0721722e948bbd6eabad6))
