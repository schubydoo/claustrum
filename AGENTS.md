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
| Build the binary | `make build`. CGO is off, the build uses `-trimpath`, and the binary is stripped. Output: `./claustrum` |
| Cross-build all 6 targets | `make all`. Targets: linux/darwin/windows × amd64/arm64 → `dist/` |
| Format | `make fmt` runs `gofmt -l -w *.go`, so it skips `handlers/`. `gofmt -l .` must be empty |
| Vet | `make vet` (`go vet ./...`) |
| Lint | `golangci-lint run ./...`. The configuration is `.golangci.yml` (v2) |
| Tests | `make test` (`go test -race -timeout 20m ./...`). This runs the unit suites and the socket-integration suites. The time limit is explicit, because a race run on one Linux host took more than 9 minutes, near the 10 minute default of `go test` |

- Use Go 1.26 and `CGO_ENABLED=0`. The build toolchain is deliberately held
  below 1.27. The default `jsonv2` in 1.27 moves inherited wire bytes, so build
  and CI resolve Go from `go.mod`, never from `go-version: stable`. See
  [`docs/UPSTREAM-TRACKING.md`](docs/UPSTREAM-TRACKING.md) → toolchain-induced
  drift. Every build has `github.com/klauspost/compress` (zstd) and
  `golang.org/x/sys` (its `unix` or `windows` package).
  `github.com/Microsoft/go-winio` is Windows-only.
- Tests cover the wire surface two ways: fast unit tests, and a
  socket-integration suite (`harness_test.go` + `integration*_test.go`). The
  suite boots the daemon on a temp `AF_UNIX` socket. It asserts the frames of
  every method against golden fixtures (`testdata/socket_*.golden.json`). CI
  gates compatibility without the reference binary, on linux, macOS
  and Windows. Process fixtures come from the test binary
  (`helperproc_test.go`), never from `/bin/*`, so stream bytes match the
  goldens on every OS. Statement coverage is approximately 99%.

## Architecture

There is one binary. A flag selects the mode (`main.go`): `-serve`, `-bridge`,
`-stop`, `-version`, `-install`, `-probe-cli`.

