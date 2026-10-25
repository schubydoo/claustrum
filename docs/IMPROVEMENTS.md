# claustrum — shipped ledger

This page is for maintainers who want the history of shipped hardening work. It
records the completed CI, test, release, and safety items that changed the
daemon from a bare reimplementation into a maintained drop-in. It is history,
not a backlog. An item that has a current rule links to the page that holds
that rule.

Most of this work is wire-neutral. It changes tooling, tests, or OS-level
behaviour, but it does not change a JSON-RPC frame. The deliberate divergences
(the D series) and the client-driven extensions (CT-1..CT-5) live in
[DIVERGENCES.md](DIVERGENCES.md), with the rules that judge them.

## The shipped items

The impact/cost ratings are H/M/L/S, as the original triage set them. The
"where" part of each row points at the artifact that carries the item.

| # | Item | I/C | What shipped · where |
|---|------|-----|----------------------|
| 1 | CI workflow | H/L | `go vet`, `gofmt -l`, `make all`, `go test` gate every PR (`.github/workflows/ci.yml`). |
| 2 | In-repo Go test suite | H/M | `harness_test.go` and the integration suites boot the daemon on a temp socket. They assert each method's frames over the real wire. Thus CI gates compatibility without the reference binary. |
| 3 | Golden-frame fixtures | H/M | The tests assert `testdata/socket_*.golden.json` byte-equal. `CONTRIBUTING.md` at the root of the repository tells how to regenerate them. |
| 4 | Atomic `-install` extract | M/L | `ensureCLI` stages the file at `.fetch-<random>` and renames it into place, so an interrupted install never leaves a half-written `cliPath`. See [INSTALL.md](INSTALL.md) → `-install`. |
| 5 | Timeouts on `git`/`exec` calls | M/L | Wrapped git and the linux-only `ldd` libc probe in `exec.CommandContext`. Both became divergences, D5 and D14, and both are retired now, because the reference bounds those calls itself. See [DIVERGENCES.md](DIVERGENCES.md). |
| 6 | pre-commit + `gofmt`/`vet` hooks | M/L | A zero-dependency hook lives in `.githooks/`. `make hooks` installs it. It uses the same lint order as CI. `--no-verify` bypasses it. |
| 7 | `go vet`-clean + `staticcheck` in CI | M/L | The CI `lint` job runs `golangci-lint` (`.golangci.yml`: staticcheck + govet + errcheck + ineffassign + unused + misspell/unconvert). |
| 8 | Bounded replay buffer (ring) | M-H/M | Each per-process replay buffer got the 16 MiB cap of the reference. The oldest frames drop, and `firstSeq` advances. See [Stream notifications](PROTOCOL.md#stream-notifications). |
| 9 | stdin backpressure | M/M | A per-process `stdinWriter` goroutine drains a bounded queue, so `process.stdin` returns at once and a full queue gets an error, not a block. See [process.stdin](protocol/process-stdin.md). |
| 10 | Fuzz the JSON-RPC parser | M/L-M | `fuzz_test.go` adds `FuzzDispatch` and `FuzzBindParams`. `FuzzDispatch` skips the side-effectful methods. CI runs the seeds. |
| 11 | Release automation | H/M | `.goreleaser.yaml` + `release.yml`: 6-target builds, checksums, syft SBOM, cosign signing, SLSA provenance. knope opens the version PRs (changesets-only). |
| 12 | Pin the Go toolchain | M/L | `go.mod` carries an explicit `toolchain` directive. CI and release use `go-version-file: go.mod`, so builds are reproducible against a known patch. |
| 13 | Structured/leveled logging | M/L-M | `logging.go` holds a small leveled stderr logger. The level tag goes before the `[Component]` prefixes, so existing greps keep matching. See [ARCHITECTURE.md](ARCHITECTURE.md#operational-logging). |
| 14 | Windows process-tree kill via Job Objects | M/M-H | A Job Object held the spawned children (`sysproc_windows.go`), and a kill ended the whole tree. A later reconciliation removed the job, so a kill ends the direct child only ([process.kill → Windows child trees](protocol/process-kill.md#windows-child-trees)). This item added `golang.org/x/sys` (Windows-only). |
| 15 | Docs site (mkdocs) | M/M | mkdocs-material publishes `docs/` to GitHub Pages. A `docs` workflow runs `mkdocs build --strict` on every PR and deploys on a push to `main`. |
| 16 | `/metrics` counters | L-M/M | `metrics.go` holds always-on atomic counters. The daemon serves `/metrics` only with `-metrics-addr`, which is off by default. No wire change. See [PROTOCOL.md](PROTOCOL.md#flags-of-claustrum-alone). |
| 17 | Duplicate-`id` spawn policy | L/L | A spawn that reuses a live `id` replaces the registry entry, and the previous process gets no signal. An earlier build ended the previous tree, and that kill is removed. See [process.spawn](protocol/process-spawn.md). |
| 18 | Token from fd/stdin | L/L | With `-token-fd <n>`, `-serve` reads the auth token from an open descriptor. It sends the token to the detached child over an inherited pipe (`CLAUSTRUM_TOKEN_PIPE`). Additive/off-wire. See [PROTOCOL.md](PROTOCOL.md#the-token-source). |
| 19 | Docs-site visibility/formatting pass | L/L-M | Changed the prose-heavy tables into per-item and per-method sections, so every method gets its own table-of-contents entry on the published site. Site-only. |
| 20 | Windows CI test runner | M/M | Added a `windows-latest` leg to the test matrix. The stdlib helper-process pattern (`helperproc_test.go`, `CLAUSTRUM_TEST_HELPER`) replaced the Unix fixtures, so the streamed bytes stay byte-identical on every operating system. `TestSocketFilesBattery` skips on Windows, because its golden pins a Unix mode string. |
| 21 | Exited-child group-kill guard + LIVED-mutant triage | S/S | `kill` and `killAll` skip the children that already exited, so the daemon cannot SIGKILL a recycled pgid (Unix). The duplicate-id replace did the same, until its kill was removed (item 17). [process.kill](protocol/process-kill.md#differences-from-the-reference) records this. A mutant triage after the run closed 5 real assertion gaps. |
| 22 | Spawn/exec syscall hardening | S/S | A `strace -f` differential found two syscall-level differences. First, the plain git calls (`gitContext`, `gitStdoutErr`) run `git -C <repo>`. The hardened calls now pass no `-C` ([Hardened git calls](PROTOCOL.md#hardened-git-calls)). Either way the daemon never calls `chdir`, so concurrent requests do not race. Second, `process.kill` signalled the whole process group, and now only `KILL` does ([process.kill](protocol/process-kill.md)). The frames are byte-identical. |
| 23 | Recover from handler panics | M/S | The per-request goroutine wraps dispatch in `defer recover()`, so a handler panic no longer stops the whole daemon. The reply is claustrum's own and not a parity claim. See [Handler panic recovery](PROTOCOL.md#handler-panic-recovery). |
| 24 | `-install` follows `89cb6289` | M/M | The `--version` bounds, the install order, the default CLI folder, the Windows `.exe` name, the progress lines and the download texts follow the reference. D11 is retired, and a home guard (D2) runs before the tree delete. See [INSTALL.md](INSTALL.md) → `-install`. |
| 25 | `peerCheck` in `server.capabilities` | M/S | The result gained the `peerCheck` member and the `server.peer_check` feature, and D25 is retired. See [The peer check](protocol/server.md#the-peer-check). |
| 26 | `files.read` follows `5fd08069` | M/S | `files.read` refuses a file that is not regular, as the reference does, D4 is retired, and `-files-read-regular-only` is a deprecated no-op. See [files.read](protocol/files-read.md). |
| 27 | `files.extract_tar` keeps the execute bit of the owner | M/S | The file modes, the entry `error` texts, the refusal of a name that leaves `destDir` and the `.synced` marker follow the reference. `server.capabilities` lists `files.extract_tar.execBit`. See [files.extract_tar](protocol/files-extract-tar.md). |
| 28 | `files.extract_tar` prepares `destDir` through a handle of its parent folder | M/S | The wipe of `destDir` and the new folder go through a handle of the parent folder, with the home guard by identity first. See [files.extract_tar](protocol/files-extract-tar.md). |
| 29 | `git.worktree_create` refuses a filter that only the repository defines | M/S | In the state `shadow`, a checkout that needs such a filter answers `repository_only_filter` and rolls back. See [git.worktree_create](protocol/git-worktree-create.md#git-filters). |
| 30 | The git methods refuse a repository config that the daemon does not accept | M/M | The daemon reads the repository config itself, follows `include.path`, and refuses a config that it does not accept. See [PROTOCOL.md](PROTOCOL.md#repository-config-refusals). |
| 31 | The git calls carry the option lists of `5fd08069` | M/S | The git calls of the git methods got the option lists and variables of the reference. See [Hardened git calls](PROTOCOL.md#hardened-git-calls). |
| 32 | The git methods run git on the daemon's own copy of the settings | H/L | The daemon keeps a copy of the git settings of each repository in a folder `git-shadow` beside its socket. `git.info` reports the state of that copy in `settingsControl`. See [The settings copy](PROTOCOL.md#the-settings-copy). |
| 33 | The measurement records leave the docs, and each doc file has a word limit | M/S | The folder `docs/record/` is removed, with each link to it. `TestDocsWordLimits` (`docs_consistency_test.go`) fails for a file of `docs/` or for `AGENTS.md` that passes its limit. |
| 34 | On Linux and macOS a stopping daemon keeps a new daemon's socket when it has another inode | H/S | `keepSocketFileAtClose` (`livepred_unix.go`), `TestCloseAllKeepsSuccessorSocket`. |
| 35 | Four places where claustrum differed from `5fd08069` in a signal, a remove or a write | H/M | The four are the worktree file copy, the session key of a spawn, the host cleaner on Linux and the start reap. Each now follows `5fd08069` (measured on Linux). The tests are `worktreecopy_test.go`, `sessionsupersede_test.go`, `hostclean_unexamined_linux_test.go` and `childreap_inspect_linux_test.go`. |
| 36 | `git.worktree_create` drops the `--show-scope` listing and adds the top folder pair of `5fd08069` | M/S | Both hold with a settings copy, and the pair also with `shadowDirOff`. `fallback-git` has no filter rule. |
| 37 | `server.shutdown` needs no `auth` | M/S | `server.shutdown` answers without `auth`, as the reference does, so `-stop` sends no token. A daemon of a build before that change still asks for `auth` there. It answers `-32001` and keeps running, while `-stop` prints `stopped` and exits `0`. Stop such a daemon by its PID before an upgrade. See [Authentication](PROTOCOL.md#authentication). |
