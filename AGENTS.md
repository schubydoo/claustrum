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
| Format | `make fmt` runs `gofmt -l -w *.go`, so it skips `handlers/`. `gofmt -l .` must be empty |
| Vet | `make vet` (`go vet ./...`) |
| Lint | `golangci-lint run ./...`. The configuration is `.golangci.yml` (v2) |
| Tests | `make test` (`go test -race -timeout 20m ./...`). This runs the unit suites and the socket-integration suites. The time limit is explicit, because a race run took more than 9 minutes, near the 10 minute default of `go test` |

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
[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md#source-layout-flat-package-main).

| File | Role |
|------|------|
| `rpc.go` | types, error codes, `gate` (read loop), `route` (`dispatchRequest`) |
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

Unmarked behavior follows the reference, pinned at `52d82f0a`, except its
terminal. It was measured there, on `c8e2f083` or on an earlier build. A D-number,
"claustrum's own", "from the code" and "not measured" mark the exceptions. A
rule that names another build follows that build. A file, function or Go API
name describes claustrum's code, not a measurement.

`process.spawn` runs arbitrary commands as the user of the daemon. This is by
design. Treat socket + token as equivalent to shell access. The threat model is
in [`SECURITY.md`](SECURITY.md).

### The delete paths

Five code paths give a caller-supplied or operator-supplied path to a recursive
delete. The daemon `~`-expands the RPC paths first, so `"~"` once meant
`os.RemoveAll($HOME)`. That destroyed a home directory. The five paths remain
the only recursive deletes whose target a caller or an operator names. The
deletes inside the folder `git-shadow` are not among the five: their root comes
from the socket, as in `plugins.prune`.

| Path | What is deleted | Who names it | Guard | Method page |
|------|-----------------|--------------|-------|-------------|
| `files.extract_tar` | the last name of `destDir` | the caller (RPC) | `wipesHomeDir`, `destEntryHoldsHome`, `checkDestViews` | [files.extract_tar](docs/protocol/files-extract-tar.md) |
| `git.worktree_remove` | `worktreePath`, its registration | the caller (RPC) | `wipesHomeDir` on `worktreePath`, containment | [git.worktree_remove](docs/protocol/git-worktree-remove.md) |
| `git.worktree_create` rollback | `worktreePath`, its registration, one stale entry, two Windows staging removes | the caller (RPC) | `wipesHomeDir` (D2), identity tests, one exception (see Registration and index) | [git.worktree_create](docs/protocol/git-worktree-create.md#after-a-failure) |
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
first.


#### Path 1: `files.extract_tar`

The caller names `destDir`. The wipe removes the last name of the cleaned `destDir` as a
tree, through a handle of its parent folder (`wipeDestDir`). The handle follows no link and no
junction and refuses the names `.` and `..`. On Windows it refuses a last name of dots and spaces
alone too.

Guards, in order: `wipesHomeDir` on `destDir` before any step. Then `destEntryHoldsHome`
(identity, through `folderHoldsHome`) before the wipe. Then `checkDestViews`: the name in
the handle and the cleaned path must name one entry, or nothing is deleted. On Windows, if
the two views of a name with a dot or a space differ, the entry goes with a plain
`os.Root.Remove`. No remove runs after a home refusal. The identity, view and open tests
are claustrum's own (D2).

Never widen: This delete path is approved by the maintainer. Do not widen it.

Detail: [files.extract_tar](docs/protocol/files-extract-tar.md).

#### Path 2: `git.worktree_remove`

The caller names `worktreePath` and `baseRepo`. The remove deletes `worktreePath` itself
and its registration, and runs no `git worktree remove`. Each delete goes through an
`os.Root`, so none follows a symlink out of the leaf or its parent. The remove holds no
git directory.

Guards: a leaf that is a symlink or not a directory is refused. A worktree LOCKED in the
`.git` folder of `baseRepo` is refused (D22), with one exception: see Never widen. A
repository config, git directory file or settings copy that the daemon does not accept
refuses the remove before any delete. `"shadowDirOff":true` turns that refusal off, and so
does a remove that starts while such a request runs ([The repository
checks](docs/protocol/git-worktree-remove.md#the-repository-checks)). On Windows a junction
at any directory between `baseRepo` and the leaf is refused (D19, approved by the maintainer). Without `worktreeRoot`,
the containment of the reference refuses a home path first, so `wipesHomeDir` fires only
when a repo is an ancestor of home. With `worktreeRoot` it is the active home guard. Both
branches run it before the delete. The location refusals of `worktreeRoot` delete nothing.

Never widen: On Linux and macOS a gone worktree whose locked entry sits behind a linked
`<git dir>/worktrees` gets success, and its branch goes. That exception follows the
reference. Do not widen it.

Detail: [git.worktree_remove](docs/protocol/git-worktree-remove.md).

#### Path 3: the `git.worktree_create` rollback

The caller names `worktreePath` and `baseRepo`. After a failed `git worktree add`, the
first rollback runs no git call and removes the leaf only if it is an empty directory. After
a good add, the second rollback deletes the entries of the leaf in directory-read order,
stops at the first failure, deletes the registration, runs the branch step, and removes the
empty leaf ([When a rollback leaves
something](docs/protocol/git-worktree-create.md#after-a-failure)). The second rollback runs when `timeoutMs` expired during the add, the checkout or the
copy step, after a post-checkout drain that exceeded that `timeoutMs`, a failed checkout, `config.worktree` copy or index placement, and a
refusal of the filter rule (`gitfilter.go`, [Git
filters](docs/protocol/git-worktree-create.md#git-filters)).

##### Leaf, stale entry and refusals

- **Leaf.** `wipesHomeDir` guards every delete of the leaf. Create tests the checkpoint
  identity of the leaf again and holds the leaf and its parent open until it answers. On
  Windows both handles share delete access. The empty-leaf rmdir tests only that the path
  still resolves to the leaf and the parent is still the held parent.
- **Stale entry.** One `os.Root.RemoveAll` of one stale `worktrees` entry, after
  `wipesHomeDir` on its path, through a root at that folder (the held folder when held).
  Create removes that entry only if it is the only stale entry and holds no `locked` file.
  Windows keeps an entry with a `locked` file too (`windowsStaleEntryGoes`) and an entry
  whose record names a path that exists (D27). It deletes nothing behind a junction or a
  directory symbolic link at that folder (claustrum's own in the state `shadow`). A Windows
  create with no held git directory is not measured.
- **Refusals** (Linux and macOS). Before the second rollback and the checkout, these delete
  nothing, except an empty leaf with no `.git` file: a `.git` file of the leaf that is gone,
  no regular file or without `gitdir: `. A registration that is a symbolic link and a `commondir` path through a symbolic link. A record that names another worktree. A leaf that is no longer the folder of
  the create. A `.git` file that names no registration. An unreadable `commondir` or
  `gitdir`. A registration that is not the one new name of its folder. A failed listing. One
  state deletes before the checkout: the record names another path, `rev-parse
  --show-toplevel` fails by itself, and the folder has one new name. The second rollback
  then acts on that name, never on the name in the `.git` file. A show-toplevel call that a time limit stopped starts no rollback
  (claustrum's own, not measured).

##### Registration and index

- **Registration** (Linux and macOS). If git answered `rev-parse --absolute-git-dir`, it is
  the entry that the tests after the add accepted (after a failed show-toplevel, the one
  new name). The rollback reads neither the `.git` file nor the `gitdir` record again. That follows
  `89cb6289`, which removes that entry when either one changed. One `os.Root.RemoveAll` of that name through a root at the
  registrations directory, after `wipesHomeDir`. The entry must still have its accepted
  identity after the open, and create holds the accepted folder open. If the open fails, the
  identity comes from a stat of the path (not measured). A folder that is not the accepted
  one stays (D24).
- **Held, or no answer of git.** A create that holds its git directory deletes the
  registration by name through the held folder (the moved entry on Windows), and none if
  `.git` is no longer that folder. Otherwise, with no answer or on Windows, the folder is
  the one that the `.git` file names. It goes only if its `gitdir` record names the leaf and
  it resolves strictly inside the registrations directory. That delete runs no
  `wipesHomeDir`. It acts on the verified resolved path, through a root with the checked
  identity on Linux and macOS, and by plain `os.RemoveAll` on Windows. With no answer of git, a registration
  whose back-pointer cannot be read is never deleted.
- **Index** (Linux and macOS). One `os.Root.Remove` of the fixed name `index` in the
  registration, never a tree, after "file exists". The registration is the accepted entry.
  With no answer of git it is the folder that the `gitdir:` path names, and it gets the
  index only if its record names the new worktree. With an answer, create refuses before
  the checkout an entry whose record cannot be read or names another path. The placement then reads no record again: a record that goes during the checkout does not
  stop it. The identity
  test of the rollback applies (D24, not measured). An entry folder whose stat fails takes
  the placement itself, which fails there.

Never widen: Never widen a delete of the [two steps that move
the hold](#two-steps-that-move-the-hold).

Detail: [git.worktree_create](docs/protocol/git-worktree-create.md#after-a-failure).

#### Path 4: the `git.worktree_create` staging sweep (Windows)

On Windows `git.worktree_create` removes old staging entries of the git directory of
`baseRepo`, which the caller names (`sweepOldStaging`, `worktreestaging.go`). Their names
start with `.claude-new-worktree-`, and the daemon did not make them. A real folder goes
as a tree, by one `os.Root.RemoveAll`. A file, a link and a junction go with one plain
`os.Root.Remove`, so the target of a junction stays.

Guards, in order: it runs in the state `shadow` only, after a good add. The path must still
name the held git directory, or the create is refused and nothing is removed or made.
Entries are read with `Lstat` through that root. The name must start with the prefix with
the dash, and the write time must be 1 hour old or more. `wipesHomeDir` refuses an entry
that is home or holds it. A failed removal logs one line, and the create goes on (not measured). `git.info` and
`git.status` remove no entry. From the code: a failed add, a
`"shadowDirOff":true` request (or one that starts while such a request runs and takes no
later gate), and the state `fallback-git` run no cleanup. A request with a later gate runs it.

Never widen: This step is approved by the maintainer. Do not widen the prefix, the kinds
or the folder of this delete.

Detail: [git.worktree_create](docs/protocol/git-worktree-create.md#differences-by-system).

#### Path 5: `-install`

The operator names `-cli-dir`, `-cli-version` and `-cli-zst`. `-install` deletes the CLI
path `<cli-dir>/<cli-version>` (`<cli-version>.exe` on Windows). A folder there goes as a
tree before the new CLI runs. A new CLI whose `--version` run does not start or exits
non-zero gets one plain remove, and a stopped run keeps it. On Windows the file can stay
when the exited CLI holds it (claustrum's own, not measured).

Guards, in order: D6 runs first, so the CLI path is a direct child of the cli-dir, but it
does not test which folder that child is. Right before the tree delete, `folderHoldsHome`
refuses a CLI path that is home or holds it. It runs `wipesHomeDir` and compares the folder at the CLI path with home and each parent of
home by identity (`os.SameFile`), for the given and the resolved home path (D2). The
prune and the sweeps skip an entry that is home or holds it (`cliEntryHoldsHome`, claustrum's
own, D2). `clearNonFolderCLIDir` removes two paths with a plain `os.Remove`, never as a
tree: a cli-dir entry that is a regular file, a symlink to a regular file or a FIFO, and
the regular file `ccd-cli-version` in its parent folder. The sweeps remove regular files
by name with a plain `os.Remove`. The `-cli-zst` blob goes with one plain `os.Remove`
once the new CLI is in place, with no guard before it ([Sweeps and
prune](docs/INSTALL.md#sweeps-and-prune)).

Never widen: Do not widen either delete of `clearNonFolderCLIDir` or build either path from
anything but the cli-dir. Do not turn a remove of the prune or the sweeps into a tree delete.

Detail: [-install](docs/INSTALL.md#staging-and-cleanup).

### `plugins.prune`

`plugins.prune` is a further `os.RemoveAll` site, and it is safe by construction. It
deletes `<socket-derived-root>/<hash>`. The root comes from the socket layout of the daemon
and the leaf must match `^[0-9a-f]{16}$` (`pluginHashRE`). The path is never
caller-supplied and never `~`-expanded, so it needs no `wipesHomeDir` guard.

### The `git-shadow` folder

The folder `git-shadow` of the settings copy is a further recursive delete site, and it is
socket-derived like `plugins.prune`. Do not build a name below it from a request value. The name below `git-shadow` is one that
this daemon made, one that a directory read of `git-shadow` or of the folder of the daemon
returned, or a fixed name in `.probe`. No part of the path comes from an RPC value or from
an operator. Each
delete is `wipesHomeDir` on the full path, then one call through a root at `<socket
folder>/git-shadow` (`gitshadow.go`): `os.Root.RemoveAll` (`removeEntry`), a plain
`os.Root.Remove` for a link that the sweep found (`removeLink`), or a plain `os.Root.Remove` that keeps a folder
with content (`removeOneEntry`). The sites are the test folder `.check-<n>`, the folder of
the daemon at shutdown (`shadowHost.close`) or after a failed lock step, a copy that this
daemon wrote whose repository file is gone, the short-lived entries of one request
(`.gitdir-<n>`, `.index-<n>`, `.worktreeinclude-<n>`, `.config-<n>`, `.s<n>`), each gone at the end of its step or at the reply (`.config-<n>`
right after the file of the registration is written, or at the reply of a create refused
before that step), the sweep
`sweepOldFolders`, and one repository folder at 128 (`evictOldRepoFolder`). [The
folders](docs/protocol/git-rules-settings-copy.md#the-folders) has the layout and the sweep
rules. A folder with no lock stays 1 minute (claustrum's own), and a link goes as a link
(D8).

In `.probe`, before each uncached call about the repository (`prepareProbeFolder`), these go
as a tree (`removeEntry`): `info/attributes`, `info/exclude`, `shallow`, `worktrees`,
`reftable`, `rebase-merge/head-name`, `rebase-apply/head-name`, `rebase-apply/applying`,
`BISECT_LOG`, `BISECT_START`, a folder at `HEAD` or `config`, and a `.tmp-<n>` file. Then
`rebase-merge` and `rebase-apply` get one plain remove each (`removeOneEntry`). Every other
entry stays.

On Linux and macOS the root removes the link `worktrees` of a repository folder and not its
target (`TestShadowFolderRemovalLeavesLinkTargets`). A new copy is a temp file plus one `os.Root.Rename` in the folder. With `"shadowDirOff":true` the
short-lived folders are `os.MkdirTemp` folders, and each goes with `os.RemoveAll`.

### The extract marker step

After the entries, `files.extract_tar` removes the fixed name `.synced` at the top of
`destDir` as a tree (`writeSyncedMarker`) and creates the empty marker file. The remove is
one `os.Root.RemoveAll` and the create is exclusive, both through a handle of `destDir`.
After the open, `extractTarGz` tests the handle against the folder that `wipesHomeDir`
judged. If it differs, the request fails before any entry and before the remove
(claustrum's own, D2). A second file entry with the name of an earlier one removes that
one regular file with a plain `os.Root.Remove`. The marker remove acts inside the first of
the five paths ([What the request
does](docs/protocol/files-extract-tar.md#what-the-request-does)).

Never widen: This step is approved by the maintainer. Do not widen it. Do not build that
name from a parameter or from the archive.

### The temp-folder test

On Linux and macOS a spawn tests the folder of an inherited `TMPDIR`, `TEMP` or
`CLAUDE_CODE_TMPDIR` (`tempfolder_unix.go`). It removes one file, `.claude-ssh-<n>`, by
name with a plain `os.Remove`, only after its own exclusive create. A failed create removes
nothing (from the code).

### The held git directory

In the state `shadow` `git.worktree_create` holds the git directory of `baseRepo` open from
its gate to its reply (`acceptedGitDir`, [The held git
directory](docs/protocol/git-worktree-create.md#the-held-git-directory)). Hold these:

- Each delete, rename, new folder and file write of the create and of its rollback inside
  the git directory goes through the held folder, never the path. The comment at the top of
  `gitdirheld.go` lists the steps.
- By the path stay: git itself, a request that holds no git directory (the off switch, the
  state `fallback-git`), the stale step for a `worktrees` folder outside the held folder
  (from the code), and on Linux and macOS the index in a `worktrees` entry that is a
  symbolic link, with the identity test of D24. After a gate under the off switch,
  claustrum takes the hold at a later gate before the first good add (claustrum's own).
- On Linux and macOS no delete of a create or a remove goes behind a `worktrees` entry that
  is a symbolic link, and a remove of an existing worktree folder is refused there.
- On Windows a directory symbolic link at `.git` is refused as a junction is, on every git method before any
  write or remove (`gitDirIsSymlink`), unless the request has `"shadowDirOff":true`. A
  request started during one gets this rule until its later gate (from the code, not
  measured).

#### Two steps that move the hold

Two steps take a new folder from the path, on Linux and macOS. After `rev-parse
--absolute-git-dir`, a path that leads to another git directory which passes the
gate becomes the held folder (`acceptedGitDir.movedTo`). After a good `worktree
add`, a new real directory at the path that passes the gate does too
(`replacedAtPath`, approved by the maintainer). Another git directory is not
measured. Add no other step that resolves the path of the git directory again,
and never widen a delete of these steps. A rollback after the first move is not
measured. After the second, the rollback acts through that new folder ([Rare
states](docs/protocol/git-worktree-create.md#rare-states)).


### What `git.worktree_create` writes

In the state `shadow` the create writes into the git directory of `baseRepo` and into the
new worktree. Only the two removes of the Windows staging step are tree deletes. Each write
goes through an `os.Root`, with a fixed name, a name that the daemon read through that root,
or a name that it made ([The settings
copy](docs/protocol/git-worktree-create.md#the-settings-copy)).

- `<git dir>/worktrees`, made before the add. On Linux and macOS a last
  `core.sharedrepository` copy line other than `umask` sets its mode by one `os.Root.Chmod`, with the set-group-ID bit,
  only on a folder that this call made. macOS is not measured.
- Linux and macOS: the `.git` file of the new worktree (`replaceWorktreeGitFile`), by one
  `os.Root.Remove` of the fixed name `.git` and one exclusive create with mode 0666 through a root at the
  leaf. The leaf must be the folder of the checkpoint, `.git` a regular file of at most 8192
  bytes, and its text `gitdir: `, the folder of the copy, `/worktrees/`, one name with no separator and a
  newline. Each other text stays as it is.
- `config.worktree` of the registration (`writeWorktreeConfig`), through the held folder
  and a root at the registration. That must be a real direct entry of `<git dir>/worktrees`
  and the accepted one by identity (on Windows the moved one). After an empty answer of git, its `gitdir` record must
  name the leaf. An entry there goes first by one plain `os.Root.Remove` (Linux and macOS,
  claustrum's own), and the create is exclusive. On Windows a folder there fails and a file
  there gets the new text. Before it the daemon runs `git config --file <its own file> --unset-all`
  on a file in `.config-<n>`.
- Windows: the staging folder `<git dir>\.claude-new-worktree-<16 hex>` and one
  `os.Root.Rename` of it into `worktrees` (`addStage.move`). The folder and its files are
  new, made with exclusive creates through the held root, and the `.git` file of the leaf
  must name the staged entry first. A failed step after its mkdir
  removes it as a tree through the held root, by the name that this call made, after
  `wipesHomeDir`. If the `.git` file cannot be replaced, the moved registration goes too,
  by its name. Its identity test against the held staging folder is claustrum's own. The index is a
  new file of the moved registration. With none it is `index.tmp-<n>` and a rename.

### Auth and token

Auth is in-band per request (`"auth":"<token>"`). The token comes from `-token-file`, which
the daemon reads once and unlinks, or from `-token-fd`. No mode reads `CLAUDE_RPC_TOKEN`,
and the daemon strips it from spawned children. `server.shutdown` is the one unauthenticated
method, for parity with the reference, and `-stop` sends no `auth` member. Every other
method rejects an unauthenticated request `-32001`
([Authentication](docs/PROTOCOL.md#authentication)).

### Token persistence

The daemon persists its token to `daemon.token` (mode `0600`) beside the socket
(`tokenpersist.go`). The fixed name and location are the reconnect contract, so they are not
configurable. On Windows `0600` is not an owner-only DACL. Do not "fix" that without an
opt-in divergence. `daemon.lock` is parity with `4534d86`, not a claustrum fix. See [`docs/DAEMON.md`](docs/DAEMON.md#token-persistence-daemontoken).

### Concurrency and panics

The requests of one connection dispatch concurrently, and replies can return out of order
as in the reference. Do not serialize them. The read loop writes parse and auth errors. The
per-request goroutine recovers from panics with `-32603 "recovered panic: <v>"`. For
`server.shutdown` it writes no frame at all. That frame is claustrum's own and NOT a parity
claim. No client can observe it, so add no golden. The tests provoke it through the
`dispatchRequest` seam ([Handler panic
recovery](docs/PROTOCOL.md#handler-panic-recovery)).

### Network

`-serve` makes no outbound network connections. Every dial of `-serve` is to a local `AF_UNIX` socket.
`-install` reaches the network only with `-cli-url`, and verifies the SHA-256 of that
download before the new CLI is put in place, unconditionally ([SECURITY.md →
Network](SECURITY.md#network)).

### Time bounds

`lddProbeTimeout` is 5 s, `cliRunBound` 30 s and `cliFirstRunBound` 120 s
([INSTALL.md](docs/INSTALL.md#the-direct---version-run)). The flags `-libc-probe-timeout`
and `-cli-probe-timeout` are deprecated no-ops that log one warning. `-cli-download-timeout`
sits beside them in the `-install` arm of `main`. A line that sets one of these bounds from
a flag compiles and passes every isolated test. `TestInstallArmWiresEachFlagToItsOwnGlobal`
is the guard.

### The limiter rule

A disabled limiter bypasses its `io.LimitReader` / `context.WithTimeout` entirely. Never
"simplify" it into a huge value. For the caps, the `cap+1` (or `max-total+1`) arithmetic
defines the boundary. For the deadlines, an unarmed cancel path makes the timeout `false` by
construction. A huge constant is a different, observable behavior. This applies to every
opt-in cap and bound in Part B.

### Time limits of the git calls

The git calls of `git.list_branches`, `git.info`, `git.status` and `git.worktree_create`
have always-on time limits (`gitstop.go`), listed in [Time limits of git
calls](docs/protocol/git-rules-calls.md#time-limits-of-git-calls). They are not the limiter
of a divergence. `git.worktree_remove` gets none, and `-git-timeout` is a no-op that logs one
warning. The branch step has its own bounds ([Time limits of the branch
step](docs/protocol/git-rules-calls.md#time-limits-of-the-branch-step)).

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
key is the reachable knob, not the flag.

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
