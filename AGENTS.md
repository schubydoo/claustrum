# Claustrum

Claustrum is a tiny Go daemon with few dependencies. It is a clean-room
reimplementation of the small daemon that hosts a remote Claude Code session
over SSH. It combines three parts: a local CLI-version manager, a process
supervisor, and a JSON-RPC multiplexer over an `AF_UNIX` socket. The
multiplexer holds a replay buffer. Black-box probes of the reference binary
captured a behavioral contract, and claustrum is built to that contract. No
code was copied and no code was decompiled (see [`NOTICE`](NOTICE)).

The one hard rule: stay byte-identical to the JSON-RPC frames of the reference
daemon. The wire surface is the product.

## Build · run · test

| Task | Command |
|------|---------|
| Build the binary | `make build`. CGO is off, the build uses `-trimpath`, and the binary is stripped. Output: `./claustrum` |
| Cross-build all 6 targets | `make all`. Targets: linux/darwin/windows × amd64/arm64 → `dist/` |
| Format | `make fmt` runs `gofmt -w`. `gofmt -l .` must be empty |
| Vet | `make vet` (`go vet ./...`) |
| Lint | `golangci-lint run ./...`. The configuration is `.golangci.yml` (v2) |
| Tests | `go test -race ./...`. This runs the unit suites and the socket-integration suites |
| Validation battery | `scratch/probe/validate.sh`. It diffs frames against the reference. The script is gitignored |

- Use Go 1.25+ and `CGO_ENABLED=0`. The build toolchain is deliberately held
  below 1.27. The default `jsonv2` in 1.27 moves inherited wire bytes, so build
  and CI resolve Go from `go.mod`, never from `go-version: stable`. See
  [`docs/UPSTREAM-TRACKING.md`](docs/UPSTREAM-TRACKING.md) → toolchain-induced
  drift. One dependency is cross-platform:
  `github.com/klauspost/compress` (zstd). Two more go into Windows builds only:
  `golang.org/x/sys` and `github.com/Microsoft/go-winio`.
- In-repo tests cover the wire surface two ways: fast unit tests, and a
  socket-integration suite (`harness_test.go` + `integration*_test.go`). The
  suite boots the daemon on a temp `AF_UNIX` socket. It asserts the frames of
  every method against golden fixtures (`testdata/socket_*.golden.json`). CI
  therefore gates compatibility without the reference binary, on linux, macOS
  and Windows. Process fixtures come from the test binary itself
  (`helperproc_test.go`), never from `/bin/*`, so stream bytes match the
  goldens on every OS. Statement coverage is approximately 99%. The
  cross-binary battery that diffs against the reference daemon lives in
  `scratch/`.

## Architecture

There is one binary. A flag selects the mode (`main.go`): `-serve`, `-bridge`,
`-stop`, `-version`, `-install`, `-probe-cli`. `19f30c46` added the one-shot
`-probe-cli` CLI-runnability probe.

