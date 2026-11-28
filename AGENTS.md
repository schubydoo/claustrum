# Claustrum

Claustrum is a tiny Go daemon with few dependencies. It is a clean-room
reimplementation of the small daemon that hosts a remote Claude Code session
over SSH. It combines three parts: a local CLI-version manager, a process
supervisor, and a JSON-RPC multiplexer over an `AF_UNIX` socket. The
multiplexer holds a replay buffer. Black-box probes of the reference binary
captured a contract, and claustrum is built to that contract. No
code was copied, and no decompiler or disassembler output was transcribed into
the code (see [`NOTICE`](NOTICE)).

The one hard rule: stay byte-identical to the JSON-RPC frames of the reference
daemon. The wire surface is the product.

## Build · run · test

| Task | Command |
|------|---------|
| Build the binary | `make build`. The build uses `-trimpath`, and the binary is stripped. Output: `./claustrum` |
| Cross-build all 6 targets | `make all`. Targets: linux/darwin/windows × amd64/arm64 → `dist/` |
| Format | `make fmt` runs `gofmt -l -w *.go handlers internal`. `gofmt -l .` must be empty |
| Vet | `make vet` (`go vet ./...`) |
| Lint | `golangci-lint run ./...`. Config: `.golangci.yml` (v2) |
| Tests | `make test` (`go test -race -timeout 20m`, `internal/git` in four shards). It runs the unit and socket-integration suites. The time limit is explicit, because a race run took over 9 minutes, near the 10 minute default of `go test` |

- Use Go 1.26 and `CGO_ENABLED=0`. The build toolchain is held
  below 1.27. The default `jsonv2` in 1.27 moves inherited wire bytes, so build
  and CI resolve Go from `go.mod`, never from `go-version: stable`. See
  [`docs/UPSTREAM-TRACKING.md`](docs/UPSTREAM-TRACKING.md) → toolchain-induced
  drift.
- Tests cover the wire surface with fast unit tests and a socket-integration suite
  (`harness_test.go` + `integration*_test.go`). The suite boots the daemon on a temp
  `AF_UNIX` socket and asserts the frames of every method against golden fixtures
  (`testdata/socket_*.golden.json`). Process fixtures come from the test binary
  (`helperproc_test.go`), never from `/bin/*`, so stream bytes match the goldens on every OS.

## Architecture

There is one binary. A flag selects the mode (`main.go`): `-serve`, `-bridge`,
`-stop`, `-version`, `-install`, `-probe-cli`.