The table names the files that an agent must not break. The file map of the
modes and the OS splits is in
[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md#source-layout-flat-package-main).

| File | Role |
|------|------|
| `rpc.go` | request and response types, error codes, and the dispatch order |
| `results.go` | result structs, with fields in the order of the reference. Never a map |
| `methods_*.go` | the 20 methods across `server.*` / `files.*` / `git.*` / `launcher.*` / `process.*` / `plugins.*` |
| `process.go` | spawn, stream frames, stdin queue, replay buffer, `reattach` |
| `handlers/prune.go` | the params type of `plugins.prune`, whose name is in its `-32602` text |
| `homeguard.go` | `wipesHomeDir`, the home guard that each RPC path to a recursive delete owes |
| `methods_files.go` | the wipe of `destDir` and the `.synced` marker of `files.extract_tar` |
| `methods_plugins.go` | the delete of `plugins.prune` |
| `worktreeremove.go` | the deletes of `git.worktree_remove` |
| `worktreeverify.go` | the tests after the `worktree add` and both rollbacks of `git.worktree_create` |
| `worktreestale*.go` | the stale registration step before the add |
| `worktreestaging.go` | the Windows staging folder, its move and the cleanup of old staging entries |
| `gitdirheld.go` | the held git directory and the list of the steps that act through it |
| `githarden_index_*.go` | the placement of the index of a new worktree, and the registration delete of the second rollback (`removeCreatedRegistration`) |
| `install.go` | the CLI path delete, the sweeps, the prune and the blob remove of `-install` |
| `gitshadow.go` (`removeEntry` and `removeLink`, which each delete there goes through) · `gitshadowsweep.go` · `gitshadowrepo.go` · `gitshadowuse.go` | the deletes in the folder `git-shadow` |

Every OS serves the same JSON-RPC methods.

## Conventions

- Byte-identical wire frames are the contract. Results are ordered structs,
  not maps. Any change to `rpc.go` / `methods_*.go` / `process.go` /
  `results.go` must keep the socket-integration suite and its golden
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
- Each fact of the documentation has one home. A rule that a reader acts on
  gets one or two sentences on the page of its method under `docs/protocol/`.
  The history of completed work goes into
  [`docs/IMPROVEMENTS.md`](docs/IMPROVEMENTS.md). The history of a reference
  build goes into [`docs/REFERENCE-BUILDS.md`](docs/REFERENCE-BUILDS.md).
  Link to
  that home. Do not copy the text.
  [`docs/PROTOCOL.md`](docs/PROTOCOL.md) keeps the other sections that more than one
  method uses. The error texts, the stream frames, the flags and the shared git rules have pages
  under `docs/protocol/`. The rule is for the documents under `docs/`.
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
- A changeset body is ONE line. knope renders any multi-line body as a `####`
  heading block instead of a bullet. That breaks the changelog. Fold every
  detail into that one line. Several sentences on it are fine.
  `scripts/lint_changesets.py` gates this in CI and pre-commit.
- Before a PR, make all four of these true: `gofmt -l .` prints nothing,
  `go vet ./...` is clean, `golangci-lint run` is clean, and
  `make test` is green. For a wire-surface change, a golden fixture
  (`testdata/socket_*.golden.json`) changes only for a new reference frame or
  a catalogued divergence. Name the reason in the PR.
- CI gates every PR on the branch ruleset's required checks: `ci required checks
  passed` + `security required checks passed` + `conventional PR title`.

## Gotchas — Part A: always-on safety (hold these before touching code)

Unmarked behavior follows the reference, pinned at `5fd08069`. A D-number,
"claustrum's own", "from the code" and "not measured" mark the exceptions. A
rule that names another build follows that build. A file, function or Go API
name describes claustrum's code, not a measurement.

### Shell access

`process.spawn` runs arbitrary commands as the user of the daemon. This is by
design. Treat socket + token as equivalent to shell access. The threat model is
in [`SECURITY.md`](SECURITY.md).

### The delete paths

Five code paths give a caller-supplied or operator-supplied path to a recursive
delete. The daemon `~`-expands the RPC paths first, so `"~"` once meant
`os.RemoveAll($HOME)`. That destroyed a home directory. The five paths remain
the only recursive deletes whose target a caller or an operator names. The
deletes inside the folder `git-shadow` are not among the five: their root comes
from the socket, as in `plugins.prune` (see below).

| Path | What is deleted | Who names it | Guard | Method page |
|------|-----------------|--------------|-------|-------------|
| `files.extract_tar` | the last name of `destDir` | the caller (RPC) | `wipesHomeDir`, `destEntryHoldsHome`, `checkDestViews` | [files.extract_tar](docs/protocol/files-extract-tar.md) |
| `git.worktree_remove` | `worktreePath`, its registration | the caller (RPC) | `wipesHomeDir` on `worktreePath`, containment | [git.worktree_remove](docs/protocol/git-worktree-remove.md) |
| `git.worktree_create` rollback | `worktreePath`, its registration, one stale entry, two Windows staging removes | the caller (RPC) | `wipesHomeDir` (D2), identity tests, one exception (see Guards: which registration) | [git.worktree_create](docs/protocol/git-worktree-create.md#after-a-failure) |
| `git.worktree_create` staging (Windows) | old staging entries of the `baseRepo` git directory | the caller (RPC): a good add in `shadow` | prefix, age, `wipesHomeDir` | [git.worktree_create](docs/protocol/git-worktree-create.md#differences-by-system) |
| `-install` | the CLI path | the operator | D6, `folderHoldsHome` | [-install](docs/INSTALL.md#staging-and-cleanup) |

#### The home guard

`wipesHomeDir` refuses a target that is home or contains home. It resolves a
relative path with `filepath.Abs` first. It still permits a path under home,
because `~/.claude/…` is the install path of the daemon itself. The guard is
always-on, not opt-in. Any RPC path param that reaches a recursive delete owes
this guard. `IsAbs && !isFilesystemRoot` is not a substitute: home passes both
tests, and neither test resolves a relative path. Before `7d193f89`,
`worktreePath:".."` from a daemon whose working folder is home reached
`os.RemoveAll` on the parent of home, so the guard resolves a relative path
first. See [`docs/DIVERGENCES.md`](docs/DIVERGENCES.md) D2.

#### Path 1: `files.extract_tar`

##### What is deleted

`files.extract_tar` wipes `destDir`. The wipe removes one name as a tree: the
last name of the cleaned `destDir`. It goes through a handle of the parent
folder of `destDir` (`wipeDestDir`). The handle follows no link and no junction,
and it refuses the names `.` and `..`, so the wipe cannot take the parent folder
by those names. On Windows the handle refuses a last name of dots and spaces
alone too, such as `...`.

##### Who names the path

The caller, in `destDir` (RPC).

##### Guards, in order

1. `wipesHomeDir` (`homeguard.go`) guards `destDir`, before any step of the
   request.
2. Before the wipe, a folder at the last name that is the home folder, or a
   parent folder of it, gets the home refusal by identity
   (`destEntryHoldsHome`, which uses `folderHoldsHome`). On Linux and macOS
   that covers a path through a link above the last name. On Windows the text
   compare refuses a dot or a space after home or after a folder above it,
   because the path is resolved first. It lets a path with the `\\?\` prefix
   and the short 8.3 name of home pass, and the test by identity refuses
   those.
3. Then the name in the handle and the cleaned path must name one entry
   (`checkDestViews`), or nothing is deleted. On Windows the handle and a path
   with the `\\?\` prefix read a name with a dot or a space after it in two
   ways. Both tests follow the mkdir again. If the two views differ there, the
   entry at that name goes with a plain `os.Root.Remove`, never as a tree.

No remove runs after a home refusal. The identity test, the view test and the
open test are claustrum's own (D2).

##### Never widen

This delete path is approved by the maintainer. Do not widen it.

##### Detail

[files.extract_tar](docs/protocol/files-extract-tar.md).

#### Path 2: `git.worktree_remove`

##### What is deleted

`git.worktree_remove` deletes `worktreePath` itself. It runs no `git worktree
remove`. Order, stops and a missing `baseRepo`: [What the delete
removes](docs/protocol/git-worktree-remove.md#what-the-delete-removes). Without
`worktreeRoot` and with a daemon `GIT_DIR` and `GIT_COMMON_DIR`, `<git dir>` is
the repository that `GIT_DIR` names. The remove holds no git directory, and the
swap of `.git` during a remove is in [Rare
states](docs/protocol/git-worktree-remove.md#rare-states). Each delete goes
through an `os.Root`, so no delete follows a symlink out of the leaf or its
parent.

##### Who names the path

The caller, in `worktreePath` and `baseRepo` (RPC).

##### Guards

- A leaf that is a symlink or not a directory is refused.
- A worktree LOCKED in the `.git` folder of `baseRepo` is refused, not deleted
  (D22), with one exception: see Never widen.
- A repository config, or one of three files of the git directory, that the
  daemon does not accept refuses the remove before any delete.
  `"shadowDirOff":true` in the request turns that refusal off. A remove that
  starts while a `"shadowDirOff":true` request runs gets no such refusal
  either. It then deletes on a config that only the daemon refuses. A failed
  start test of the settings copy refuses the remove before any delete too. So
  do a bad answer of git about the repository and a copy folder that cannot be
  made. Detail: [The repository
  checks](docs/protocol/git-worktree-remove.md#the-repository-checks) and
  [Repository config
  refusals](docs/protocol/git-rules-config.md#repository-config-refusals).
- On Windows a junction at any directory between `baseRepo` and the leaf is
  refused too. At a junction at `.claude` or `.claude\worktrees`, `89cb6289`
  answers success and deletes the branch, for a branch that another ref
  reaches. At a junction above `.claude`, it answers success with no leaf and
  no branch there. That is D19, approved by the maintainer.
- The containment of the reference refuses a home path first: `worktreePath`
  must be strictly inside `baseRepo`. On the default branch `wipesHomeDir` is
  therefore defense-in-depth. It fires in one case only: a repo is an ancestor
  of home. On the `worktreeRoot` branch that in-repo containment does not
  apply, so there `wipesHomeDir` is the active home guard. Both branches run it
  before the delete.
- With `worktreeRoot`, the location refusals delete nothing. They refuse a
  reserved name, a root that holds or leads into the repository, a root inside
  a worktree of it, a root that resolves to `/`, and a link loop.

##### Never widen

On Linux and macOS a gone worktree whose locked entry sits behind a linked `<git
dir>/worktrees` gets success, and its branch goes. That exception follows the
reference. Do not widen it.

##### Detail

[git.worktree_remove](docs/protocol/git-worktree-remove.md).

#### Path 3: the `git.worktree_create` rollback

##### What is deleted

`git.worktree_create` deletes before the add and in a rollback. A rollback
deletes `worktreePath`. There are two rollbacks. After a failed `git worktree
add`, it runs no git call and removes the leaf only if the leaf is an empty
directory. The second rollback follows a successful add. It deletes the entries
of the leaf in the order that the directory read returns them, and it stops at
the first failure. Then it deletes the registration, runs the branch step on the
created branch, and then removes the empty leaf. What a rollback leaves, and a
registration that cannot be deleted: [When a rollback leaves
something](docs/protocol/git-worktree-create.md#after-a-failure).

##### Who names the path

The caller, in `worktreePath` and `baseRepo` (RPC).

##### Guards: stale entry

Before the add, create removes one stale entry of `worktrees` in the common git
directory of `baseRepo`. The stale test: [After a
failure](docs/protocol/git-worktree-create.md#after-a-failure). Create removes
that entry only if it is the only stale entry and holds no `locked` file.
Windows keeps an entry with a `locked` file too (`windowsStaleEntryGoes`). The delete is one
`os.Root.RemoveAll`, after `wipesHomeDir` on the entry path. It goes through a
root at that folder. When a Windows create holds the git directory, it goes
through the held folder. On Windows the step runs after `rev-parse
--absolute-git-dir`. It deletes nothing behind a junction or a directory
symbolic link at that folder (claustrum's own in the state `shadow`). Its
compare ignores letter case. A create that holds no git directory is not
measured there. An entry whose record names a path that exists stays (D27,
Windows).

##### Guards: refusals

On Linux and macOS these refusals come before the second rollback and before the
checkout. They delete nothing, except an empty leaf that has no `.git` file.
They answer a `.git` file of the leaf that is gone, is no regular file or has no
`gitdir: `. They answer a registration that is a symbolic link and a `commondir`
path through a symbolic link. They answer a record that names another worktree
and a leaf that is no longer the folder of the create. They also answer a `.git`
file that names no registration, a `commondir` file or a `gitdir` record that
cannot be read, a registration that is not the one new name of its folder, and a
listing that fails. One more state deletes before the checkout. The record names
another path, the `rev-parse --show-toplevel` call fails by itself, and the
folder has one new name. The create then runs the second rollback on that name,
never on the name in the `.git` file. A show-toplevel call that a time limit
stopped starts no rollback (claustrum's own, not measured).

##### When the second rollback runs

The second rollback runs when the `timeoutMs` of the caller expired during the
add, the checkout or the copy step. It also runs after a post-checkout drain
that exceeded that `timeoutMs`, a failed read-tree checkout, a failed
`config.worktree` copy and a failed placement of the index of the new worktree.
The filter rule of the checkout (`gitfilter.go`) leads to it after a read-tree
call that fails at a filter of the repository. So does a refusal before it. In
the state `shadow` that is a refusal for an unusable filter. In a create with no
settings copy it is a refusal for a failed rule listing. In the state
`fallback-git` the rule does not run, and git runs the filter. In the state
`shadow` these refuse too: over 64 filter names, and a last listing that fails
or runs at 30 s and does not exit 0. So does a first checkout listing that
failed or that its 30 s limit stopped.

##### Guards: registration

On Linux and macOS, if git answered `rev-parse --absolute-git-dir`, the
registration of the rollback is the entry that the tests after the add accepted.
That is the entry of the registrations directory of `baseRepo` with the last
name of the `gitdir:` path, read once after the add. After a failed `rev-parse
--show-toplevel` it is the one new name above. The rollback reads neither the
`.git` file of the leaf nor the `gitdir` record again. That follows `89cb6289`,
which removes that entry when either one changed. The delete is one
`os.Root.RemoveAll` of that name through a root at the registrations directory,
after `wipesHomeDir` on the entry path. After the open of the root, and through
it, the entry must still have its accepted identity. Create holds the accepted
folder open until it answers. While that handle is open, a new folder does not
get that identity. If the open fails, the identity comes from a stat of the path
(not measured). The rollback deletes nothing if the folder at the path is not
the accepted one at that test (D24). In the state `shadow` a failed `rev-parse
--absolute-git-dir` stops the create. So "no answer of git" below is a create
outside that state, or an empty answer.

##### Guards: which registration

A create that holds its git directory deletes the registration by its name
through the held folder. On Windows it is the moved entry. If `.git` is no
longer that folder, the rollback deletes no registration. A create that holds
none, and has no answer of git or runs on Windows, takes the folder that the
`.git` file of the leaf names. If its `gitdir` record does not name the leaf,
the rollback does not delete it. If it does not resolve strictly inside the
registrations directory of `baseRepo`, the same holds. That delete acts on the
resolved path that its check verified, and on Linux and macOS through a root at
the registrations directory, which must still have the identity that the check
saw. It runs no `wipesHomeDir`, and on Windows it is a plain `os.RemoveAll` with
no root.

##### Guards: index

On Linux and macOS the placement of the index removes a file, a link or an empty
folder at `<registration>/index`, after a create there answered "file exists".
That is one `os.Root.Remove` of the fixed name `index`, never a tree. The
registration of the placement is that same entry. With no answer of git it is
the folder that the `gitdir:` path names. If git answered, the create refuses
before the checkout an entry whose `gitdir` record cannot be read or names
another path, as `89cb6289` does (Linux and macOS). The placement then reads no
record again: a record that goes during the checkout does not stop it. The
placement has the identity test of the rollback, on the root that it opened. It
places nothing if the folder at the path is not the accepted one at that test
(D24, not measured). With no answer the registration tests do not run. An entry
then gets the index only if its `gitdir` record can be read and names the new
worktree. An entry folder whose stat fails takes the placement itself, which
fails there. With no answer of git, a registration whose back-pointer cannot be
read is never deleted.

##### Guards: leaf

`wipesHomeDir` guards every delete of the leaf as defense-in-depth behind the
containment that create applies itself. Create also tests the checkpoint
identity of the leaf again, so a swap while create runs cannot redirect a
delete. Create holds the leaf and its parent open until it answers, so a
replacement cannot reuse the identity of the leaf. On Windows both handles share
delete access, so they do not block these deletes. The empty-leaf rmdir tests
only two things: the path still resolves to the leaf, and the parent is still
the held parent. An rmdir cannot delete content.

##### Never widen

Never widen a delete of the [two steps that move
the hold](#two-steps-that-move-the-hold).

##### Detail

[git.worktree_create](docs/protocol/git-worktree-create.md#after-a-failure).

#### Path 4: the `git.worktree_create` staging sweep (Windows)

##### What is deleted

On Windows `git.worktree_create` removes old staging entries of the git
directory of `baseRepo` (`sweepOldStaging`, `worktreestaging.go`). Those are
entries whose name starts with `.claude-new-worktree-`. The daemon did not make
them. Only a real folder goes as a tree, with one `os.Root.RemoveAll` of that
name. A file, a link and a junction go with one plain `os.Root.Remove`, so the
target of a junction stays.

##### Who names the path

The caller: `baseRepo` comes from the request.

##### Guards, in order

The step runs in the state `shadow` only, after a good `git worktree add` and
before the daemon makes its own staging folder. The cleanup and the move act
through the held git directory (see below).

1. Before the cleanup the path must still name that folder. If not, the create
   is refused, and nothing is removed or made in the git directory or in the
   folder at its path. The entries are listed through that root, so each name
   is a direct entry of the git directory.
2. The name must start with the fixed prefix, with the dash. The entry is read
   with `Lstat` through the root, so no link and no junction is followed. Its
   own write time must be 1 hour old or more.
3. `wipesHomeDir` then refuses an entry that is home or holds it.

An entry that cannot be removed logs one line, and the create goes on (not
measured). `git.info` and `git.status` remove no entry. From the code: a create
whose `worktree add` fails, a request with `"shadowDirOff":true` (or one that
starts while such a request runs and takes no later gate) and the state
`fallback-git` run no cleanup. Those three are not measured. A request with a
later gate runs it. Linux and macOS run no such step.

##### Never widen

This step is approved by the maintainer. Do not widen the prefix, the kinds or
the folder of this delete.

##### Detail

[git.worktree_create](docs/protocol/git-worktree-create.md#differences-by-system).

#### Path 5: `-install`

##### What is deleted

`-install` deletes the CLI path, which is operator input:
`<cli-dir>/<cli-version>`, and `<cli-version>.exe` on Windows. The cli-dir is
`-cli-dir`, or `<home>/.claude/remote/ccd-cli` without it. A folder at the CLI
path goes as a tree BEFORE the new CLI runs. The new CLI replaces a file there.
A new CLI whose `--version` run does not start or exits non-zero gets one plain
remove. A stopped run keeps it. On Windows claustrum can leave that file when
the exited CLI holds it (claustrum's own code, not measured).

- `-install` also removes two operator-named paths with a plain `os.Remove`,
  never as a tree (`clearNonFolderCLIDir`). The first is the cli-dir entry, only
  when it is a regular file, a symlink to a regular file or a FIFO. The second
  is the regular file named exactly `ccd-cli-version` in the parent folder of
  the cli-dir. No other name and no other file kind is removed. The rest:
  [Staging and cleanup](docs/INSTALL.md#staging-and-cleanup).
- `-install` also sweeps the cli-dir. Each sweep removes regular files by name
  with a plain `os.Remove`, never as a tree. A folder and a link (from the code)
  stay. Neither sweep names the version. The 10 minute sweep takes
  `.fetch-<digits>` and `<version>.zst`. It runs before a new CLI runs, after a
  failed attempt and after a D6 refusal. On a cache hit it runs only after a
  `--version` run that its bound stopped, as on `89cb6289`. The other sweep and
  the `-cli-keep` prune: [Sweeps and prune](docs/INSTALL.md#sweeps-and-prune).
- `-install` removes the `-cli-zst` blob, an operator-named path, with one
  plain `os.Remove` once the new CLI is in place, before it runs. Every earlier
  failure keeps the blob. The chmod case is claustrum's own. No guard runs
  before the remove beyond that: the path was read as a zstd archive first.

##### Who names the path

The operator: `-cli-dir`, `-cli-version` and `-cli-zst`.

##### Guards, in order

1. D6 runs first: `makeCLIDir` and `ensureCLI` run it before they write or
   remove anything at a path built from the version. D6 makes the version one
   path component, so the CLI path is a direct child of the cli-dir. D6 does not
   test which folder that child is.
2. With a cli-dir that is the parent of the home folder and a version that is
   its leaf name, the CLI path is the home folder. So `folderHoldsHome` runs
   right before the tree delete and refuses a CLI path that is home or contains
   it (D2). It runs `wipesHomeDir` first. Then it compares the folder at the CLI
   path with home and with each parent folder of home by identity
   (`os.SameFile`). It does that for the given and the resolved home path. The
   reference deletes home there.

The prune and the sweeps skip an entry of the cli-dir that is the home folder or
holds it (`cliEntryHoldsHome`). A folder gets the tests of `folderHoldsHome`,
and every other kind gets `wipesHomeDir`. That guard is claustrum's own (D2).

##### Never widen

Do not widen either delete of `clearNonFolderCLIDir` or build either path from
anything but the cli-dir. Do not turn a remove of the prune or the sweeps into a
tree delete.

##### Detail

[-install](docs/INSTALL.md#-install--ensure-the-agent-cli).

### `plugins.prune`

`plugins.prune` is a further `os.RemoveAll` site, and it is safe by
construction. It deletes `<socket-derived-root>/<hash>`. The root comes from the
socket layout of the daemon itself, not from an RPC or operator path. The leaf
must match `^[0-9a-f]{16}$` (`pluginHashRE`). The path is never caller-supplied
and never `~`-expanded, so it needs no `wipesHomeDir` guard.

### The `git-shadow` folder

The folder `git-shadow` of the settings copy is a further recursive delete site,
and it is socket-derived like `plugins.prune`. Each delete there is
`wipesHomeDir` on the full path, as defense in depth, and then one call through
a root at `<socket folder>/git-shadow` (`gitshadow.go`). That is
`os.Root.RemoveAll` (`removeEntry`), or a plain `os.Root.Remove` for a link that
the sweep found (`removeLink`). The name below that root is one that this daemon
made, or one that a directory read of `git-shadow` or of the folder of the
daemon returned. No part of the path comes from an RPC value or from an
operator.

- The test folder `.check-<n>`, after each start test, also a failed one.
- The folder of the daemon, with each folder of a repository in it. It goes at
  shutdown (`shadowHost.close`, from `closeAll`), after its own lock file is
  closed. It also goes right after a lock step that failed. The empty
  `git-shadow` stays.
- A copy in the folder of a repository whose file in the repository is gone, and
  a `rebase-merge` or `rebase-apply` folder with it. Only a copy that this
  daemon wrote goes. Also a temp file `.tmp-<n>` that a failed write left.
- The short-lived entries of one request: `.gitdir-<n>`, `.index-<n>`,
  `.worktreeinclude-<n>`, `.config-<n>`, and `.s<n>` on Windows. Each goes at
  the end of its step or at the reply. `.config-<n>` goes right after the file
  of the registration is written. It goes at the reply of a create that is
  refused before that step.
- The sweep of `sweepOldFolders`. An entry goes only if its lock can be taken,
  and only with this host form or when it is old. A folder with no lock stays 1
  minute (claustrum's own). A link goes as a link, and nothing is made in its
  target (D8).
- One repository folder at 128 (`evictOldRepoFolder`).

On Linux and macOS the folder of a repository holds a link `worktrees` into the
git directory. The root removes that link and not its target
(`TestShadowFolderRemovalLeavesLinkTargets`). A new copy is a temp file plus one
`os.Root.Rename` inside the folder of the repository. With `"shadowDirOff":true`
the short-lived folders are `os.MkdirTemp` folders in the temporary directory,
and each goes with `os.RemoveAll`, as before. Do not build a name below
`git-shadow` from a request value.

### The extract marker step

The marker step of `files.extract_tar` is a further recursive delete, and it is
safe by construction. After the entries it removes the fixed name `.synced` at
the top of `destDir` as a tree (`writeSyncedMarker`). Then it creates the empty
marker file. The remove is one `os.Root.RemoveAll`, and the create is exclusive.
Both go through a handle of `destDir`, so neither follows a link out of
`destDir`. That handle is opened through a handle of the parent folder. After
the open, `extractTarGz` tests it against the folder that `wipesHomeDir` judged:
`destDir` must be a real directory and the folder of the handle. If not, the
request fails before any entry and before the remove (claustrum's own, D2). The
remove takes whatever sits at `.synced` in that folder at that moment. In a
normal run that is the entry of the archive. After a directory entry `.synced/`
with content, an empty file stays there. On Linux and macOS the file has mode
`0600`. This step is approved by the maintainer. Do not widen it. Do not build
that name from a parameter or from the archive. A second file entry with the
name of an earlier one removes that one regular file through the same handle.
That is a plain `os.Root.Remove`, not a tree. The marker remove acts inside the
first of the five paths.

### The held git directory

In the state `shadow` `git.worktree_create` holds the git directory of
`baseRepo` open from its gate to its reply (`acceptedGitDir`). After a gate
under the off switch, claustrum takes the hold at a later gate before the first
good add (claustrum's own). A stale step before that gate goes by the path.
[git.worktree_create](docs/protocol/git-worktree-create.md#the-held-git-directory)
has the rules for a reader. Hold these rules:

- Each delete, rename, new folder and file write of the create and of its
  rollback inside the git directory goes through the held folder, never through
  the path. The comment at the top of `gitdirheld.go` has the list of the steps.
- By the path stay: git itself, a request that holds no git directory
  (`"shadowDirOff":true` or a start while such a request runs, until its later
  gate, the state `fallback-git`, no run folder), and on Linux and macOS the
  index in a `worktrees` entry that is a symbolic link, with the identity test
  of D24. So does the stale step for a folder `worktrees` outside the held
  folder (from the code).
- On Linux and macOS no delete of a create or a remove goes behind a
  `worktrees` entry that is a symbolic link. A remove of an existing worktree
  folder is refused there.

#### Two steps that move the hold

Two steps take a new folder from the path, on Linux and macOS. After `rev-parse
--absolute-git-dir`, a path that leads to another git directory which passes the
gate becomes the held folder (`acceptedGitDir.movedTo`). After a good `worktree
add`, a new real directory at the path that passes the gate does too
(`replacedAtPath`, approved by the maintainer). Another git directory is not
measured. Add no other step that resolves the path of the git directory again,
and never widen a delete of these steps. A rollback after the first move is not
measured. After the second, the rollback acts through that new folder.

#### Links at `.git` on Windows

On Windows a directory symbolic link at `.git` is refused as a junction is, on
every git method without `"shadowDirOff":true`, before any write or remove
(`gitDirIsSymlink`). The folder behind the link stays unchanged. With
`"shadowDirOff":true` git makes and removes the worktree behind either link at
`.git`. With the member, either link at `.git\worktrees` refuses the create
after its listing, and the leaf goes. A request started during one gets both
rules until its later gate (from the code, not measured).

### What `git.worktree_create` writes

In the state `shadow` `git.worktree_create` writes into the git directory of
`baseRepo` and into the new worktree. Only the two removes of the staging step
below are tree deletes. Each goes through an `os.Root`, with a fixed name, a
name that the daemon read through that root, or a name that it made. The steps
are these.

#### The `worktrees` folder

The folder `<git dir>/worktrees` (0777 less the umask), made before the add
through the held folder. It stays after a failed add. On Linux and macOS, a last
`core.sharedrepository` copy line other than `umask` sets its mode by one
`os.Root.Chmod`, with the set-group-ID bit, only on a folder that this call
made. macOS is not measured.

#### The `.git` file

On Linux and macOS, the `.git` file of the new worktree
(`replaceWorktreeGitFile`). It is one `os.Root.Remove` of the fixed name `.git`
and one exclusive create with mode 0666, through a root at the leaf. Three tests
come first. The leaf must be the folder of the checkpoint, by identity with the
held handle. `.git` must be a regular file of at most 8192 bytes. Its text must
be `gitdir: `, the folder of the copy, `/worktrees/`, one name with no
separator, and a newline. Each other text stays as it is.

#### The `config.worktree` file

The new file `<git dir>/worktrees/<name>/config.worktree` (0666 less the umask)
(`writeWorktreeConfig`). It goes through the held folder and then a root at the
registration. The registration must be a direct entry of `<git dir>/worktrees`
and a real folder. It must be the folder that the tests after the add accepted,
or the moved one on Windows, by identity. After an empty answer of git, its
`gitdir` record must name the leaf. On Linux and macOS an entry there goes
first, by one plain `os.Root.Remove` through that root, and the create is
exclusive. On Windows a folder there fails. A file there gets the new text. The
remove before the create is claustrum's own. Before that step the daemon runs
`git config --file <its own file> --unset-all` on a file in `.config-<n>` of its
own folder.

#### The staging folder (Windows)

On Windows, the staging folder `<git dir>\.claude-new-worktree-<16 hex>` with a
copy of the whole staged registration, and one `os.Root.Rename` of it to the
first free name of `<name>`, `<name>1` to `<name>99` in `worktrees`
(`addStage.move`). The folder and its files are new, made with exclusive creates
through the held root. Before that the `.git` file of the leaf must name the
staged entry, and then it is replaced as above. A failed step after its mkdir
removes it as a tree through the held root, by the name that this call made,
after `wipesHomeDir`. If the `.git` file cannot be replaced, the moved
registration goes too, by its name. Its identity test against the held staging
folder is claustrum's own.

#### The index (Windows)

On Windows the index is a new file of the moved registration, through the held
folder, made as `config.worktree` is. A create with no moved registration uses
`index.tmp-<n>` and a rename, through the held folder when it holds one.

### Auth and token

Auth is in-band per request (`"auth":"<token>"`). The token of the daemon comes
from `-token-file` or from `-token-fd`. With `-token-file` the daemon reads the
file once and then unlinks it, so the token never lands in
`/proc/<pid>/environ`. With `-token-fd` the daemon reads the token from an open
descriptor. It forwards the token to the daemonized child over a pipe, so this
handoff never touches disk. No mode reads `CLAUDE_RPC_TOKEN`: `-bridge` relays a
client with its own `auth`, and the daemon strips the variable from spawned
children. `server.shutdown` is the one unauthenticated method. That is parity
with the reference. Desktop stops the daemon with no token in its environment
([driver claim](docs/ARCHITECTURE.md#driver-claims-and-their-provenance)), so
`-stop` sends no `auth` member. Every other method rejects an unauthenticated
request `-32001`.

### Token persistence and the run-dir lock

The daemon persists its token to `daemon.token` (mode `0600`) beside the socket: [Token persistence](docs/DAEMON.md#token-persistence-daemontoken)
(`tokenpersist.go`). The fixed name and the socket-dir location are the
reconnect contract, so they are deliberately not configurable. One parity caveat
remains: on Windows `0600` is not an owner-only DACL. Do not "fix" that caveat
without making the change an opt-in divergence. With the run-dir lock
(`daemon.lock`, parity with `4534d86`), a new daemon on Linux and macOS evicts a
prior live same-socket daemon before it binds. Two cases remain: eviction is
refused, or the host is Windows, which ships no run-dir lock. That lock is
parity, not a claustrum "fix". The macOS holder-verification that claustrum
applies to the lock is a hardening divergence (D15). See
[`docs/DAEMON.md`](docs/DAEMON.md) → Token persistence / Run-dir lock.

### Concurrency and panics

The requests of one connection dispatch concurrently. Parse and auth errors are
the exception: the read loop writes them ([`docs/PROTOCOL.md`](docs/PROTOCOL.md)
→ Transport). Replies can return out of order, which matches the reference. Do
not serialize them. The per-request goroutine recovers from panics. It replies
with `-32603 "recovered panic: <v>"`. For `server.shutdown` it writes no frame
at all. That frame is claustrum's own and is NOT a parity claim. The path is
unreachable, so no client can observe it and it cannot diverge from anything. Do
not add a golden for that frame. Do not treat it as a wire contract. The tests
provoke it through the `dispatchRequest` seam.

### Network

`-serve` makes no outbound network connections, and `-install` reaches the
network only with `-cli-url`: [SECURITY.md → Network](SECURITY.md#network). The
download is verified by SHA-256, unconditionally.

### Time bounds

The `ldd` libc probe bounds ldd itself at 5 s (`lddProbeTimeout`). The direct
`<cli> --version` run of `-install` is stopped at 30 s on a cache hit
(`cliRunBound`). After an install in the same run it is stopped at 120 s
(`cliFirstRunBound`). The old flags `-libc-probe-timeout` and
`-cli-probe-timeout` are deprecated no-ops, and each logs one warning.
`-cli-download-timeout` sits beside them in the `-install` arm of `main`. A line
that sets one of these bounds from a flag compiles and passes every isolated
test. `TestInstallArmWiresEachFlagToItsOwnGlobal` is the guard against it.

### The limiter rule

A disabled limiter bypasses its `io.LimitReader` / `context.WithTimeout`
entirely. Never "simplify" it into a huge value. For the caps, the `cap+1` (or
`max-total+1`) arithmetic defines the boundary. For the deadlines, an unarmed
cancel path makes the timeout `false` by construction. A huge constant is a
different, observable behavior. This rule applies to every opt-in cap and bound
in Part B.

### Time limits of the git calls

The git calls of `git.list_branches`, `git.info`, `git.status` and
`git.worktree_create` have time limits (`gitstop.go`). They are always on and
are not the limiter of a divergence, and [Time limits of git
calls](docs/protocol/git-rules-calls.md#time-limits-of-git-calls) lists them.
`git.worktree_remove` gets none of these limits, and `-git-timeout` is a no-op
that logs one warning. The branch step has its own bounds ([Time limits of the
branch step](docs/protocol/git-rules-calls.md#time-limits-of-the-branch-step)).

## Gotchas — Part B: the live divergences

A divergence is a deliberate difference from the reference. It is not a bug.
Do not "fix" one toward the reference without the maintainer. The table lists
every live entry of [`docs/DIVERGENCES.md`](docs/DIVERGENCES.md), which holds
the rules, the cost and the reopen trigger of each.

| ID | Summary | Default | System |
|----|---------|---------|--------|
| [D2](docs/DIVERGENCES.md#d2) | Refuse a destructive path that is home or contains home, on three methods and on `-install` | always-on | all |
| [D3](docs/DIVERGENCES.md#d3) | Cap the output size of `files.extract_tar` | off | all |
| [D6](docs/DIVERGENCES.md#d6) | `-cli-version` must be one path component | always-on | all |
| [D8](docs/DIVERGENCES.md#d8) | Refuse a link, a FIFO and, on Linux and macOS, another user's `remote-server.log` at the start. An own link to an own file passes on Linux and macOS. The `git-shadow` sweep makes no lock file in the target of a link | always-on | all |
| [D10](docs/DIVERGENCES.md#d10) | Cap the CLI size of `-install` (decompressed and download body) | off | all |
| [D12](docs/DIVERGENCES.md#d12) | Bound the download exchange of `-install` | off | all |
| [D15](docs/DIVERGENCES.md#d15) | Verify that a run-dir lock holder is our serve process before a signal, in the serve eviction and in `-stop` | always-on | macOS |
| [D16](docs/DIVERGENCES.md#d16) | `git.status` of a linked worktree returns the status. With no global excludes file of the user, the reference errors there | always-on | Windows |
| [D19](docs/DIVERGENCES.md#d19) | `git.worktree_remove` refuses a junction at any directory between `baseRepo` and the leaf | always-on | Windows |
| [D20](docs/DIVERGENCES.md#d20) | Wait 50 ms and read again before the group `SIGKILL` of a child-group leader that reads as gone, at the reap of a `-serve` start | always-on | Linux, macOS |
| [D22](docs/DIVERGENCES.md#d22) | `git.worktree_remove` refuses a worktree locked in the `.git` folder of `baseRepo`. Part A names the one exception | always-on | all |
| [D23](docs/DIVERGENCES.md#d23) | The environment block of a child is in name order | always-on | Windows |
| [D24](docs/DIVERGENCES.md#d24) | The rollback of `git.worktree_create` keeps a folder that replaced the new registration. On Windows a rollback after `.git` became a file that names a second repository runs no git call | always-on | all |
| [D27](docs/DIVERGENCES.md#d27) | The stale step of `git.worktree_create` keeps an entry whose `gitdir` record names a path that exists | always-on | Windows |
| [CT-1](docs/DIVERGENCES.md#ct-1) | Opt-in `wantPid` adds `pid` and `startTime` on spawn and reattach | off (the caller asks) | all |
| [CT-2](docs/DIVERGENCES.md#ct-2) | `-keep-children` leaves the child tree running on shutdown | off | all |
| [CT-3](docs/DIVERGENCES.md#ct-3) | The `claustrum.conf` config file | off (no file) | all |
| [CT-4](docs/DIVERGENCES.md#ct-4) | Hardened token persistence, a deferred idea | not built | none |
| [CT-5](docs/DIVERGENCES.md#ct-5) | The `-listen-pipe` named-pipe transport | off | Windows |

The three caps and bounds D3, D10 and D12 default off. That is the parity
position: a non-off default fails an operation that the reference completes.
Claude Desktop owns the `-serve` / `-install` argv, so the `claustrum.conf`
key is the reachable knob, not the flag. Each disabled state bypasses its
limiter entirely, by the "never simplify" rule of Part A.

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