| File | Role |
|------|------|
| `rpc.go` | request/response types, error codes, and `dispatch`. The dispatch order is parse → auth → version → route. Probes showed that dispatch tests auth before the jsonrpc version, except on the unauthenticated `server.shutdown` |
| `server.go` | the `-serve` daemon: `AF_UNIX` listener (mode `0600`), per-conn read loop, concurrent dispatch, self-daemonize, graceful shutdown |
| `methods_*.go` | the 20 methods across `server.*` / `files.*` / `git.*` / `launcher.*` / `process.*` / `plugins.*`. `7d193f89` removed `server.version`. `19f30c46` added `plugins.prune`. `89cb6289` added `launcher.resolve` (`methods_launcher.go`, which also holds the `process.spawn` launcher checks and the child env strip) |
| `managedlauncher*.go` | the managed launcher's OS split (the settings folder, the Windows refusal) and the launcher runs of `-install` and `-probe-cli` behind `CLAUDE_SSH_MANAGED_LAUNCHER=1` (`89cb6289`) |
| `results.go` | result structs, with fields declared in the exact order the reference emits. Never use a map. A map sorts its keys and diverges from the wire contract |
| `process.go` | `procManager` / `managedProc`: spawn in own process group, base64 stream frames, async stdin writer (bounded queue + backpressure), per-process replay buffer, `reattach` |
| `bridge.go` | `-bridge`: a stdio↔socket relay. SSH attaches to it. It injects no auth |
| `logging.go` | leveled stderr logger (`CLAUSTRUM_LOG_LEVEL`, default emit-everything). The level tag goes before the `[Component]` prefixes, so existing greps keep matching |
| `metrics.go` | opt-in Prometheus counters at `/metrics`. The `-metrics-addr` flag creates the listener. Without that flag there is no listener. Counting is always-on atomics |
| `wirelog.go` | opt-in `-wire-log` JSON-RPC frame capture. It is a pure side channel over already-marshaled bytes, and it is off by default. It redacts credentials by key only, not by payload contents. It forces `0600` on every open |
| `install.go` | `-install`: CLI download / verify (SHA-256) / extract (zstd) / prune. A `-cli-url` download is verified unconditionally. If a `-cli-checksum` is supplied, claustrum also verifies the local `-cli-zst` blob, as the reference does. The compare is case-sensitive |
| `*_unix.go` / `*_windows.go` · `pipetransport*.go` | OS specifics (daemonize, process groups on Unix, login-shell PATH) and the opt-in, default-off, Windows-only `-listen-pipe` named-pipe transport (CT-5). On Windows a kill ends the direct child only, and no child is in a job of the daemon. The daemon starts outside the job of its launcher. If that job refuses, the daemon starts inside it and the launcher logs one line. `-keep-children` works on Windows as claustrum's own flag: `89cb6289` exits 2 on it |

The JSON-RPC surface is identical on every OS. Full internals →
[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md).

## Conventions

- Byte-identical wire frames are the contract. Results are ordered structs,
  not maps. Any change to `rpc.go` / `methods_*.go` / `process.go` /
  `results.go` must keep the validation battery green. An intentional
  divergence owes three things. Give it an entry with its decision rules in
  [`docs/DIVERGENCES.md`](docs/DIVERGENCES.md). Give it its wire frames in
  [`docs/PROTOCOL.md`](docs/PROTOCOL.md), or in the page of its method under
  `docs/protocol/`. The measured cells go into the record of that method under
  `docs/record/`. Call it out in the PR.
- Do not add a new dependency without discussion. The permitted set is
  stdlib + zstd (`klauspost/compress`) + `golang.org/x/sys` and
  `github.com/Microsoft/go-winio` (both Windows-only), with `CGO_ENABLED=0`.
- Never add telemetry.
- Cross-platform parity is a rule. Keep OS specifics in `*_unix.go` /
  `*_windows.go`. `make all` must cross-compile cleanly for all six targets.
- Keep host-specific / reverse-engineering working notes out of the repo.
  `scratch/` is gitignored on purpose.
- Each fact of the documentation has one home. A rule that a reader acts on
  gets one or two sentences on the page of its method under `docs/protocol/`.
  A measurement goes into the record of that method under `docs/record/`, or
  into a test. The history of completed work goes into
  [`docs/IMPROVEMENTS.md`](docs/IMPROVEMENTS.md). The history of a reference
  build goes into [`docs/REFERENCE-BUILDS.md`](docs/REFERENCE-BUILDS.md).
  Another document links to
  that home and does not copy the text. A method that has no page yet keeps
  its text in [`docs/PROTOCOL.md`](docs/PROTOCOL.md). That document also
  keeps the sections that more than one method uses, for example the error
  texts and the branch step. The rule is for the documents under `docs/`.
  `AGENTS.md` keeps the safety rules of Part A, because an agent must hold
  them before it touches code.

## Always do

- Every change to `main` goes through a branch and a PR. Never commit or push
  to `main` directly.
- Use Conventional Commits for PR titles (`feat:` / `fix:` / `docs:` /
  `chore:` / `ci:` …). PRs squash-merge, so the title becomes the commit
  subject. Titles are hygiene only. They do not drive releases.
- Releases are changesets-only (knope, not release-please). For a
  user-facing change, add a `.changeset/*.md` fragment with
  `knope document-change`. The fragment drives the version bump and the
  changelog. No fragment ⇒ no release. An internal PR needs no fragment.
  Apply the `no-changelog` label instead. See [`.changeset/`](.changeset/) and
  CONTRIBUTING.md → Changesets.