The table names the files that an agent must not break. The file map of the
modes and the OS splits is in
[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md#source-layout).

| File | Role |
|------|------|
| `rpc.go` | `request`, `gate` (read loop), `route` (`dispatchRequest`) |
| `internal/rpc/` | error codes, reply types, result structs with fields in the order of the reference. Never a map |
| `methods_*.go`, `internal/{files,git}/` | the 24 methods across `server.*` / `files.*` / `git.*` / `launcher.*` / `process.*` / `plugins.*` |
| `internal/supervisor/process.go` | spawn, stream frames, stdin queue, replay buffer, `reattach` |
| `handlers/prune.go` | the params type of `plugins.prune`, whose name is in its `-32602` text |
| `internal/homeguard/homeguard.go` | `WipesHomeDir`, the home guard each RPC path to a recursive delete owes |
| `internal/files/methods_files.go` | the wipe of `destDir` and the `.synced` marker of `files.extract_tar` |
| `methods_plugins.go` | the delete of `plugins.prune` |
| `internal/git/worktreeremove.go` | the deletes of `git.worktree_remove` |
| `internal/git/worktreeverify.go` | the tests after the `worktree add` and both rollbacks of `git.worktree_create` |
| `internal/git/worktreestale*.go` | the stale registration step before the add |
| `internal/git/worktreestaging.go` | the staged add: its folder `.s<n>`, the staging folder and its move, and the cleanup of old staging entries |
| `internal/git/gitdirheld.go` | the held git directory and the list of the steps that act through it |
| `internal/git/githarden_index_*.go` | the placement of the index of a new worktree, and the registration delete of the second rollback (`removeCreatedRegistration`) |
| `internal/install/install.go` | the CLI path delete, the sweeps, the prune and the blob remove of `-install` |
| `internal/git/gitshadow.go` (`removeEntry` and `removeLink`) · `internal/git/gitshadowprobe.go` (`removeProbeEntry`) · `internal/git/gitshadowsweep.go` · `internal/git/gitshadowrepo.go` · `internal/git/gitshadowuse.go` · `internal/git/headcopyrmdir_unix.go` · `internal/git/requestdirrmdir_*.go` · `internal/git/worktreecontentbatch.go` (`removeObjectBody`) | the deletes in the folder `git-shadow` |
| `internal/hostsweep/hostclean*.go` | the run-dir delete (`removeRunDir`) and the signals of the host cleaner |
| `clitemproot_unix.go` | the two rmdirs of the new private temp folder (`removeMadeTempRoot`) and the registry remove |
| `internal/sessiondirs/sessiondirs.go` | the record of session folders, its temp-file remove and its prune |
| `internal/childrec/childreap.go` | the signals and record removes of the startup reap |
| `internal/git/gitcommand.go` | the start of every git call, which passes over a git in a session folder |
| `internal/git/gitshellpath.go` | the git of the login shell's PATH and the stop of its `--exec-path` call |

Windows lacks two methods: `files.find` and `files.mkdir`.

## Conventions

- Byte-identical wire frames are the contract. Results are ordered structs,
  not maps. Any change to `rpc.go`, `methods_*.go`, `internal/supervisor/process.go`,
  `internal/rpc/`, `internal/files/`, `internal/git/` must keep the socket-integration suite and its golden
  fixtures green (`make test`). An intentional
  divergence owes two things. Give it an entry with its decision rules in
  [`docs/DIVERGENCES.md`](docs/DIVERGENCES.md). Give it its wire frames in
  [`docs/PROTOCOL.md`](docs/PROTOCOL.md), or in the page of its method under
  `docs/protocol/`. Call it out in the PR.
- Do not add a new dependency without discussion. The permitted set is
  stdlib + zstd (`klauspost/compress`) + `golang.org/x/sys` (all OSes) and
  `github.com/Microsoft/go-winio` (Windows-only), with `CGO_ENABLED=0`.
- Never add telemetry.
- Cross-platform parity is a rule. Keep OS specifics in `*_unix.go` /
  `*_windows.go`. `make all` must cross-compile cleanly for all six targets.
- Keep host-specific / reverse-engineering working notes out of the repo.
  `scratch/` is gitignored on purpose.
- Each fact of the documentation has one home. Link to it and do not copy the text
  ([`docs/STYLE.md`](docs/STYLE.md)). The rule is for the documents under `docs/`. `AGENTS.md`
  keeps the safety rules of Part A, because an agent must hold them before it touches code.

## Always do

- Every change to `main` goes through a branch and a PR. Never commit or push
  to `main` directly.
- Use Conventional Commits for PR titles (`feat:` / `fix:` / `docs:` /
  `chore:` / `ci:` …). PRs squash-merge, so the title becomes the commit
  subject. Titles are hygiene only. They do not drive releases.
- Releases are changesets-only (knope, not release-please). A user-facing change adds a
  `.changeset/*.md` fragment with `knope document-change`. No fragment ⇒ no release. An
  internal PR takes the `no-changelog` label. A changeset body is ONE line, because knope
  renders a multi-line body as a `####` heading block. `scripts/lint_changesets.py` gates
  it. See CONTRIBUTING.md → Changesets.
- Before a PR, make all four of these true: `gofmt -l .` prints nothing,
  `go vet ./...` is clean, `golangci-lint run` is clean, and
  `make test` is green. For a wire-surface change, a golden fixture
  (`testdata/socket_*.golden.json`) changes only for a new reference frame or
  a catalogued divergence. Name the reason in the PR.
- CI gates every PR on the branch ruleset's required checks: `ci required checks
  passed` + `security required checks passed` + `conventional PR title`.

## Gotchas — Part A: always-on safety (hold these before touching code)

Unmarked behavior follows the reference, pinned at `87375cc7`, except its
terminal. It was measured there, on `4c111e86`, `c9125eb8`, `b3081b51`, `be1c1ca5` or an earlier build. A D-number,
"claustrum's own", "from the code" and "not measured" mark the exceptions. A
rule that names another build follows that build. A file, function or Go API
name describes claustrum's code, not a measurement.

Part A is a pointer index. A new reference rule for a delete site goes on its method
page. AGENTS.md gets a new line only for a new delete site or a new Never widen rule.

`process.spawn` runs arbitrary commands as the user of the daemon. This is by
design. Treat socket + token as equivalent to shell access. The threat model is
in [`SECURITY.md`](SECURITY.md).

### The delete paths

Five code paths give a caller-supplied or operator-supplied path to a recursive
delete. The daemon `~`-expands the RPC paths first, so `"~"` once meant
`os.RemoveAll($HOME)`. That destroyed a home directory. The
deletes inside the folder `git-shadow` are not among the five: their root comes
from the socket, as in `plugins.prune`. `removeall_allowlist_test.go` lists each
tree delete. It fails on an unlisted site, or on a listed guard function that
calls no homeguard check.

| Path | What is deleted | Who names it | Guard | Method page |
|------|-----------------|--------------|-------|-------------|
| `files.extract_tar` | the last name of `destDir` | the caller (RPC) | `homeguard.WipesHomeDir`, `destEntryHoldsHome`, `checkDestViews` | [files.extract_tar](docs/protocol/files-extract-tar.md) |
| `git.worktree_remove` | `worktreePath`, its registration | the caller (RPC) | `homeguard.WipesHomeDir` on `worktreePath`, `worktreeLeafHomeRefusal`, containment | [git.worktree_remove](docs/protocol/git-worktree-remove.md) |
| `git.worktree_create` rollback | `worktreePath`, its registration, one stale entry, two staging removes | the caller (RPC) | `homeguard.WipesHomeDir` (D2), `leafHoldsHome`, identity tests, one exception (see Path 3) | [git.worktree_create](docs/protocol/git-worktree-create.md#after-a-failure) |
| `git.worktree_create` staging sweep | old staging entries of the `baseRepo` git directory | the caller (RPC): a good add in `shadow` | prefix, age, `homeguard.WipesHomeDir` | [git.worktree_create](docs/protocol/git-worktree-create.md#after-a-failure) |
| `-install` | the CLI path | the operator | D6, `homeguard.FolderHoldsHome` | [-install](docs/INSTALL.md#staging-and-cleanup) |

#### The home guard

`homeguard.WipesHomeDir` refuses a target that is home or contains home. It resolves a
relative path with `filepath.Abs` first. It still permits a path under home,
because `~/.claude/…` is the install path of the daemon. The guard is
always-on, not opt-in. Any RPC path param that reaches a recursive delete owes
this guard. `IsAbs && !homeguard.IsFilesystemRoot` is not a substitute: home passes both
tests, and neither test resolves a relative path. A running daemon dials only with
`sockroute.DialSocketInDaemon`, which never changes the working folder that
`filepath.Abs` reads.

#### Path 1: `files.extract_tar`

The wipe removes the last name of `destDir` as a tree, through a parent handle that
follows no link (`wipeDestDir`). Before it, an old `destDir` loses `.synced`
(`prepareOldDestDir`). Never widen: This delete path is approved by the maintainer. Do
not widen it.

#### Path 2: `git.worktree_remove`

It deletes `worktreePath` and its registration through an `os.Root`, not
`git worktree remove`. Before any delete it refuses a symlink or non-directory leaf
and a lock that counts (D22). A failed repository check and `homeguard.WipesHomeDir` also refuse. Markers beside dangling `.git` links: see
[their rule](docs/protocol/git-list-branches.md#response). Never widen: On Linux and macOS a gone worktree whose locked entry sits
behind a linked `<git dir>/worktrees` succeeds, and its branch goes. That exception
follows the reference. Do not widen it. Never widen (maintainer approved, Windows): with a junction between `baseRepo`
and the leaf, the branch ref goes, and so does the one registration whose record names the leaf as the caller spelled it.
Keep the leaf, all behind the junction, and a registration whose record names the real path (`hasJunctionBelow`).

#### Path 3: the `git.worktree_create` rollback

After a failed add, only an empty leaf goes, or on Linux and macOS a file or link in its place. After a good add, the rollback
deletes the leaf and the registration and runs the branch step. Some states keep the
registration or the branch (method page). Before the add, the stale step removes at most
one stale `worktrees` entry, and only one with no `locked` file. The one tree delete with no home guard is the
registration of a create that holds no git directory and accepted no registration.
Never widen (maintainer approved, Windows only): a read-tree call can fail by itself after a junction in `baseRepo`
moved to a second repository. The leaf and the registration of the first repository then go. In the second repository only
the ref of the same name goes, also for a live or locked worktree (`87375cc7`, measured on Windows). Its entry and leaf stay.
Never widen: Never widen a delete of the [steps that move
the hold](#steps-that-move-the-hold). Never widen: on Linux and macOS the branch step runs behind a link at `.git`
only when the link names the held git directory. Only an empty folder that took the leaf right after a staged add goes,
by one rmdir (`undoMovedAwayLeaf`).

#### Path 4: the `git.worktree_create` staging sweep

After a good add in `shadow`, old `.claude-new-worktree-` entries of the held git
directory go (`sweepOldStaging`). A real folder goes as a tree, any other kind by one
plain `os.Root.Remove`. Never widen: This step is approved by the maintainer. Do not widen
the prefix, the kinds or the folder of this delete.

#### Path 5: `-install`

A folder at `<cli-dir>/<cli-version>` goes as a tree after D6 and
`homeguard.FolderHoldsHome`. Every other remove is one plain `os.Remove`. Never widen: Do
not widen either delete of `clearNonFolderCLIDir` or build either path from anything but
the cli-dir. Do not turn a remove of the prune or the sweeps into a tree delete.

### Other delete and signal sites

- **`plugins.prune`** deletes `<socket-derived-root>/<hash>`, where the leaf matches
  `files.PluginHashRE`. The path is never caller-supplied and never `~`-expanded, so it
  needs no home guard ([plugins.prune](docs/protocol/plugins-prune.md)).
- **The host cleaner** (Linux and macOS) removes a run dir by one `os.Root.RemoveAll` at `<root>/run`, with
  a name from a directory read and no shape test (`removeRunDir`). An identity test precedes each signal
  ([guards](docs/DAEMON.md#host-cleaner-off-wire-linux-and-macos)). Never widen: Build no
  run-dir name from a request value or an operator path.
- **The log entry** (Linux and macOS, 13047a51). At start each try is one plain `os.Remove` of the entry at `remote-server.log` beside the socket
  (`openDaemonLogAt`, [the log](docs/DAEMON.md#daemon-log-remote-serverlog)). Never widen: remove only that one name, never a tree.
- **`git-shadow`.** Each delete is `homeguard.WipesHomeDir`, then one call through a root
  at `<socket folder>/git-shadow` (`removeEntry`, `removeLink`) or at the daemon's `.probe` (`removeProbeEntry`), except the HEAD copy rmdir, the request folder rmdir, the scratch file remove, the seed copy remove and the keep swap
  (below). Do not build a name below it from a request value ([The
  folders](docs/protocol/git-rules-settings-copy.md#the-folders)). A link goes as a link
  ([D8](docs/DIVERGENCES.md#d8)). Never widen: `removeOwnFolderFile` removes one entry at the daemon's folder path, never a tree, link target or (Linux, macOS) folder ([rule](docs/protocol/git-rules-settings-copy.md#the-folders)). Outside `git-shadow`,
  `removeTempRequestDir` removes a request's temp folder after `homeguard.WipesHomeDir`.

#### The exceptions in `git-shadow` and the LFS store

- **The HEAD copy folder.** On Linux and macOS one rmdir after `homeguard.WipesHomeDir` removes an empty `HEAD` folder of a `git-shadow` copy (`removeEmptyHeadFolder`). Never widen: no tree.
- **A request folder in a `0300` folder.** On Linux and macOS, `removeEntry` fails with a permission error in a daemon folder of mode `0300`. One rmdir after `homeguard.WipesHomeDir` then removes an empty request folder (`removeEmptyRequestDir`), while the daemon folder keeps its mkdir identity. Linux uses an `O_PATH` handle, and macOS tests the identity and then runs it by path, with a short window between the two. Never widen: in a daemon folder of mode 0300, remove only an empty request folder that the request made, by one rmdir, never a tree, and only while the daemon folder has its mkdir identity.
- **The scratch files of the content check.** One plain remove by name deletes each object file after its `hash-object` answer (`removeObjectBody`, [rule](docs/protocol/git-worktree-create.md#the-content-check)). It goes through the one root of `.readback-scratch-<n>`, after `homeguard.WipesHomeDir`. At a `timeoutMs` deadline the folder goes when the check ends (claustrum's own, not measured). An entry put in its place after the kind test goes by that same remove. Never widen: remove only the regular file, named by an object id, that the check just wrote there, never a tree or a link target.
- **The LFS store.** On Linux and macOS a create in `shadow` removes `.claude-<8 hex>-<oid>` temp files from one shard
  of `<git dir>/lfs/objects`, each by one file `unlinkat` after `homeguard.WipesHomeDir` (`removeLFSStoreEntry`,
  [the private LFS store](docs/protocol/git-worktree-create.md#the-private-lfs-store)). Never widen: Remove no other
  name and never a tree. The sweep removes only a regular file named by the directory read. A failed or stopped
  copy of the seed removes its own object file (`copySeed`). Never widen: remove only the object file that the
  seed just made, by one plain unlinkat, never a tree. The keep (macOS: claustrum's own) renames its `.kept-<oid>` link over the downloaded copy
  in the private store, after `homeguard.WipesHomeDir` (`swapInKept`). Never widen: rename only the `.kept-<oid>` link of the file that the keep just wrote, and only onto that copy.

#### More delete and signal sites

- **The LFS space stop.** On Linux and macOS a Git LFS download can leave under a tenth of the free space at the start. Then the
  group of the read-tree call gets one `SIGKILL` (`lfsRoomWatch`). It goes through `reapCheckoutGroup` after `gitStillRuns`, and the private folder then goes
  by its usual remove before the reply ([the private LFS store](docs/protocol/git-worktree-create.md#the-private-lfs-store)).
  Never widen: send it only to the group of the checkout git that this create started, and none after its reap.
- **`session-folders`.** One plain `os.Root.Remove` by name, of the temp file of a failed write and in the prune ([session
  folders](docs/protocol/git-rules-session-folders.md)). Never widen: Never make either
  remove a tree delete. Never build the name of a remove there from a request value, a
  path or a link target.
- **The shutdown sweep.** On Linux and macOS, a child whose args ask for `stream-json` gets `SIGTERM` first, then its group gets `SIGKILL` (`argsAskEnd`,
  `endAsked`, [D28](docs/DIVERGENCES.md#d28)). Never widen: In the shutdown sweep, send a group
  `SIGKILL` after the exit of a child only while its exit drain is open. On Linux and macOS a plain spawn whose reaped leader stayed in its group gets a group `SIGKILL` while any process holds its pipes. That process can be outside the group. With `-keep-children` it gets none
  (`killGroupInOpenDrain`, [the rule](docs/protocol/server.md#a-child-in-its-exit-drain)). A reused group number takes that signal, as on `87375cc7` (measured on Linux), a risk the maintainer accepted. A client `KILL` can come before it, so the group can get two. A pty spawn gets none from this path. Never widen: send it from this path at most once for a child, only to a plain spawn, before its exit frame.
- **A give-up during a start.** A spawn of an `id` given up after its start test ends its new child (on Linux and macOS a group `SIGKILL`)
  (`endUnstarted`, [process.spawn](docs/protocol/process-spawn.md#a-give-up-during-the-start)). Never widen: send it only to
  the group that this spawn made, before the one `cmd.Wait` of that child, and to no other child.
- **The group of an exited launcher** (87375cc7, [rules](docs/protocol/process-spawn.md#the-group-of-an-exited-launcher-build-87375cc7)). Linux: group `SIGTERM` and `SIGKILL` before the reap (`settleLeftGroupHeld`). macOS: a probe and a group `SIGKILL` right after the reap (`settleLeftGroupReaped`). A reused number can then hit an unrelated group, a risk the maintainer accepted. The refusal below does not prevent it. Never widen: on macOS send it only to the group of a launcher spawn that this daemon started, once, right after its reap. A client `KILL` that passed its test before the reap mark can still send one more. Refuse a number of 1 or less, the own pid and the own group (`groupNumberRefused`, claustrum's own, not measured). Never widen: on Linux send the shutdown `SIGKILL` (`killHeldGroupAtStop`) only while `leaderHeld` holds the zombie, never after `cmd.Wait` and never with `-keep-children`.
- **The unattended end of a spawn** (87375cc7). `SIGTERM` to the child, then the group `SIGKILL` after at most 7 s (Linux,
  macOS). Windows ends the job (`endUnheld`, `killGroupOnUnheldExit`,
  [process.spawn](docs/protocol/process-spawn.md#the-unattended-end-build-87375cc7)). Never widen: arm it only for an
  accepted top-level unattendedSeconds above 0. Send no `SIGKILL` for a refused `SIGTERM`. Signal only that child, its group or its job, and none to a group that
  the spawn did not make. Send each signal through `unheldGate` before the reap (Linux, macOS). Start no end after the
  shutdown began.
- **The extract marker.** `.synced` goes as a tree through the handle of `destDir` that
  `extractTarGz` tested (`writeSyncedMarker`, [the
  steps](docs/protocol/files-extract-tar.md#what-the-request-does)). Never widen: This step is approved by the maintainer. Do not
  widen it. Do not build that name from a parameter or from the archive.
- **The temp-folder test** (Linux and macOS) removes its own `.claude-ssh-<n>` file by one plain `os.Remove`
  ([process.spawn](docs/protocol/process-spawn.md)). Never widen: Do not make or remove
  the file in any folder but the inherited folder or its nearest existing parent.
- **The startup reap** (Linux and macOS) sends `SIGTERM`, then `SIGKILL`, to the group of a recorded pid after
  the identity test (`reapTargets`, [the reap](docs/DAEMON.md#the-reap)). Each remove of `forget` and
  `forgetLate` takes one bare name by one `os.Root.Remove`. The gone-leader group `SIGKILL` follows the
  wait of [D20](docs/DIVERGENCES.md#d20). Never widen: signal only the group of `rec.Pid` after its identity test.
  Remove one bare name, never a tree.

#### The private temp folder of a spawn

- **`cliTempRoot`.** On Linux and macOS it removes the `ccd-<10 hex>` folder that it just made and found unfit, by one rmdir
  after `homeguard.WipesHomeDir` (`removeMadeTempRoot`, [the
  rules](docs/protocol/process-spawn.md#the-private-temp-folder-build-b3081b51)). Before it, one rmdir removes the `t`
  that it just made, through the handle of the new folder. Never widen: Remove no other name and never a tree.
- **A lost temp folder** (Linux and macOS). A child that asked with `cliTempRootEnd` gets SIGTERM through `signalIfLive`, then,
  if it still runs 3 s later, a group SIGKILL (`endForLostTempRoot`). A child that ended on the SIGTERM gets a group
  SIGKILL before its reap (`killGroupOnTempRootExit`). Never widen: Signal no other child, send no group SIGKILL
  after the reap, and none to a group that the spawn did not make.
- **The loss kinds** (`checkTempRoot`, `checkChildDir`). The folder, its `t` and the temp dir count as lost in the
  [measured kinds](docs/protocol/process-spawn.md#a-lost-folder-build-4c111e86). Never widen: Add no kind without a
  measured row, and end no child over a failed check, a gone temp dir or a folder of mode 0.

### The held git directory

In `shadow` a create holds the git directory of `baseRepo` from its gate to its reply.
Each write and delete inside it goes through the held folder. The steps and exceptions
are at the top of `internal/git/gitdirheld.go` ([The held git
directory](docs/protocol/git-worktree-create.md#the-held-git-directory)).

#### Steps that move the hold

On Linux and macOS, `acceptedGitDir.movedTo`, `askedAgain` and `replacedAtPath` take a new folder from the path. Add no other
step that resolves the path of the git directory again, and never widen a delete of these
steps. The maintainer approved two exceptions: the Windows stale step, and the rollback
of the registration after the staging step took the hold ([Rare
states](docs/protocol/git-worktree-create.md#rare-states)). Never widen (maintainer approved, Windows only): a junction in `baseRepo`
that moves to a second repository right after `rev-parse --absolute-git-dir` (`noteJunctionMove`) puts that repository in the
`GIT_DIR` of the one `worktree add`, so its branch lands there. The registration stays in the held folder, the leaf stays, and no stale step and no rollback of the good add run. Its rollback after a failed add is on [the create page](docs/protocol/git-worktree-create.md#differences-by-system).
During a remove `pinBeforeLocate` pins the `baseRepo` folder and one handle of its real git directory. With that hold, one move after the pin reaches only the ref of the same name in the second repository.
The states with no hold and the open race windows are on [the remove page](docs/protocol/git-worktree-remove.md#platform-notes).
A move inside the branch step still reaches that ref by path with no gate. Add no other delete there.

### Other rules

- **Writes of `git.worktree_create`** (`writeLeafGitFile`, `writeHeldLeafGitFile`, `writeWorktreeConfig`,
  `addStage.move`). Each goes through an `os.Root` or the held leaf. Only the staging
  sweep and the two staging removes are tree deletes ([The settings
  copy](docs/protocol/git-worktree-create.md#the-settings-copy)).
- **Auth** is in-band per request. No mode reads `CLAUDE_RPC_TOKEN`, and children do not
  get it. Only `server.shutdown` skips auth ([Authentication](docs/PROTOCOL.md#authentication)).
- **Token.** Keep the name and place of `daemon.token` fixed. Do not "fix" its Windows
  mode without an opt-in divergence ([DAEMON.md](docs/DAEMON.md#token-persistence-daemontoken)).
- **Concurrency.** Do not serialize the requests of a connection. Add no golden for the
  panic frame ([Handler panic recovery](docs/PROTOCOL.md#handler-panic-recovery)).
- **Network.** `-serve` dials no network. `-install` does with `-cli-url` only, and
  verifies the SHA-256 ([SECURITY.md](SECURITY.md#network)).
- **Time bounds.** `TestInstallArmWiresEachFlagToItsOwnGlobal` guards the flags of the
  `-install` bounds ([INSTALL.md](docs/INSTALL.md#the-direct---version-run)).
- **Limiters.** A disabled cap or bound bypasses its limiter. Never "simplify" it into a
  huge value, which is a different, observable behavior.
- **Git time limits** (`internal/git/gitstop.go`) are always on and are no divergence
  ([Time limits](docs/protocol/git-rules-calls.md#time-limits-of-git-calls)).
- **The deadline kill of the content check** (87375cc7, `internal/git/worktreecontentkill.go`, `helperKills`). At the `timeoutMs` deadline the running `cat-file --batch` and the commit, tree and file `hash-object` each get `Process.Kill` ([rule](docs/protocol/git-worktree-create.md#the-content-check)). Linux and macOS repeat it once for a helper that runs at the kill (claustrum's own, not measured), except for the file `hash-object`. Never widen: signal only these four helpers of this check, before their reap (`sysproc.LeaderGate`), and never a group.
- **The `timeoutMs` stop of the read-tree call** (`internal/git/githarden.go`, `cmd.Cancel`). Never widen: it sends its
  group `SIGKILL` only before the reap of git (`sysproc.LeaderGate`). The kill after a drain overrun comes before the reap too (`runCheckoutPipes`). A wait with no exit status sends no overrun kill (claustrum's own).
- **A failed read-tree call** (Linux, `runCheckoutPipes`). After the checkout git of a create fails by itself, its group gets one
  `SIGKILL`. 4c111e86 sends it after a Git LFS download error or a failed smudge filter, also with a `timeoutMs` (measured on Linux). Never widen: send it once, only to the group of that git, after its exit and before its reap (claustrum's
  own). Send none after the `timeoutMs` stop or the LFS space stop.
- **The bound stops of `-install` and `-probe-cli`** (`internal/install/groupstop.go`, `runCLIVersion`). Never widen: each
  sends its group `SIGKILL` only before the reap of its run (`sysproc.LeaderGate`).
- **The `--exec-path` stop of the login git** (Linux and macOS, `internal/git/gitshellpath.go`, `runExecPath`). At its 10 s bound
  (claustrum's own) the call gets a `SIGKILL` through its handle. Never widen: send it only to that one git, before its
  reap (`sysproc.LeaderGate`), and never to a group.
- **The job start test** (Windows, `4c111e86`, `awaitJobTest`, `beginJobTest`, [rules](docs/protocol/process-kill.md#the-start-test-of-the-jobs)).
  Never widen: end this test's job by `TerminateJobObject` once, at 1 minute or an asleep failure, before the
  child's reap (`sysproc.LeaderGate`), or close it at that reap or the daemon exit. End nothing else.

## Gotchas — Part B: the live divergences

A divergence is a deliberate difference from the reference. It is not a bug. Do not
"fix" one toward the reference without the maintainer. Each entry is in
[`docs/DIVERGENCES.md`](docs/DIVERGENCES.md).

| ID | Summary | Default | System |
|----|---------|---------|--------|
| D2 | Refuse a destructive path that is or holds home | always-on | all |
| D6 | `-cli-version` must be one path component | always-on | all |
| D8 | Refuse a file link at `remote-server.log` after a failed rename (Windows). The `git-shadow` sweep writes nothing through a link | always-on | all |
| D15 | Verify a lock holder before a signal | always-on | macOS |
| D20 | Wait 50 ms before the `SIGKILL` of a gone leader | always-on | Linux, macOS |
| D22 | Remove refuses a lock in the `.git` folder of `baseRepo` | always-on | Linux, macOS |
| D23 | The environment of a child is in name order | always-on | Windows |
| D24 | Create rollback keeps a folder that replaced the registration | always-on | all |
| D27 | The stale step keeps an entry with a live record | always-on | Windows |
| D28 | No group `SIGKILL` after the exit frame at shutdown | always-on | Linux, macOS |
| CT-1 | `wantPid` adds `pid` and `startTime` | the caller asks | all |
| CT-2 | `-keep-children` keeps the children at shutdown | off | all |
| CT-3 | The `claustrum.conf` config file | no file | all |
| CT-4 | Hardened token persistence, a deferred idea | not built | none |
| CT-5 | The `-listen-pipe` named-pipe transport | off | Windows |
| CT-6 | Cap the output size of `files.extract_tar` | off | all |
| CT-7 | Cap the CLI size of `-install` | off | all |
| CT-8 | Bound the download of `-install` | off | all |

CT-6, CT-7 and CT-8 default off, the parity position. The `claustrum.conf` key is the
reachable knob, because Claude Desktop owns the argv.

## Where detail lives

- Protocol / frames → [`docs/PROTOCOL.md`](docs/PROTOCOL.md)
- Running the daemon (token, lock, cleaner, start, log) → [`docs/DAEMON.md`](docs/DAEMON.md)
- `-install` and `-probe-cli` (`cliError` texts, facts line) → [`docs/INSTALL.md`](docs/INSTALL.md)
- Docs style guide → [`docs/STYLE.md`](docs/STYLE.md)
- One short page for each method → `docs/protocol/`
- Divergence catalog + rules → [`docs/DIVERGENCES.md`](docs/DIVERGENCES.md)
- Internals + driver-claim provenance → [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)
- Worked client examples → [`docs/EXAMPLES.md`](docs/EXAMPLES.md)
- Keeping compatibility in sync → [`docs/UPSTREAM-TRACKING.md`](docs/UPSTREAM-TRACKING.md)
- History of the reference builds → [`docs/REFERENCE-BUILDS.md`](docs/REFERENCE-BUILDS.md)
- Terms → [`docs/GLOSSARY.md`](docs/GLOSSARY.md)
- Shipped ledger → [`docs/IMPROVEMENTS.md`](docs/IMPROVEMENTS.md)
- Host-local guardrails + tool routing → `CLAUDE.local.md` (gitignored)
- CI · security · releases → [`.github/workflows/`](.github/workflows/) (the
  `ci` / `security` aggregators are the required checks). Releases: pending
  `.changeset/` fragments → knope opens a version PR (`knope-prepare.yml`).
  Merging it tags `v*` (`knope-release.yml`), and the tag fires `release.yml`.
  That release is signed and carries SBOM and SLSA, via
  [`.goreleaser.yaml`](.goreleaser.yaml). Gated on the `KNOPE_ENABLED` repo var.
- Claude Code / Anthropic API specifics → prefer current docs (context7) over memory:
  <https://docs.anthropic.com/en/docs/claude-code/overview>.