- A changeset body is ONE line. knope renders any multi-line body as a
  `####` heading block instead of a bullet. That breaks the changelog, and it
  already did so in 1.7.2 and 1.7.3. Fold every detail into that one
  line. Several sentences on it are fine. `scripts/lint_changesets.py` gates this in CI and pre-commit.
- Before a PR, make all four of these true: `gofmt -l .` prints nothing,
  `go vet ./...` is clean, `golangci-lint run` is clean, and
  `go test -race ./...` is green. For a wire-surface change, also re-run the
  `scratch/` validation battery to make sure that the frames stay
  byte-identical.
- CI gates every PR on the branch ruleset's required checks: `ci required checks
  passed` + `security required checks passed` + `conventional PR title`.

## Gotchas — Part A: always-on safety (hold these before touching code)

- `process.spawn` runs arbitrary commands as the user of the daemon. This is by
  design. Treat socket + token as equivalent to shell access. The threat model
  is in [`SECURITY.md`](SECURITY.md).
- Four code paths give a caller-supplied or operator-supplied path to a
  recursive delete (`os.RemoveAll` or `os.Root.RemoveAll`).
  `git.worktree_remove` uses `os.Root.RemoveAll`. The rollback of
  `git.worktree_create` uses it too, and both on Windows. Three of the four paths are RPC paths. The daemon
  `~`-expands those RPC
  paths first, so `"~"` once meant `os.RemoveAll($HOME)`. That destroyed
  the maintainer's home directory on 2026-08-02:
    - `files.extract_tar` wipes `destDir`. `wipesHomeDir` (`homeguard.go`) guards it.
    - `git.worktree_remove` deletes `worktreePath` itself. It runs no `git
      worktree remove`. It deletes the entries of the leaf except `.git` in the
      order that the directory read returns them, and it stops at the first
      failure. Then it deletes the rest and the leaf, then the entry under
      `<git dir>/worktrees`. Without `worktreeRoot`, the entry stays for a
      `baseRepo` that does not exist.
      Without `worktreeRoot` and with a daemon `GIT_DIR` and `GIT_COMMON_DIR`,
      `<git dir>` is the repository that `GIT_DIR` names. Each delete goes through an `os.Root`, so no delete
      follows a symlink out of the leaf or its parent. A leaf that is a symlink
      or not a directory is refused. A worktree LOCKED in the `.git` folder of
      `baseRepo` is refused, not deleted, also where `89cb6289` answers success (D22).
      On Windows a junction at any directory between `baseRepo` and the leaf is
      refused too. At `.claude` or `.claude\worktrees`, `f6010b97` and `89cb6289`
      answer success and delete the branch. For `89cb6289` that is measured for a
      branch that another ref reaches. At a junction above `.claude`, `89cb6289`
      answers success with no leaf and no branch there (Windows VM, cells J-b-wt-pj
      and J-b-wt-pJ). That is D19, widened by the maintainer's decision of
      2026-10-07.
      Since `7d193f89`, the containment of the reference refuses a
      home path first: `worktreePath` must be strictly inside `baseRepo`. On the
      default branch `wipesHomeDir` is therefore defense-in-depth. It fires in
      one case only: a repo is an ancestor of home. On the `worktreeRoot` /
      `external_root` branch that in-repo containment does not apply, so there
      `wipesHomeDir` is the active home guard. Both branches run it before the
      delete.
    - When `git.worktree_create` rolls back a worktree, it deletes
      `worktreePath`. There are two rollbacks. After a failed `git worktree add`,
      it runs no git call and removes the leaf only if the leaf is an empty
      directory. Before the add, on Linux and macOS, create removes one stale
      entry of `<baseRepo>/.git/worktrees`: an entry whose `gitdir` record
      names the `.git` of the new leaf. It removes that entry only if it is the
      only stale entry of the folder (`89cb6289`, cells A13, A13b and S1 to S7c,
      Linux and macOS VMs). The delete is one `os.Root.RemoveAll`
      of the entry name through a root at that folder, after `wipesHomeDir` on
      the entry path. An entry with a `locked` file stays (`89cb6289`, cells A9
      and A9b, Linux and macOS VMs). On Windows that step is one `os.RemoveAll` of the
      entry path, with no `locked` test (not measured).
      The second rollback follows a successful add. On Linux and
      macOS four refusals come before it, and they delete nothing. Two answer
      a `.git` file of the new worktree that names no registration of the
      repository. A registration whose `commondir` file or `gitdir` record
      cannot be read as a file gets the first of them (`89cb6289`, cells P-g to
      P-j, Linux and macOS VMs). The third answers a registration whose `gitdir`
      record was read and names another worktree. The fourth answers a
      registration that is a stale entry which the step before the add left in
      place (`89cb6289`, cells A9 g, A9b g and A10 g, Linux and macOS VMs). The first
      three come before the
      `timeoutMs` test that follows the add (`89cb6289`, row D-9 on a macOS VM
      and cell P-f on Linux and macOS VMs). From the code, the fourth does too
      (not measured).
      The second rollback runs when the
      `timeoutMs` of the caller expired during the add, the checkout or the copy
      step. It also runs after a post-checkout drain that exceeded that
      `timeoutMs`, and after a failed read-tree checkout. On Linux and macOS it
      also runs after a failed placement of the index of the new worktree. It
      deletes the entries
      of the leaf in the order that the directory read returns them, and it
      stops at the first failure. Then it deletes the registration, runs the
      branch step on the created branch, and then removes the empty leaf. On
      Linux and macOS a registration that cannot be deleted skips the branch
      step. On Linux and macOS, if git answered `rev-parse
      --absolute-git-dir`, the registration of the rollback is the entry that
      the tests after the add accepted. That is the entry of the registrations
      directory of `baseRepo` with the last name of the `gitdir:` path, read
      once after the add. The rollback reads neither the `.git` file of the leaf
      nor the `gitdir` record again. `89cb6289` removes that entry when either
      one is changed (cells Z16 and Z18 on Linux and
      macOS VMs, cells B1 to B3 and B5 on the same VMs). The delete is one
      `os.Root.RemoveAll` of that name through a root at the registrations
      directory, after `wipesHomeDir` on the entry path. After the open of the
      root, and through it, the entry must still have the identity that the
      tests saw. Create holds the accepted folder open until it answers.
      While that handle is open, a new folder does not get that identity. If
      the open fails, the identity comes from a stat of the path (not
      measured). The rollback deletes nothing if the folder at the path is not
      the accepted one at that test (D24). With no answer of git, and on
      Windows, the registration of the rollback is the folder that the `.git`
      file of the leaf names. If its `gitdir` record does not name the leaf, the
      rollback does not delete it. If it does not resolve strictly inside the
      registrations directory of `baseRepo`, the same holds. That delete acts on the resolved path that its check
      verified, and on Linux and macOS through a root at the registrations
      directory, which must still have the identity that the check saw. On Linux and macOS the placement of the index removes a
      file, a link or an empty folder at `<registration>/index`, after a create
      there answered "file exists". That is one
      `os.Root.Remove` of the fixed name `index`, never a tree. The
      registration of the placement is that same entry. With no answer of git
      it is the folder that the `gitdir:` path names. If git answered, the
      create refuses before the checkout an entry whose `gitdir` record cannot
      be read or names another path (`89cb6289`, cells P-c and P-g to P-m,
      Linux and macOS VMs). The placement then reads no record again: a record
      that goes during the checkout does not stop it. With the record gone,
      `89cb6289` answers success in cell B4 (Linux and macOS VMs) and the `openat` text of
      a 0500 folder in cell Z15 (Linux and macOS VMs). The placement has the
      identity test of the rollback, on the root that it opened. It places
      nothing if the folder at the path is not the accepted one at that test
      (D24, not measured). With no answer the
      registration tests do not run. An entry then gets the index only if its
      `gitdir` record can be read and names the new worktree. An entry folder
      whose stat fails takes the placement itself, which fails there. With no
      answer of git, a registration whose back-pointer cannot be read is never
      deleted.
      `wipesHomeDir` guards every delete of the leaf as defense-in-depth behind the
      containment that create applies itself. Create also tests the checkpoint
      identity of the leaf again, so a swap while create runs cannot redirect
      a delete. Create holds the leaf and its parent open until it answers,
      so a replacement cannot reuse the identity of the leaf. On Windows both
      handles share delete access, so they do not block these deletes.
      The empty-leaf rmdir tests only two things: the path still resolves to
      the leaf, and the parent is still the held parent. An rmdir cannot
      delete content.
    - `-install` deletes the CLI path, which is operator input:
      `<cli-dir>/<cli-version>`, and `<cli-version>.exe` on Windows. The cli-dir
      is `-cli-dir`, or `<home>/.claude/remote/ccd-cli` without it. A folder at
      that path goes as a tree BEFORE the new CLI runs, as on the reference. A
      file there is replaced by the new CLI. A new CLI that does not run goes
      with a plain remove, so nothing is left at that path.
      Two guards run before that tree delete. The single-path-component rule
      of D6 runs first: `ensureCLI` runs it before it writes or removes
      anything at a path built from the version. D6
      makes the version one path component, so the CLI path is a direct child
      of the cli-dir. D6 does not test which folder that child is. With a
      cli-dir that is the parent of the home folder and a version that is its
      leaf name, the CLI path is the home folder. So `cliFolderHoldsHome`
      runs right before the tree delete and refuses a CLI path that is home or
      contains it. It runs `wipesHomeDir` first. Then it compares the folder at
      the CLI path with home and with each parent folder of home by identity
      (`os.SameFile`). It does that for the home path as given and for its
      resolved path. The reference deletes home there (measured on Linux, macOS
      and Windows).
      `-install` also removes two operator-named paths with a plain
      `os.Remove`, never as a tree (`clearNonFolderCLIDir`). The first is the
      cli-dir entry itself, only when it is a regular file, a symlink to a
      regular file or a FIFO. A symlink goes as a link, and its target stays.
      The second is the regular file named exactly `ccd-cli-version` in the
      parent folder of the cli-dir. No other name and no other file kind is
      removed. The reference does both (Linux, macOS and Windows VMs). Do not
      widen either delete, and do not build either path from anything but the
      cli-dir.
      `-install` also sweeps the cli-dir. The sweep is one plain `os.Remove`
      for each entry named `.fetch-*` or `*.zst` that is more than 10 minutes
      old, never as a tree. It runs before the run of a new CLI and after a
      failed attempt. On a cache hit it runs only after a `--version` run that
      its bound stopped. A cache hit with a good run does not sweep (cells Z8
      of `89cb6289`, Linux, macOS and Windows VMs). The sweep after a stopped
      run is outside `ensureCLI`, so no D6 test comes before it. Cell P6
      (Linux VM) shows that sweep. The sweep also runs after a D6 refusal. In cell P5 (Linux VM,
      `-cli-version ../x`) a `.fetch-d` file that is 20 minutes old goes on
      claustrum and on `89cb6289`. The sweep names only
      the cli-dir, never the version. A second sweep removes an entry named
      `*.zst.part` that is more than 7 days old, with the same plain
      `os.Remove`. It runs once, before any CLI runs, also on a cache hit
      (cells Z1 and Z4b on Linux, macOS and Windows VMs). So
      no D6 test comes before it either, and it names only the cli-dir. In
      cell P5 an 8 day old `p.zst.part` file goes too. A CLI file whose name
      ends in `.zst.part` goes in that sweep at more than 7 days, and the run
      installs the version again (cells P4 and P4c, Linux VM). A
      name that ends in `.zst.part` follows this rule alone, also with the
      `.fetch-` prefix (cells Z2 and Z5, the same three systems).
      The `-cli-keep` prune runs after a good install only, so after D6. It
      is one plain `os.Remove` for each entry of the cli-dir past the keep
      count, in the order of the mtime, never as a tree. A file, a link and
      an empty folder go. A folder with content stays and takes its place in
      the order, and a link goes as a
      link. The new CLI has no protection in the prune. It goes when enough
      entries are newer, and with `-cli-keep 0` every counted entry goes.
      `89cb6289` does the same (rows C-1, C-2, C-7 and C-11 on Linux and
      macOS VMs, cell C-11dir on a Windows VM, cells K0a, K0b and F2 on all
      three). With a negative `-cli-keep` no prune runs. A good install then
      prints no result line and exits 2, as on `89cb6289` (cells Kneg and
      Kn-2, all three systems). The stderr text there is claustrum's own
      line, by the maintainer's decision of 2026-10-07. The prune does not
      count the sweep's names, a `*.zst.part` name or a `.blob-` name, and it
      removes none of them. `89cb6289` counts a planted `.blob-` file (cell
      C-12, Windows VM) and removes it (cells B1, all three systems). That
      difference is D18. Do not turn a remove of the
      prune into a tree delete.
      The prune and the sweeps never remove the home folder. Each skips an
      entry of the cli-dir that is the home folder or holds it
      (`cliEntryHoldsHome`). A folder gets the tests of `cliFolderHoldsHome`,
      and every other kind gets `wipesHomeDir`. In the prune the skipped entry
      still takes its place in the order. That guard is claustrum's own (D2).
      `89cb6289` removes an empty home folder there (cells H1 and H3 on Linux,
      macOS and Windows VMs).
      `-install` removes the `-cli-zst` blob, an operator-named path, with one
      plain `os.Remove` once decompression succeeded. The home guard refusal
      is the one exception: it keeps the blob. No guard runs before the remove
      beyond that: the path was read as a zstd archive first.

  `wipesHomeDir` refuses a target that is home or contains home. It resolves a
  relative path with `filepath.Abs` first. It still permits a path under home,
  because `~/.claude/…` is the install path of the daemon itself. The guard is
  always-on, not opt-in. Any RPC path param that reaches a recursive delete
  owes this guard. `IsAbs && !isFilesystemRoot` is not a substitute: home
  passes both tests, and neither test resolves a relative path. A daemon in home
  that receives `worktreePath:".."` therefore still destroys home through the
  parent directory (measured). See
  [`docs/DIVERGENCES.md`](docs/DIVERGENCES.md) D2.
- `plugins.prune` is a further `os.RemoveAll` site, and it is safe by
  construction. It deletes `<socket-derived-root>/<hash>`. The root comes from
  the socket layout of the daemon itself, not from an RPC or operator path. The
  leaf must match `^[0-9a-f]{16}$` (`pluginHashRE`). The path is never
  caller-supplied and never `~`-expanded, so it needs no `wipesHomeDir` guard.
  The four paths above remain the only caller-supplied or operator-supplied
  recursive deletes.
- Auth is in-band per request (`"auth":"<token>"`). The token of the daemon
  comes from `-token-file` or from `-token-fd`. With `-token-file` the daemon
  reads the file once and then unlinks it, so the token never lands in
  `/proc/<pid>/environ`. With `-token-fd` the daemon reads the token from an
  open descriptor. It forwards the token to the daemonized child over a pipe,
  so this handoff never touches disk. No mode reads `CLAUDE_RPC_TOKEN`:
  `-bridge` relays a client that supplies its own `auth`, and the daemon strips
  the variable from spawned children. `server.shutdown` is the one method that
  is not authenticated. That is parity with the reference. Desktop stops the
  daemon with no token in its environment, so `-stop` sends no
  `auth` member. Every other method rejects an unauthenticated request `-32001`.
- The daemon persists its token to `daemon.token` (mode `0600`) beside the
  socket. It writes the file atomically at startup and unlinks it on graceful
  shutdown (`tokenpersist.go`). A client can thus reconnect after the daemon
  unlinked the `-token-file`, or after the `-token-fd` pipe closed. The fixed
  name and the socket-dir location are the reconnect contract, so they are
  deliberately not configurable. One parity caveat remains: on Windows `0600`
  is not an owner-only DACL. Do not "fix" that caveat without making the change
  an opt-in divergence. The old caveat was that two daemons in one dir collide
  on the file. The run-dir lock now bounds it (`daemon.lock`, parity with
  `4534d86`). On Linux and macOS a new daemon evicts a prior live same-socket
  daemon before it binds. The two daemons therefore no longer coexist to
  collide. Two cases remain: eviction is refused, or the host is Windows, which
  ships no run-dir lock. That lock is parity, not a claustrum "fix". The macOS
  holder-verification that claustrum applies to the lock is a hardening
  divergence (D15). See [`docs/PROTOCOL.md`](docs/PROTOCOL.md) → Token
  persistence / Run-dir lock.
- The requests of one connection dispatch concurrently. Parse and auth errors
  are the exception: the read loop writes them
  ([`docs/PROTOCOL.md`](docs/PROTOCOL.md) → Transport). Replies can return out
  of order, which matches the reference. Do not serialize them. The per-request
  goroutine recovers from panics. It replies with
  `-32603 "recovered panic: <v>"`. For `server.shutdown` it writes no frame at
  all. That frame is claustrum's own and is NOT a parity claim. The path
  is unreachable, so no client can observe it and it cannot diverge from
  anything. Do not add a golden for that frame. The battery never exercises it.
  Do not treat it as a wire contract. The tests provoke it through the
  `dispatchRequest` seam.
- `-serve` makes no outbound network connections. `-install` reaches the network
  only with `-cli-url`. That download path verifies its SHA-256 before it
  extracts, unconditionally. Every dial `-serve` makes is to a local `AF_UNIX`
  socket, never network egress.
- The `ldd` libc probe bounds ldd itself at 5 s (`lddProbeTimeout`), as on the
  reference since `19f30c46`. The direct `<cli> --version` run of `-install` is
  stopped at 30 s on a cache hit (`cliRunBound`), as on the reference. After an
  install in the same run it is stopped at 120 s (`cliFirstRunBound`). The old flags
  `-libc-probe-timeout` and `-cli-probe-timeout` are deprecated no-ops, and each
  logs one warning. `-cli-download-timeout` sits beside them in the `-install`
  arm of `main`. A line that sets one of these bounds from a flag compiles and
  passes every isolated test. `TestInstallArmWiresEachFlagToItsOwnGlobal` is the
  guard against it.
- A disabled limiter bypasses its `io.LimitReader` / `context.WithTimeout`
  entirely. Never "simplify" it into a huge value. For the caps, the `cap+1`
  (or `max-total+1`) arithmetic defines the boundary. For the deadlines, an
  unarmed cancel path makes the timeout `false` by construction. A huge constant
  is a different, observable behavior. This rule applies to every flag in
  Part B.

## Gotchas — Part B: the opt-in wire divergences

Four divergences are opt-in flags:

- D3 (`max-extract-bytes`)
- D5 (`git-timeout`)
- D10 (`max-cli-bytes`)
- D12 (`cli-download-timeout`)

All four default OFF. That is the parity position.
The reference applies no such cap, deadline, or refusal at any input that the
probe can reach. A non-off default therefore fails an operation that the
reference completes. Claude Desktop owns the `-serve` / `-install` argv, so the
`claustrum.conf` key is the reachable knob, not the flag. Each disabled state
bypasses its limiter entirely. That is the "never simplify" rule of Part A.

On `git.worktree_remove` the deadline of D5 gates no delete. That method runs no
`git worktree remove`, and no D5 kill leads to a delete there. A kill only refuses
or skips a step. `git.worktree_create` is the exception. Its
rollback deletes after a failed or killed read-tree checkout, and it removes an
empty leaf after a failed add. Never read a fired `git-timeout` as "git refused".
Opting D5 in is wire-visible.

D13 is a non-flag divergence: verify-before-decompress ordering, on `-cli-url` and on a `-cli-zst` blob with a checksum. D13 is
always-on, but it is unresolved, not justified. D1, D4, D7, D11 and D14 are retired,
because claustrum now matches the reference on those paths.

D17 is off-wire and macOS-only. The host cleaner reads an `lsof` run it gave
up on as busy. A completed run that found nothing reads as not busy. The
reference side is not probe-measured. The
harm it refuses is the cleaner SIGTERMing a daemon that is serving a client on a
host where `lsof` never returns. The sibling lock read is deliberately NOT
covered: it reads a run it gave up on as not held. An `lsof` command that does
not start is parity, not D17. The daemon then gets no SIGTERM, and a run folder
with a lock file stays, as on `89cb6289` (macOS VM, 3 of 3 runs). So is a run
that writes to stderr and exits 1. See the entry.

D20 is off-wire, on Linux and macOS. Before the group `SIGKILL` of a child-group
leader that reads as gone, the reap of a `-serve` start waits 50 ms and reads the
leader again. See the entry.

D21 is off-wire and Windows-only. A second daemon that starts on a live socket
writes to the same `remote-server.log`. claustrum appends. If both daemons are
this build, no line is lost. `89cb6289` truncates the file at that start, so the
earlier lines of the first daemon are lost. Keeping the lines is the maintainer's
decision of 2026-10-02, not a rule 3 clause. See the entry.

D22 is on-wire and always-on. `git.worktree_remove` refuses a worktree that is
locked in the `.git` folder of `baseRepo`. `89cb6289` answers success in
four states. Rows p6 and p6f: a daemon `GIT_DIR` and `GIT_COMMON_DIR` of another
repository, with the folder present or gone. Row p6e: a daemon `GIT_DIR` alone.
Those three ran on Linux, macOS and Windows VMs. Row p6d: a `baseRepo` that does
not exist as sent (Linux and macOS VMs). With `worktreeRoot`, rows q2 to q4
(Linux VM) differ in the frame too. That is the maintainer's decision of
2026-10-03. See the entry.

D23 is Windows-only. The environment block of a child is in name order, because
Go 1.26 sorts it at the process start. `89cb6289` keeps the order of its
launching block and adds its entries after it. Name order stays by the
maintainer's decision of 2026-10-06, not by a rule 3 clause. See the entry.

D24 is on Linux and macOS, and the frames are equal in its two cells (Linux and macOS VMs).
If git answered `rev-parse --absolute-git-dir`, a rollback of
`git.worktree_create` deletes the registration only if the
folder at its path is still the folder that the tests after the add accepted.
`89cb6289` deletes the folder at that path in cells B6 and B6b (Linux and macOS VMs, 2 runs
each). In cell B6b that folder holds the registration of a live sibling worktree.
The placement of the index has the same test (not measured). The guard stays by
the maintainer's decision of 2026-10-07, not by a rule 3 clause. See the entry.

D25 is on-wire, Linux only, and only with `CLAUDE_SSH_PEER_CHECK=1` in the daemon
environment. claustrum has no peer check. It answers `"peerCheck":"unavailable"`
in `server.capabilities` and serves every caller. `5fd08069` answers
`"peerCheck":"on"` with a `peerCheckBy` member there (Linux VM). That is the
maintainer's decision of 2026-10-10, not a rule 3 clause. See the entry.

The flag/key table, the governing rules (rule 1–4 + clauses (a)/(b)/(c)), each
divergence's default / activation / cost / reopen trigger →
[`docs/DIVERGENCES.md`](docs/DIVERGENCES.md). Per-method wire frames →
[`docs/PROTOCOL.md`](docs/PROTOCOL.md). Driver-claim provenance ("Desktop owns
the argv", `cliError` classification, libc selection) →
[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md).

## Where detail lives

- Protocol / frames → [`docs/PROTOCOL.md`](docs/PROTOCOL.md)
- One short page for each method → `docs/protocol/`
- The measurements behind a method page → `docs/record/`
- Divergence catalog + rules → [`docs/DIVERGENCES.md`](docs/DIVERGENCES.md)
- Internals + driver-claim provenance → [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)
- Worked client examples → [`docs/EXAMPLES.md`](docs/EXAMPLES.md)
- Keeping compatibility in sync → [`docs/UPSTREAM-TRACKING.md`](docs/UPSTREAM-TRACKING.md)
- History of the reference builds → [`docs/REFERENCE-BUILDS.md`](docs/REFERENCE-BUILDS.md)
- Terms → [`docs/GLOSSARY.md`](docs/GLOSSARY.md)
- Shipped ledger (completed work) → [`docs/IMPROVEMENTS.md`](docs/IMPROVEMENTS.md)
- Host-local agent guardrails + agent-tool routing → `CLAUDE.local.md` (gitignored)
- CI · security · releases → [`.github/workflows/`](.github/workflows/) (the
  `ci` / `security` aggregators are the required checks). Releases: pending
  `.changeset/` fragments → knope opens a version PR (`knope-prepare.yml`).
  Merging it tags `v*` (`knope-release.yml`), and the tag fires `release.yml`.
  That release is signed and carries SBOM and SLSA, via
  [`.goreleaser.yaml`](.goreleaser.yaml). Gated on the `KNOPE_ENABLED` repo var.
- Claude Code / Anthropic API specifics → prefer current docs (context7 /
  find-docs) over memory:
  <https://docs.anthropic.com/en/docs/claude-code/overview>.
