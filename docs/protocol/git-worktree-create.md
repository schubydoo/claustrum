# git.worktree_create

`git.worktree_create` makes a linked git worktree for a new or an existing branch.
It runs on Linux, macOS and Windows, and the checks differ by system (see [Platform differences](#platform-differences)).
The method checks the paths, runs `git worktree add --no-checkout`, checks out the files, and then copies the ignored files that the repository lists.
A client must send `branchName` even with `existingBranch`, and `worktreeRoot` is refused on Windows.
A failure after the add rolls the worktree back, and a failure frame can end with `"branchKept":true`.

This page holds the contract first. The measured cells and rows are in [Evidence](#evidence), and the history is at the [end](#history).
Other methods of the protocol are in the [protocol reference](../PROTOCOL.md).

## Request

Frame: `{baseRepo,branchName,worktreePath[,sourceBranch][,existingBranch][,worktreeRoot][,timeoutMs]}`

| Parameter | Type | Required | Meaning |
|---|---|---|---|
| `baseRepo` | string | listed as required | The repository. It is `baseRepo`, not `path`. When it is absent, the daemon uses its cwd repository. |
| `branchName` | string | required | Name of the branch to create. If it is missing, the answer is `-32602 branchName is required`. This holds with `existingBranch` too. |
| `worktreePath` | string | required | Where to put the worktree. The response and each undo text quote it exactly as sent. An empty value gives `mkdir_failed`. |
| `sourceBranch` | string | optional | Picks the start commit of the new branch. The response echoes it exactly as sent. |
| `existingBranch` | string | optional | Attaches the worktree to an existing local branch instead of creating one. It is the `git.worktree_create.existingBranch` capability. |
| `worktreeRoot` | string | optional | Places the worktree outside the repository, exactly two levels under this root. It is the `external_root` capability. |
| `timeoutMs` | number (ms) | optional | A per-request deadline over the add, the checkout and the copy step. An absent value or `0` arms no deadline. |

Rules for combinations:

- With `worktreeRoot` on Windows, the request is refused before any location test.
- With `worktreeRoot`, `baseRepo`, `worktreeRoot` and `worktreePath` must be absolute and carry no `..` component.
- Without `worktreeRoot`, `worktreePath` must sit strictly under `baseRepo`. It must be absolute, carry no `..` component and not exist.
- With `existingBranch` that names a local branch, the worktree attaches to that branch and `branch` in the response is `existingBranch`.
- With `existingBranch` empty or unknown, the new-branch path runs and `branch` is `branchName`.
- With `existingBranch` that attaches, the chosen start commit goes unused. `sourceBranch` is still echoed.
- With an absent `timeoutMs` or `0`, the response is byte-identical to the default response.

## Response

Success:

`{"success":true,"path":"<worktreePath>","sourceBranch":"<b>","branch":"<b>"}`

| Field | Meaning |
|---|---|
| `success` | `true`. |
| `path` | `worktreePath` exactly as sent. |
| `sourceBranch` | `sourceBranch` as sent, whichever start candidate was used. With no `sourceBranch` (or one that resolves to nothing), the current branch from `rev-parse --abbrev-ref HEAD`. On a detached HEAD the member is omitted. |
| `branch` | The branch that the worktree checks out. It follows `sourceBranch` on the wire. It is the created `branchName` or the attached `existingBranch`. It is present on every success. |

Failure:

`{"success":false,"error":"<text>","errorCode":"<code>"}`

When the rollback check finds commits that no other ref reaches, a failure frame ends with `"branchKept":true` after `errorCode`.
The member is only ever true, and it is absent otherwise.
A check that does not finish, and a skipped name, add no member.
No measured frame with `branchKept` has `sourceBranch` or `branch`.
claustrum puts `branchKept` after them.
The table in [The branch step](../PROTOCOL.md#the-branch-step) gives each case.

### Refusals and errors

In the texts, `<p>` and `<leaf>` are `worktreePath` in a fixed spelling (see [Step 9](#step-tests)).
`<repo>` is `baseRepo` as sent. An absent `baseRepo` gives the empty string, so a space comes before the semicolon.
The full texts of the root refusals (ancestor, repository, checkout, symlink loop) and of the other `mkdir_failed` frames are in the [error-string catalog](../PROTOCOL.md#error-string-catalogue).
A text that this page shortens with `…` is quoted here as the old section quoted it.

| Text | `errorCode` | Condition |
|---|---|---|
| `-32602 branchName is required` | none (JSON-RPC error `-32602`) | `branchName` is missing. |
| `baseRepo is inside a managed worktrees directory …`, in the frame `{success:false,error:"baseRepo is inside a managed worktrees directory …",errorCode:"nested_base_repo"}` | `nested_base_repo` | `baseRepo` sits under a managed-worktrees marker, or fails claustrum's own trust-root test. |
| `{success:false,error:<text>,errorCode:"worktree_add_failed"}` with the trust-check text | `worktree_add_failed` | The git-directory trust check refuses the git directory. |
| `git worktree add failed: cannot locate the repository's git directory: <text>` | `worktree_add_failed` | The trust check refuses and the daemon environment carries `GIT_COMMON_DIR`. |
| `not a git repository` | `not_a_repo` | The resolved repository is not git. Also "No repository" from the trust check. |
| `refusing to create worktree: <p> is a relative path; …` | `unsafe_path` | Without `worktreeRoot`, `worktreePath` is relative. |
| `refusing to create worktree: <p> contains a ".." component; …` | `unsafe_path` | Without `worktreeRoot`, `worktreePath` has a `..` component. |
| `refusing to create worktree: <p> has a component Windows reads as a different name (trailing dot or space, or a colon); …` | `unsafe_path` | Windows only. It comes before the containment check. |
| `refusing to create worktree: <p> is not inside the repository <repo>; session worktrees are only created and removed under <repository>/.claude/worktrees` | `unsafe_path` | Without `worktreeRoot`, `worktreePath` is not strictly under `baseRepo`. |
| `refusing to create worktree: <p> already exists, and a new worktree is only ever created in a fresh directory` | `unsafe_path` | Without `worktreeRoot`, `worktreePath` exists. |
| `failed to create parent directory: "" does not name a directory` | `mkdir_failed` | `worktreePath` is empty. |
| `{"success":false,"error":"failed to create parent directory: <path of the junction> is not a directory","errorCode":"mkdir_failed"}` | `mkdir_failed` | Windows. A junction sits at a directory between `baseRepo` and the leaf. |
| `refusing to {create,remove} worktree: <root> cannot be used: a custom worktree location is not supported on Windows hosts yet` | `unsafe_path` on create, none on remove | Windows. Any `worktreeRoot`. |
| `<p> is not <worktree location>/<directory>/<name> beneath <root>` | `unsafe_path` | `worktreePath` is not exactly two levels under `worktreeRoot`. |
| `{"success":false,"error":"refusing to create worktree: / is a filesystem root; choose the worktree location by its absolute path, without \"..\", beneath the filesystem root","errorCode":"unsafe_path"}` | `unsafe_path` | `worktreeRoot` is the file system root `/`. |
| `<root> is owned by uid <o>, not by you (uid <u>); …` | `unsafe_path` | The root is not owned by the daemon's user. |
| `<root> is writable by <who> (mode <perm>); … chmod go-w` | `unsafe_path` | The root is writable by its group or by every user. |
| `<dir> already exists, is not marked as a worktree directory, and holds other files (for example "<name>"); … must start out empty …` | `unsafe_path` | With `worktreeRoot`, an unmarked `<directory>` level is not empty. |
| the "already exists" text above, naming the cleaned path | `unsafe_path` | With `worktreeRoot`, the leaf exists. |
| no text is stated | `mkdir_failed` | With `worktreeRoot`, the marker cannot be created for a reason other than an existing entry. |
| `refusing to create worktree: <leaf> carries a .git file that does not name this repository's own worktree admin directory` | `unsafe_path` | After the add, test 1, 3 or 4 of the registration fails (Linux, macOS). |
| `refusing to create worktree: <leaf> was not populated by git worktree add` | `unsafe_path` | After the add, `baseRepo` has no `worktrees` directory (Linux, macOS). |
| `refusing to create worktree: <leaf> carries a .git file naming an admin entry other than the one just created for it` | `unsafe_path` | After the add, the entry is a stale entry that was left in place (Linux, macOS). |
| `refusing to create worktree: <leaf> carries a .git file naming an admin directory whose own record is of a different worktree` | `unsafe_path` | After the add, the `gitdir` record of the entry is not `<worktreePath>/.git` (Linux, macOS). |
| `git worktree add failed: <text>` | `worktree_add_failed` | The add fails with an error other than the ones above. A `timeoutMs` that expired during the add gives this frame too. |
| `git worktree add failed: <fallback text> (attaching to the existing branch <b> was refused first: <attach text>)` | `worktree_add_failed` | The attach add fails and the fallback add fails too. |
| `git worktree add failed (checkout): <text>` | `worktree_add_failed` | The `read-tree` checkout fails, or the placement of the index fails (Linux, macOS). |
| `git worktree add timed out after <n>ms (…)` | `timeout` | The deadline from `timeoutMs` fires (see [Step 10](#step-deadline)). |
| `the registration <entry> is not the folder that was tested after the add` | inside the `(checkout)` frame | Linux, macOS. The directory at the entry path is not the directory that the tests accepted. claustrum's own text. |
| `the registration <entry> has no gitdir record that names this worktree` | inside the `(checkout)` frame | Linux, macOS. The four tests did not run and the record cannot be read or names another path. claustrum's own text. |
| `<frame>; and the undo could not finish for <leaf>: …` | unchanged | A rollback step fails (see [Rollback](#rollback)). |

## What the method does, in order

1. [Read the parameters](#step-params). Check `branchName`, `baseRepo` and the Windows gate.
2. [Check the repository](#step-repo). Test the managed-worktrees marker, the trust of the git directory and the repository.
3. [Check the path without `worktreeRoot`](#step-path). Apply the containment under the repository.
4. [Check the path with `worktreeRoot`](#step-root). Apply the root, ancestor, checkout and `<directory>` tests.
5. [Create the parent directories and the marker](#step-parents).
6. [Drop a stale registration](#step-stale) (Linux and macOS).
7. [Pick the start commit](#step-source) from `sourceBranch`.
8. [Run `git worktree add`](#step-add), with the attach mode and its fallback.
9. [Test the new registration](#step-tests) (Linux and macOS).
10. [Test the deadline](#step-deadline) that `timeoutMs` sets.
11. [Check out the files and place the index](#step-checkout).
12. [Copy the ignored files](#step-copy) into the new worktree.
13. [Answer](#step-result). A failure after the add runs the [Rollback](#rollback).

The [text rule for git messages](#step-textrule) applies to every step that quotes git.

### Step 1: Read the parameters { #step-params }

- `branchName` is required. It is required with `existingBranch` too. If it is missing, the answer is `-32602 branchName is required`.
- The repository is `baseRepo`, not `path`. When `baseRepo` is absent, the daemon uses its cwd repository.
- On Windows, any `worktreeRoot` is refused before any location test. The refusal has `errorCode:"unsafe_path"` on create.
- `path` and each undo text quote `worktreePath` exactly as sent.
- A refused daemon `GIT_CONFIG_COUNT` changes the answers of this method. See [The daemon's own git environment](../PROTOCOL.md#the-daemons-own-git-environment).

Evidence: [Paths without `worktreeRoot`](#ev-path).

### Step 2: Check the repository { #step-repository }

- A `baseRepo` that sits under a managed-worktrees marker is refused with `errorCode:"nested_base_repo"`. The same frame answers a `baseRepo` that fails claustrum's own trust-root test, as in `git.worktree_remove`. No git runs and nothing is created.
- This refusal comes before the check of the daemon's `GIT_CONFIG_COUNT`.
- Then the git-directory trust check runs on `baseRepo`. It runs after the managed-worktrees test and before anything is created. See [Git-directory trust check](../PROTOCOL.md#git-directory-trust-check).
- A refused git directory answers `{success:false,error:<text>,errorCode:"worktree_add_failed"}`. "No repository" answers `not_a_repo`. Either way the daemon creates no worktree directory, no entry and no branch.
- When the daemon environment carries `GIT_COMMON_DIR`, the text is `git worktree add failed: cannot locate the repository's git directory: <text>`.
- If the resolved repository is not git, the answer is `{success:false,error:"not a git repository",errorCode:"not_a_repo"}`. The daemon tests this before the add.
- This method ignores replace objects and grafts. If `refs/replace` or `info/grafts` name others, the checkout still holds the real blob and the real commit. Ancestry is still the real ancestry.
- claustrum runs every git step of this method with `GIT_NO_REPLACE_OBJECTS=1` and `GIT_GRAFT_FILE=<null>`, except `rev-parse --absolute-git-dir`. See [Hardened git calls](../PROTOCOL.md#hardened-git-calls).

Evidence: [Paths without `worktreeRoot`](#ev-path) (replace objects and grafts).

### Step 3: Check the path without `worktreeRoot` { #step-path }

Without `worktreeRoot`, the worktree must sit inside the repository.
After the repository test, `worktreePath` must be absolute, carry no `..` component, sit strictly under `baseRepo`, and not exist.
Each failure answers `{success:false,error:"refusing to create worktree: …",errorCode:"unsafe_path"}`.
The texts are in the [error table](#refusals-and-errors).
The first two are `"<p> is a relative path; …"` and `"<p> contains a \"..\" component; …"`.

- The spelling refusal (trailing dot or space, or a colon) is Windows only. It comes before the containment check.
- The recommended location is `<repo>/.claude/worktrees/<id>`. The enforced rule is only containment in the repository.
- On Linux and macOS, the `<p>` of the "already exists" text has the symlinks of the parent directory of the leaf resolved. The last name is kept, and the path is cleaned.
- On Windows, the `<p>` of the "already exists" text has the on-disk letter case of each component that exists. claustrum keeps the volume name and any 8.3 short name as sent.
- `<repo>` is `baseRepo` as sent. An absent `baseRepo` gives the empty string, so a space comes before the semicolon.
- An empty `worktreePath` answers `{success:false,error:"failed to create parent directory: \"\" does not name a directory",errorCode:"mkdir_failed"}`.
- The daemon creates the parent directory before the add, so a nested path succeeds on a fresh repository.
- A `worktreePath` with a trailing slash, `//` or `/./` succeeds too.
- The texts of this step name the repository with its symlinks resolved.

Evidence: [Paths without `worktreeRoot`](#ev-path).

### Step 4: Check the path with `worktreeRoot` { #step-root }

`worktreeRoot` is the `external_root` capability.
When the client sends `worktreeRoot`, the worktree goes outside the repository, at `<worktreeRoot>/<directory>/<name>`.
That is exactly two levels under the root.
On Linux and macOS the tests below replace the in-repository containment of Step 3.
A refusal among them has `errorCode:"unsafe_path"`, except the `mkdir_failed` cases that the [catalog](../PROTOCOL.md#error-string-catalogue) lists.
On Windows this capability is gated off (see Step 1).

The tests run in this order.

1. `worktreeRoot` and `worktreePath` must be absolute and `..`-free.
2. `baseRepo` must be absolute and carry no `..` component. The refusal uses the `worktreePath` texts and names `baseRepo` as sent. An absent `baseRepo` is a relative path with the empty string as its name. claustrum runs this test after the `worktreePath` spelling and before the two-level test, as on remove.
3. `worktreePath` must sit exactly two levels under the root. The text is `"<p> is not <worktree location>/<directory>/<name> beneath <root>"`.
4. A `worktreeRoot` of `/` is refused. The frame comes after the excludes read and the repository test, and nothing is created.
5. Test A of the checkout tests: the daemon compares the cleaned root with the cleaned `baseRepo` by whole components.
6. The daemon makes the git calls that [Hardened git calls](../PROTOCOL.md#hardened-git-calls) lists.
7. The ancestor test (see below).
8. Test B of the checkout tests: the root must not lead into the git top level of `baseRepo` or into the main checkout.
9. Test C of the checkout tests: the root must not lie in a linked worktree of the repository.
10. The daemon resolves the symlinks of the root. It tests each directory from `/` down to the root for a `.git` entry, top first.
11. The tests of the root's owner and write access.
12. The test that the `<directory>` level is not a symlink.
13. The `<directory>`-level tests, then the non-empty test, then the test that the leaf does not exist.

The checkout tests A, B and C come after the two-level test, as on remove. Nothing is created by them or by the root-chain tests.
A root that holds the repository passes. So does `<B>/Tx` beside `<B>/T`.
claustrum runs them in this order. The order of the checkout tests on create is not measured (see [Not measured](#not-measured)).

#### Ancestor test

The ancestor test judges the directories above the root.
A directory owned by a user other than the daemon's user or uid 0 refuses the create.
A directory with the other-write bit refuses it, unless the sticky bit is on.
A directory with the group-write bit and a shared group refuses it, unless the sticky bit is on.

#### Owner and write access of the root

- The root must be owned by the daemon's user (`"<root> is owned by uid <o>, not by you (uid <u>); …"`).
- The root must not be writable by its group or by every user on the host (`"<root> is writable by <who> (mode <perm>); … chmod go-w"`).
- The group-write bit counts unless the group is the private group of the daemon's user.
- A group that counts as shared on a root with mode 0777 gives the `<who>` text "its group and every user on this host".
- For this test the answers follow only the flat files `/etc/passwd` and `/etc/group`.
- Members and primary-group users in the macOS directory service do not count. A directory-service group alone is not enough.
- A stock macOS user has no `/etc/passwd` line, so there every group-writable root is refused.

A private group passes four tests:

- Its gid is the daemon's gid.
- `/etc/passwd` has exactly one line with that gid as its primary gid. That line must be the daemon user's line.
- At least one `/etc/group` line has that gid, and every such line has the name of that user.
- No such line lists a member other than that user.

The account-file lines follow these rules:

- In both files, a line that starts with `#` is skipped.
- A `/etc/passwd` line with 6, 7 or 8 fields is read.
- A passwd name with a leading space does not match the user (Linux only).
- A `+name` line is not skipped.
- A leading space and a trailing CR around a group member are trimmed, and an empty member is ignored.

#### The `<directory>` level

- The `<directory>` level must not be a symlink.
- Unless the `<directory>` level is already marked, it must start out empty. The refusal text is `"<dir> already exists, is not marked as a worktree directory, and holds other files (for example \"<name>\"); … must start out empty …"`.
- These two tests take `<directory>` from the cleaned `worktreePath`. For `R/proj/w1/` the refusal names `R/proj`.
- With `worktreeRoot`, the "already exists" refusal also quotes the cleaned path. For `R/cp/w1/` it quotes `R/cp/w1`.
- With a `worktreeRoot` behind a symlink, claustrum names both paths with the link resolved.
- A `.git` entry or a symlink loop refuses the create with `errorCode:"unsafe_path"`.
- The root-chain tests come before the tests of the root's owner and write access.
- The `<directory>` file test comes after the writable-root and foreign-owner tests. The search test comes after the writable-root test.
- The symlinked `<directory>` test comes before the `<directory>`-level tests.
- The spelling rule of these paths is in [Step 9](#step-tests).

Evidence: [Root tests](#ev-root), [Group and account files](#ev-account).

### Step 5: Create the parent directories and the marker { #step-parents }

- On Linux and macOS, without `worktreeRoot`, a directory that the create makes above the leaf asks for mode 0755. The leaf asks for 0777. The umask applies. An existing directory keeps its mode.
- A directory that the call made before a later failure stays.
- With `worktreeRoot`, each directory that the create makes above the leaf asks for mode 0700. That covers every missing directory from the highest one down to `<directory>`.
- If no marker exists, the daemon writes a 285-byte `.claude-managed-worktrees` marker at the `<directory>` level. This happens after the parent step and before the add.
- An existing entry of that name keeps its content and its mode.
- A marker that cannot be created for another reason stops the create with `errorCode:"mkdir_failed"`. The leaf is not made.
- A failed add keeps the marker.
- On Windows, a junction at a directory between `baseRepo` and the leaf fails the parent step with `{"success":false,"error":"failed to create parent directory: <path of the junction> is not a directory","errorCode":"mkdir_failed"}`. Nothing is created.
- See [`DIVERGENCES.md`](../DIVERGENCES.md#d19) → D19.

Evidence: [Parents, marker and junctions](#ev-parents).

### Step 6: Drop a stale registration { #step-stale }

Before the add, on Linux and macOS, the daemon looks for stale entries in `<baseRepo>/.git/worktrees`.
This step runs after the test that `worktreePath` does not exist.
If its `gitdir` record names the `.git` of the new worktree, an entry is stale.
The daemon removes a stale entry in one case. It must be the only stale entry of the directory, and it must hold no `locked` file.
Below, `<L>` is the real path of `worktreePath`.

How the record is compared (claustrum's own fit of the cells):

- The record loses blanks and newlines at both ends.
- A relative record counts from the entry directory.
- The path is then cleaned, and no symlink of it is resolved.
- The other side is `worktreePath` with the symlinks of its existing part resolved, plus `/.git`.

When the entry goes and when it stays:

| Case | Result |
|---|---|
| One stale entry, record `<L>/.git` with or without a newline | The entry is removed. |
| One stale entry, record `<L>/.git/`, `<L>/.git//`, `<L>/.git/.`, or `<L>/.git` with a blank and a newline | The entry is removed. |
| One stale entry, record the relative path `../../../.claude/worktrees/w1/.git` | The entry is removed. |
| The name of the entry (for example `old9`) | It does not count. |
| Two or three stale entries | None is removed. |
| One stale entry and a second stale entry that holds a `locked` file | Both stay. |
| One stale entry beside an entry that is not stale | The stale entry goes. |
| One stale entry beside a regular file, a directory with no `gitdir` record, or an empty directory | The stale entry goes. |
| Record `<L>` or `<L>/` (no `.git` part) | The entry stays. |
| An entry of a live worktree at another path | The entry stays. |
| A record that spells the path through a symlink, request with the real path | The entry stays. |
| A record with the real path, request through a symlink | The entry goes. |
| An entry that holds a `locked` file (also an empty file) | The entry stays. |

More rules:

- The remove is best effort. No error of it reaches the frame.
- With the entry at mode 0500, only `logs/HEAD` goes. With the `worktrees` directory at mode 0555, the files go and the empty directory stays.
- With no daemon `GIT_*` variable, the add then decides. After a removed entry the create succeeds with one entry `w1`.
- After a kept entry, git names the new registration `w11` and the create succeeds, or the add fails with the text of git.
- Three guards are claustrum's own. The remove is one `os.Root.RemoveAll` of the entry name, through a root at the `worktrees` directory.
- The step lists the entries and reads each record through that same root. An entry that is not a real directory is passed over.
- The home guard (D2) runs on the entry path first.
- claustrum remembers each stale entry that stays, for the stale entry test in [Step 9](#step-tests).
- On Windows, claustrum keeps its earlier step. It compares the directory that holds the record path with `worktreePath`, both with their symlinks resolved. It has no `locked` test and no root.

Evidence: [Stale registration step](#ev-stale).

### Step 7: Pick the start commit { #step-source }

A non-empty `sourceBranch` picks the start commit of the new branch.
In this step, `s` is the value as sent.

1. The daemon resolves two candidates. L is `refs/heads/<s>^{commit}`. R is `refs/remotes/origin/<s>^{commit}`.
2. If only one candidate resolves, that one wins.
3. If both resolve and `git merge-base --is-ancestor <L> <R>` exits 0 (L equals R or is behind it), R wins.
4. Otherwise the daemon runs `git merge-base <R> <L>`. If it fails (no common history, a shallow cut, a missing parent commit), R wins.
5. Otherwise the daemon runs `git diff --quiet --no-ext-diff --no-textconv --submodule=short <merge base> <L> -- ':(top,icase).claude' ':(top,icase).mcp.json'`. Exit 0 selects L. Any other exit selects R.
6. If neither candidate resolves, the result is the same as for an omitted `sourceBranch`.

How the candidates are read:

- Each candidate is plain string concatenation. A revision suffix such as `feat~1` works, and a slash name such as `team/feat` works.
- A symbolic ref is followed, so `s = "HEAD"` reads `refs/remotes/origin/HEAD`.
- An annotated tag object in the origin ref is peeled to its commit.
- An origin ref that holds a missing object, a tree or garbage counts as absent. So does a local ref that holds a missing object.
- `s` is tried only under `refs/heads/` and `refs/remotes/origin/`.
- Only the remote-tracking namespace `origin` is read. The configuration of the remote does not matter.
- The daemon fetches nothing, so a stale tracking ref is used as it is.
- On a case-insensitive file system, a loose ref matches `sourceBranch` in any letter case, and a packed ref does not (Windows, macOS). On macOS a loose ref under `Origin` also counts.

What the diff test means:

- A local change to the repository-root `.claude` entry or the root `.mcp.json`, in any letter case, selects R. So does a diff that errors.
- The test is the net tree difference from the merge base. It is not the commit history, and it is not a comparison with R.
- A nested `sub/.claude`, a `.claude.json` or a `.claudex` directory does not count.
- Uncommitted state in `baseRepo` does not count.

What the add gets:

- The add gets the full id of the chosen commit: `worktree add --no-track --no-checkout -b <branchName> <path> <sha>`.
- The checkout reads the same id. The reflog of the new branch reads `branch: Created from <sha>`. The branch gets no upstream configuration.
- `sourceBranch` is echoed exactly as sent, whichever candidate was used.
- `existingBranch` is resolved after these steps (see [Step 8](#step-add)).
- These git steps have no deadline of their own. With `-git-timeout` (D5) opted in, a killed step counts as a failed step.

When `sourceBranch` is omitted or `""`:

- Origin is not read. A non-empty `sourceBranch` that resolves to nothing reads both candidates first.
- In each of these cases the add gets no start point, so the new branch starts at HEAD. Its reflog reads `branch: Created from HEAD`.
- The daemon echoes the current branch from `rev-parse --abbrev-ref HEAD`. On a detached HEAD the result omits `sourceBranch`.
- On an unborn HEAD, the add fails with `worktree_add_failed`.

Evidence: [Start commit](#ev-source).

### Step 8: Run `git worktree add` { #step-add }

New-branch mode runs `git worktree add --no-track --no-checkout -b <branchName> <path> [<sha>]`. The text of git's failure goes through the [text rule](#step-textrule).

Attach mode:

- When `show-ref --verify refs/heads/<existingBranch>` resolves, the add uses that branch as the commit-ish: `worktree add --no-checkout <path> <existingBranch>`, with no `-b`. `branch` is `<existingBranch>`.
- When `existingBranch` is empty or names no branch, the `-b <branchName>` new-branch path runs and `branch` is `<branchName>`. A miss falls back silently rather than erroring.
- If the attach add fails, the daemon runs `worktree add --no-track --no-checkout -b <branchName> <path> [<sha>]` and goes on.
- The fallback add gets the start commit that `sourceBranch` resolved to, as the new-branch path does. The checkout then reads that commit.
- With no start commit, the add gets no start point, and the checkout reads `refs/heads/<branchName>`.
- After the fallback, `branch` is `<branchName>`. A rollback after this fallback runs the branch step on `<branchName>`, because the call created it.
- The natural trigger is an `existingBranch` that is checked out in `baseRepo`.
- With `existingBranch:"main"` and no `sourceBranch`, the reply is `{"success":true,"path":"<p>","sourceBranch":"main","branch":"<branchName>"}`.
- If the fallback add also fails, the reply is `{success:false,error:"git worktree add failed: <fallback text> (attaching to the existing branch <b> was refused first: <attach text>)",errorCode:"worktree_add_failed"}`.
- claustrum passes the full id of the start commit.

No fallback:

- When the failed attach add deleted the leaf or put a new directory in its place, no fallback runs. The daemon tests the identity of the leaf for that.
- The reply is then `git worktree add failed: <attach text>`, and the rollback of a failed add runs.
- claustrum holds the leaf and its parent open until it answers, so a replacement cannot reuse the inode of the leaf.
- On Windows both claustrum handles share delete.
- claustrum takes the identity of the leaf from its handle.

When the add fails:

- Any other failure answers `{success:false,error:"git worktree add failed: <text>",errorCode:"worktree_add_failed"}`.
- `<text>` is git's stderr, made by the [text rule](#step-textrule).
- The text can start with git's graft-file deprecation `hint:` lines, because claustrum sets `GIT_GRAFT_FILE`. When git reads that file, it prints the hint.
- An example text is `"git worktree add failed: Preparing worktree (new branch 'dup') fatal: a branch named 'dup' already exists"`.
- If the caller `timeoutMs` expired during the add, a failed add still answers this frame.
- After a failed add, the daemon runs no git call. It removes the leaf in one case: the leaf is an empty directory.
- Files that the failed add left in the leaf stay. So do a registration and a branch that it made.
- A retry at the same path then answers `unsafe_path` "already exists". The common failures, such as a branch that already exists, leave the leaf empty, so a retry with a fresh branch succeeds.
- If the failed add replaced the parent of the leaf and made a new empty leaf in it, the new leaf stays. The daemon tests the identity of the parent that it holds open for that.

Evidence: [Attach, fallback and failed add](#ev-add).

### Step 9: Test the new registration { #step-tests }

Right after a successful add, the daemon reads the `.git` file of the new worktree.
Git writes one line there, `gitdir: <path>`.
On Linux and macOS claustrum then runs four tests, the stale entry test and the record test, in this order.
Each refusal is `unsafe_path`.

1. The directory that holds `<path>` has the name `worktrees`. If it has another name, the answer is `{"success":false,"error":"refusing to create worktree: <leaf> carries a .git file that does not name this repository's own worktree admin directory","errorCode":"unsafe_path"}`. Call this the text of test 1.
2. The registrations directory of `baseRepo` exists, and it holds an entry with the last name of `<path>`.
   - That directory is `<common git dir>/worktrees` of the git directory that `rev-parse --absolute-git-dir` answered before the add.
   - If the directory is absent, the answer is `{"success":false,"error":"refusing to create worktree: <leaf> was not populated by git worktree add","errorCode":"unsafe_path"}`.
   - If the directory is present and has no entry of the name, the answer is the text of test 1.
3. The `commondir` file of that entry leads back to the common git directory. claustrum joins a relative value to the entry path as spelled. If the result is another directory, the answer is the text of test 1. A `commondir` file that cannot be read as a file gets that text too.
4. The `gitdir` record of that entry can be read as a file. If it cannot, the answer is the text of test 1.

After each refusal, no checkout runs and nothing is rolled back.
The leaf holds its `.git` file only, the registration stays with no `index`, and the branch stays.
The tests come before the deadline test that follows the add.

The result of the tests depends on the layout of the git directory and on the git version that wrote the `.git` file.
The [registration tests table](#ev-tests) gives the result for each layout.
claustrum runs the same tests in every layout (attach mode, with `worktreeRoot` and plain).
A `.git` file that names a path that does not exist, with the registration present, gives success.
The `.git` file stays as it is, and the index goes into the entry of test 2, not into `<path>`.
With a daemon `GIT_COMMON_DIR` of another repository X and `GIT_DIR` of X too, the create succeeds and git makes the registration in X.
From the code: git answers X as the git directory there, and claustrum finds the entry in X.

Without an answer to `rev-parse --absolute-git-dir`:

- From the code: the four tests need the git directory that git answered. If `rev-parse --absolute-git-dir` gave no answer before the add, claustrum runs none of the four tests and puts the index into `<path>`.
- With a `baseRepo` that is a subdirectory of a repository, the read-tree then fails, and the create answers the failed checkout of [Step 11](#step-checkout). With an answer, a subdirectory creates.

On Windows claustrum runs none of the four tests and puts the index into `<path>`.

#### The stale entry test

After the four tests, on Linux and macOS, the daemon tests the entry of test 2 against the step before the add.
If that step left stale entries in place and the entry of test 2 is one of them, the answer is `{"success":false,"error":"refusing to create worktree: <leaf> carries a .git file naming an admin entry other than the one just created for it","errorCode":"unsafe_path"}`.
It runs no checkout and rolls nothing back.

- The rule "an entry that the step before the add left" is claustrum's own fit of the cells.
- With two or more stale entries every one stays. The answer is this text where the entry of test 2 is one of them.
- A record of another worktree in an old entry does not give this text. It gets the text of the record test.
- claustrum runs this test before the pair of the record test.
- From the code: claustrum runs it before the deadline test.

#### The record test

After the stale entry test, and before the deadline test, the daemon reads the `gitdir` record of the registration of the new worktree.
On Linux and macOS that is the entry of test 2, not the directory that the `.git` file names.
The daemon compares the record with `<worktreePath>/.git` byte for byte, with `worktreePath` taken after symlink resolution.
If they differ, the answer is `{"success":false,"error":"refusing to create worktree: <leaf> carries a .git file naming an admin directory whose own record is of a different worktree","errorCode":"unsafe_path"}`.
It runs no checkout and rolls nothing back. The leaf, the entry and the branch stay.

- Before the answer, the daemon runs the `rev-parse --show-toplevel` pair with `--git-dir` and `--work-tree=<baseRepo>` in the git directory. claustrum runs it once, with `baseRepo` as sent.
- A record that cannot be read as a file gets the text of test 1. A record that was read and names anything else gets the text of the record test.
- A relative record counts from the entry. Git writes one with `worktree.useRelativePaths`.
- On macOS, git records the path in NFC. An NFC directory sent in NFD is refused. An NFD directory sent in NFC succeeds.
- A path sent in another letter case is refused. A path sent in an NFD spelling is refused.
- The symlink resolution is claustrum's choice. A `/tmp` path creates as usual.
- The check is off for a relative `worktreePath`, and on Windows. That is claustrum's choice.
- A record `<worktreePath>/.git/` with a slash at its end and no newline is removed by the step before the add (see [Step 6](#step-stale)). The create then answers the text of test 1.

The order of the tests is: test 1, test 2, test 3, test 4, the stale entry test, the record test, then the deadline test.

#### How the paths are spelled in these texts

In the texts of the four tests, the stale entry test and the record test, `<leaf>` is `worktreePath` in this spelling.
The parent directory of the leaf has its symlinks resolved, the last name is kept, and the path is cleaned.

- The "already exists" refusal before the add names the leaf in the same way, with and without `worktreeRoot`.
- With `worktreeRoot`, the "is not marked as a worktree directory" refusal names the directory that holds the leaf in that way too.
- These texts name the leaf as sent: the undo clause of a rollback, the `path` of a success, and the locked refusal of `git.worktree_remove`.
- The text names the leaf, not the target of the link. This holds for a symlink to a directory, a dangling symlink, a regular file and a symlink to a file.
- A parent directory that does not resolve gives the cleaned path as sent.
- On Windows the texts keep their earlier spelling.

Evidence: [Registration tests](#ev-tests), [Stale entry test and record test](#ev-record), [Spelling of paths](#ev-spelling).

### Step 10: Test the deadline { #step-deadline }

`timeoutMs` is caller-supplied.
It is a per-request deadline in milliseconds over the add, the checkout and the copy step.
An absent `timeoutMs`, or `0`, arms no deadline, so the reply is byte-identical to the default.
A fired deadline answers `{success:false,error:"git worktree add timed out after <n>ms (…)",errorCode:"timeout"}`.

- The deadline does not kill `git worktree add`. The daemon waits for the add to exit. The reply thus waits for the add.
- If the add failed, the reply is the add-failure frame, not `timeout`.
- If the add succeeded and the deadline expired, the parenthetical is `deadline expired before the checkout started`.
- In attach mode the daemon runs the fallback add first, and then tests the deadline.
- On Linux and macOS the four registration tests and the record test come before this deadline test.
- The deadline kills the checkout, a `read-tree`. The frame is then `…(deadline expired during the checkout): <text>`. The text rule makes `<text>` from the stderr of the killed git.
- Git can print graft-file `hint:` lines on stderr before the kill, and `<text>` then holds them.
- The deadline does not kill the copy step that seeds the new worktree. The daemon lets the step finish and then tests the deadline. If it expired, the parenthetical is `deadline expired after the checkout finished`. The reply thus waits for the copy step.
- The checkout git can exit 0 while a descendant holds one of the output pipes of the daemon. Such a descendant is a smudge or hook filter. The daemon caps that drain at a fixed ~5s from the exit of git, independent of `timeoutMs`.
- At the cap the daemon reaps the descendant. The checkout then counts as finished, so the copy step runs and the deadline test after it decides.
- When `timeoutMs` exceeds the drain, the reply is `{success:true}`. When it does not, the reply is the `timeout` frame with `deadline expired after the checkout finished`.
- These timeouts roll back as a failed checkout does (see [Rollback](#rollback)). No rollback runs `git worktree remove`, so the `.git/worktrees/` directory stays.
- A retry at the same path then succeeds with a new `branchName`. A retry with the same `branchName` succeeds in one case: the rollback deleted the branch.
- This deadline is caller-activated. It is distinct from the operator-global `-git-timeout` divergence (D5), and it applies to create only, not to `git.worktree_remove`.

Evidence: [Deadline](#ev-deadline).

### Step 11: Check out the files and place the index { #step-checkout }

The checkout is `read-tree -u --reset --no-recurse-submodules <rev>`, after the hardening `-c` options and `-c core.splitIndex=false -c core.commitGraph=false`.

- It runs with the new worktree as its working directory, and it passes no `-C`.
- `--git-dir` names the git directory of `baseRepo`, and `--work-tree` names the new worktree.
- For a linked-worktree `baseRepo`, the git directory is that worktree's own admin directory. The daemon gets it with `rev-parse --absolute-git-dir` before the add.
- The index goes to a file in a new temporary directory, named by `GIT_INDEX_FILE`. The directory has the prefix `claude-ssh-index-` (see Temporary names in [Hardened git calls](../PROTOCOL.md#hardened-git-calls)).
- Its configuration precursor, `--git-dir=<git dir> config -z --list`, runs in the new worktree too.
- On Linux and macOS, `--work-tree` names the new worktree with its symlinks resolved. On Windows claustrum passes the path as sent.

A failed checkout answers `{success:false,error:"git worktree add failed (checkout): <text>",errorCode:"worktree_add_failed"}`.
The [text rule](#step-textrule) makes `<text>`.
Where git prints graft-file deprecation `hint:` lines, the text starts with them.
The [Rollback](#rollback) runs.

A process that the checkout leaves behind starts in the new worktree.
On Windows it then blocks the removal of the leaf, and the rollback reports it with the undo text.

#### Placing the index

When git exits 0, the daemon puts that index in the registration of the new worktree. The file is `index`. The daemon removes the temporary directory.
On Linux and macOS the registration is the entry of test 2.
The index goes into the entry of test 2 with no second read of the record.
claustrum makes a new file there and copies the bytes.

The new file has these facts:

- It is a new inode, not the temporary file.
- Its group is the group that a new file gets in the registration directory. On macOS that is the group of the directory. On Linux it is the group of a setgid directory, and else the primary group.
- Its mode is 0666 less the umask of the daemon: 0644 for umask 0022, and 0600 and 0664 for the umasks 0077 and 0002. It is 0644 with `core.sharedRepository group` too, and with the temporary directory on another file system.
- Its mtime is the mtime of the temporary index, rounded up to a whole microsecond.
- claustrum leaves the atime alone and keeps a whole microsecond.

An entry that exists already at the index is replaced.
A file of mode 0600 gives a new inode of mode 0644.
An empty directory there is replaced by the index.
A symlink there goes, its target stays, and the index is a new file.

claustrum takes this order:

1. It creates the file exclusively. Any error but "file exists" is the answer.
2. On "file exists" it removes the entry with one plain remove.
3. It creates the file exclusively again.

An entry that appears at the index between the remove and the second create fails the placement with `openat <registration name>/index: file exists`.
A failed set of the mtime, after the index is written, fails the placement with `chtimesat <registration name>/index: <OS error>`.

On Windows claustrum moves the temporary file.
If that move fails, the worktree has no index and the create still succeeds.

#### A failed placement

On Linux and macOS, a placement of the index that fails is a failed checkout.
The frame is `{success:false,error:"git worktree add failed (checkout): <text>",errorCode:"worktree_add_failed"}`, and the rollback runs.

| State | OS error in `<text>` | Leaf, registration, branch after the frame |
|---|---|---|
| The registration directory loses its write bit during the checkout | `openat w1/index: permission denied` | The leaf is gone. The registration and the branch stay. The step 2 undo text follows. |
| The temporary index is gone at the exit of git with status 0 | `open <temporary directory>/index: no such file or directory` | The leaf, the registration and the branch go. In attach mode the frame is the same. |
| A file exists at the index, and the registration directory has no write bit | `removeat w1/index: permission denied` | The step 2 undo text follows. |
| A directory that holds one file exists at the index | `removeat w1/index: directory not empty` | The rollback runs. |
| The registration is gone at the install | `openat w1/index: no such file or directory` | The whole rollback runs. |
| The registrations directory has mode 0600 after the checkout, with or without a file at the index | `openat w1/index: permission denied` | The step 2 undo text follows. The leaf goes. The registration and the branch stay. |

`w1/index` is the registration name and the file, relative to the registrations directory.

Two states place nothing, and claustrum answers the failed checkout with its own text:

- The directory at the path of the entry is not the directory that the tests accepted. claustrum tests that through the registrations directory that the placement opened. The text is `the registration <entry> is not the folder that was tested after the add`. This is divergence D24.
- With no answer to `rev-parse --absolute-git-dir`, the four tests do not run. claustrum then reads the record right before it places the index, in the directory that the `.git` file names. It places the index in one case: the record can be read and names the new worktree.
- In any other state of a directory that is there, it places nothing and the `index` there keeps its bytes. The text is `the registration <entry> has no gitdir record that names this worktree`. Those states are a record that is missing, a FIFO or a directory, an empty record, and a record of another path, relative or not.
- One state is apart in both forms: an entry directory whose stat fails. The placement then runs and fails by itself, with the texts of the table above.
- In both texts of claustrum, `<entry>` is the path of the entry directory, not `worktreePath`.
- A placement that fails after a drain overrun is answered as the failed placement. So is a placement that fails after the caller `timeoutMs` expired.

How `<text>` is built:

- `<text>` is the stderr of the checkout and the OS error, joined with nothing between them.
- The text rule then applies to the joined text, so the 512-byte cut covers the OS error too. A long stderr cuts the claustrum text or drops it.
- A stderr that ends with one newline gives one space before the OS error. With no final newline there is no space. With two final newlines there are two spaces.
- With no stderr the text is the OS error alone.
- A stderr of 478 bytes keeps the whole OS error. A stderr of 500 bytes keeps its first 12 bytes, ` openat w1/in`. A stderr of 512 bytes or more keeps none of it.

Evidence: [Checkout](#ev-checkout), [Index file](#ev-index), [Failed placement](#ev-placefail).

### Step 12: Copy the ignored files { #step-copy }

`git worktree add` checks out tracked files only, so the daemon then seeds the new worktree.
The copies are best-effort, and a failure never fails the request.
A caller `timeoutMs` that expires before the copies end still fails it (see [Step 10](#step-deadline)).
Two passes copy: the manifest pass and the `.claude/` pass.

#### The manifest pass

- `.worktreeinclude` sits at the repository root and uses `.gitignore` syntax. It is an include filter over the git-ignored set.
- The daemon copies an untracked file in one case: the manifest names it and git's standard rules ignore it. A manifest match that git does not ignore is not copied.
- The manifest must be a regular file. A symlink or a directory copies nothing and runs no git.
- An empty regular file still runs `git version` and the scan, and it copies nothing.
- Git reads a temporary copy of the manifest bytes.

Choice of scan:

- If the manifest is a regular file, the daemon runs `git version`. The call has no `-c` option and no `-C`. It runs in the working directory of the daemon, with the environment of the daemon unchanged.
- The first `git version ` in the output counts, even after other text. A digit must follow it.
- Git 2.32.0 or later gets the directory scan. Older git gets the full scan, and so does output that does not parse.
- A non-zero exit also gets the full scan, even with valid output.
- Major and minor compare as numbers. A number too large for an int counts as very large.
- Text after the numbers is ignored, so `2.32.0.windows.1` gets the directory scan.

The full scan:

- It runs `git ls-files --others --ignored --exclude-from=<manifest copy> -z -- ':(exclude).claude/worktrees'`.
- `git check-ignore --stdin -z` then keeps the paths that git's standard rules ignore.
- A runtime-state path (see below) or a nested repository does not go to check-ignore.
- If every path is dropped, no check-ignore call runs.
- If either call fails, nothing is copied.
- The full scan searches every ignored directory.

The directory scan:

- It first lists the ignored entries with `git ls-files --others --ignored --exclude-standard --directory`. If the listing fails, nothing is copied.
- Every ignored file in the listing is a candidate.
- The daemon searches an ignored directory in one case: the manifest opens it.
- An any-depth pattern thus does not reach a file in a closed directory. When git ignores `build/`, `*.txt` does not copy `build/a.txt`.
- If the ignored files of the listing total more than 1 MiB, the full scan runs instead. Each file counts as its path length plus 3 bytes.

When a manifest pattern opens a directory:

- A literal name of one segment opens each listed directory that has the name as any of its segments. `build` opens `build`, `sub/build` and `build/x`. So does `**/` and then one literal. Case does not matter.
- One segment after a leading `/` opens each listed directory whose first segment matches it as a glob. `/build/` opens `build`, not `sub/build`. `/a?b/` opens `aXb` and `a_b`, not `ab`. The prefix rule does not apply here. A `\` escapes the next character, so `/ab\q/` opens nothing. Case does not matter.
- One glob segment without a leading `/` opens by its literal prefix. A glob segment holds `*`, `?`, `[` or `\`. The prefix ends before the first of these characters. The segment opens each listed directory whose path starts with the prefix. The rest of the segment is not used. Case does not matter.
- So `b*` opens `build`, not `sub/build`. `su*` opens `sub/build`. `a?b/` and `a[_]b/` open `ab`, `a b` and `a/c`. `sub\build/` opens `sub`, `sub/build` and `subbuild`. `AB\q/` opens `ab`.
- `**/` and then a literal and more segments matches the rest of the pattern from any segment of a listed directory. `**/sub/build/` opens `sub/build`.
- A pattern of two or more segments opens each listed directory that it matches segment by segment, and the listed parents of that directory.
- In such a pattern, a segment that ends in a lone `\` matches any name. So `sub\/x/` opens each listed directory of one segment and each listed `<name>/x`. Git reads that line as `sub/x/`. This holds for the first, a middle and the last segment.
- An even run of `\` at the end of a segment is a literal `\`, so `zz\\/x/` opens only `zz\/x`. A run of three acts like a run of one. Directly after `**/`, such a segment opens nothing.
- A `//` in a pattern leaves an empty segment. `build//`, `build//a.txt` and `BUILD//` open `build`, not `sub/build` or `a/x/build`. `build//` alone opens no dot directory.
- The empty segment matches no name, so `//x` opens nothing and `a//b` opens only a listed `a`.
- One segment without a leading `/` that starts with `*`, `?`, `[` or `\` has an empty prefix. It opens no directory. Neither does `**/` and then a glob. A negation and a comment open nothing too.
- Git's own match still reads a `\` as an escape. The manifest goes to git unchanged. So `a\ b/` opens `ab`, but git copies from `ab` only the files that another manifest line matches. Git does not read `\` as a separator.
- In a pattern of two or more segments, a `\` does not cut a prefix. `sub/b\q/` opens only `sub`.

Limits of the opening rules:

- Only the first 256 counted patterns can open a directory. A negation counts. Blank lines, comments and patterns over a cap do not count.
- A line of only tabs and spaces is blank. So is a line that is empty after one leading and one trailing `/` are removed, such as `//`.
- If a pattern has more than 1024 bytes after one leading and one trailing `/` are removed, it opens nothing. If it has more than 32 segments, it opens nothing too.

Dot directories:

- An any-depth pattern also opens the listed dot directories. A pattern of one segment is any-depth, unless it starts with `/`. A pattern that starts with `**/` is any-depth too.
- The 256 count does not apply to this. At most 128 dot directories open, in listing order.
- The root `.claude/` takes a place unless an explicit pattern matches it.
- The flag never opens these 14 names, in any case. They take no place.
- The first seven are `.angular`, `.cache`, `.dart_tool`, `.gradle`, `.next`, `.nuxt` and `.parcel-cache`.
- The last seven are `.pnpm-store`, `.svelte-kit`, `.terraform`, `.tox`, `.turbo`, `.venv` and `.yarn`.
- An explicit pattern still opens a skipped directory, or one past the 128th. A dot directory that an explicit pattern matches takes no place.
- If a pattern names `.claude/worktrees/`, that directory still does not open.
- The root `.claude/` opens by the rules above. It does not become one pathspec. Its children take its place in the directory batch, in the order of the directory read. `worktrees` is left out in any case. This is the list of the `.claude/` pass below. So a root `.claude/` that holds only `worktrees` adds no pathspec.

Batches of the directory scan:

- The candidates go to `git ls-files --exclude-from=<manifest copy>` in batches. Files and directories never share a batch.
- A directory pathspec has no trailing `/`.
- A batch fills in listing order. When the next path does not fit, a new batch starts.
- Each argument after the `-c` options costs its length plus 3 bytes. That covers the fixed `ls-files` arguments, the `--exclude-from` argument and the paths.
- One call costs at most 131 072 bytes on Linux and macOS, and at most 24 576 bytes on Windows. The `.claude/` pass below uses the same budget.
- The `-c` options do not count in claustrum.
- The temporary file name starts with a 27-byte prefix. A random decimal suffix follows it.
- A failed batch is skipped, and the other batches are still copied.
- Only the paths from the directory batches go to `git check-ignore --stdin -z`. It keeps the paths that git's standard rules ignore. The file candidates are copied without it.
- If it fails, the directory paths are dropped, and the file candidates are still copied.
- Three kinds of directory path do not go to check-ignore: a path that the file batches also print, a nested repository (which ends in `/`), and a Claude runtime-state path. If no path is left, no check-ignore call runs.
- A nested repository inside an ignored directory is not copied, and no empty directory is left for it.

#### The `.claude/` pass

- `.claude/` is copied separately, with no manifest entry.
- A second pass runs `git --literal-pathspecs ls-files --others --ignored --exclude-standard -z --` with one pathspec for each child of `.claude/`, such as `.claude/settings.json`.
- It leaves out `worktrees` in any case. If no other child exists, the pass runs no git.
- The pathspecs go into batches in the order of the directory read, one git call for each batch.
- The budget is the budget of the directory batches above. The fixed arguments cost 87 bytes. So the pathspecs of one call cost at most 130 985 bytes on Linux and macOS, and at most 24 489 bytes on Windows.
- Before each batch, the daemon runs `git config -z --list`, as it does before most hardened calls.
- A failed batch is skipped, and the other batches still copy.
- The pass copies what git lists, minus the exclusions in the bullets below. A `.claude/` the repository git-ignores is thus seeded into the new worktree.
- A `.claude/` that is merely untracked is not, because that view cannot see it.
- `.claude/worktrees/` is always skipped, because that is where session worktrees live.
- The listing is limited to the repository-root `.claude/`, so a nested one is reached only by the manifest pass.
- The `.claude/` pass has no manifest precondition. It runs git on every create where `.claude/` holds a child other than `worktrees`.

#### What both passes skip and how they copy

- Claude runtime state is skipped by both passes. The names are `scheduled_tasks.json`, `scheduled_tasks.lock`, `routines/.state`, `worktrees`, `checkpoints`, `mailbox`, `agent-registry.json`, `first-run` and `assistant-daemon-state.json`.
- They match as whole path components, directly under `.claude/`, case-insensitively. `.claude/Checkpoints/` is thus dropped, while `.claude/mailboxes/` and `.claude/nested/mailbox/` are both copied.
- The daemon skips symlinks.
- A filename that `git ls-files` C-quotes, with a tab, a quote, a backslash or a non-ASCII byte, IS copied. Both passes use `-z` and split on NUL.
- The copies do not preserve the source mode. The daemon creates them 0666-subject-to-umask, so an executable arrives non-executable and a `0400` source is widened. This matches the reference.
- Treat the manifest as a way to name configuration, not secrets or scripts.
- Destination containment on both passes is claustrum's own. Every copy resolves its destination component by component inside the new worktree.
- When an intermediate component is a symlink, the copy is dropped. So a link checked out into the worktree cannot carry a copy outside it.
- A `..` component cannot occur, because `git ls-files` never prints one.
- Treat the containment as claustrum hardening, not parity.

With an opted-in `-git-timeout` (D5) that kills a git call (off by default):

- A killed listing loses the pass, and a killed batch loses that batch.
- A killed `git check-ignore` loses the whole full scan, or the directory paths of the directory scan.
- A killed `git version` selects the full scan.
- The reply is still `{"success":true}`, so the loss is silent and wire-invisible.
- Each git call has its own deadline, so the manifest copy can succeed while `.claude/` files are lost, or the other way round.

Evidence: [Copy step](#ev-copy).

### Step 13: Answer { #step-result }

On success the response is `{"success":true,"path":"<worktreePath>","sourceBranch":"<b>","branch":"<b>"}` (see [Response](#response)).
On a failure after the add, the [Rollback](#rollback) runs first and the failure frame follows.
When an undo clause is appended, the `errorCode` does not change.

### The text rule for git messages { #step-textrule }

The text rule makes every git text in the failure frames of this method.
That covers the add failure, each part of the attach-fallback frame, the checkout failure and the checkout that the deadline killed.

1. Take stderr only. stdout is not quoted.
2. Keep the first 512 bytes.
3. Drop every byte that is not valid UTF-8, anywhere in the text. The cap comes first, so the bytes of a rune that the cap cut go too.
4. Replace each rune that is not printable with one space, with no collapsing. So `\r\n` gives two spaces. The set includes `\t`, NUL, `\x7f`, U+0085, U+00A0, U+200B and U+2028. claustrum uses Go's `unicode.IsPrint`.
5. Trim the spaces at both ends.
6. If the result is empty, use the exec error. That is `exit status 128` for a git that failed with that status, and `signal: killed` for a killed checkout. On Windows the killed checkout gives the error of the kill itself, `exit status 1`.

Each text of the attach-fallback frame gets its own 512-byte cap.

Evidence: [Text rule](#ev-textrule).

## Rollback

Two kinds of rollback exist.

### Rollback of a failed add

- After a failed add, the daemon runs no git call.
- It removes the leaf in one case: the leaf is an empty directory.
- Files that the failed add left in the leaf stay. So do a registration and a branch that it made.
- If the failed add replaced the parent of the leaf and made a new empty leaf in it, the new leaf stays.

### Rollback after a successful add

This rollback runs after the checkout failure, after a failed placement of the index, and after each `timeout` frame.
It runs four steps.
`<leaf>` is `worktreePath` exactly as sent, and `<entry>` is a name at the top of the leaf.

| Step | What it does | Text appended after a failure |
|---|---|---|
| 1 | Deletes the entries at the top of the leaf, one at a time, in the order that the directory read returns them. The names are not sorted. It stops at the first entry that cannot be deleted. | `; and the undo could not finish for <leaf>: the worktree directory, its registration, and the branch all remain; remove them by hand before retrying (RemoveAll <entry>: <OS error>)` |
| 2 | Deletes the registration. Then it runs the branch step on the branch that the call created. | See the table below. |
| 3 | Removes the leaf directory, which is now empty. | `; and the undo could not finish for <leaf>: the worktree directory remains (re-populated while undoing?); remove it by hand before retrying (removeat <leaf base name>: <OS error>)` |
| 4 | If the branch stays, appends the text of the branch step. | After `; and the undo could not finish for <leaf>: `. After a step 3 failure, the step 3 text comes first and the two parts are joined by `; `. |

Rules for all four steps:

- After a failure of step 1, the daemon undoes nothing else. No branch step runs.
- The `errorCode` does not change.
- With a trailing slash on `worktreePath`, the `removeat` part still names the base name, such as `w1`.
- The three wordings of steps 1 and 3 are fixed. Only `<OS error>` varies.
- On Windows the causes of a failed delete are an open handle, a running executable and an ACL that denies the delete. Two more causes are a process with its working directory in the leaf and a file name with a trailing dot.
- On Linux and macOS claustrum gives the same wordings with the OS error text of Go, for example `permission denied`.
- A process that the checkout leaves behind starts in the new worktree. On Windows it blocks the removal of the leaf, and the rollback reports it with the undo text.
- In attach mode the call made no branch. Step 1 then reads `the worktree directory and its registration both remain`.
- In attach mode no git call runs in step 2.

### Step 2 in detail

On Linux and macOS, step 2 deletes the entry that the tests after the add accepted, the entry of test 2.
The rollback reads neither the `.git` file of the leaf nor the `gitdir` record again.
The delete is one `os.Root.RemoveAll` of the entry name, through a root at the registrations directory.

| Case | Result |
|---|---|
| The registration cannot be deleted (Linux, macOS) | It stays. No branch step runs. Step 3 still runs. The text is `; and the undo could not finish for <leaf>: the worktree registration and the branch remain; remove them by hand before retrying (RemoveAll <registration name>: <OS error>)`. |
| The same, in attach mode | The text is `the worktree registration remains; remove it by hand before retrying (RemoveAll <registration name>: <OS error>)`. |
| Step 3 fails too | The frame carries the registration text alone and no step 3 text. |
| The registrations directory cannot be opened | The text holds `open <path>: <OS error>` in place of `RemoveAll <registration name>: <OS error>`. |
| The home guard (D2) refuses an entry path that is the home directory or holds it | Nothing is deleted. No text is added. The branch step runs. |
| The identity guard refuses a directory that is not the directory that the tests accepted | Nothing is deleted. No text is added. The branch step runs. |
| Git gave no answer to `rev-parse --absolute-git-dir`, so the tests did not run | claustrum takes the directory that the `.git` file of the leaf names. It deletes it in one case: its `gitdir` record names the leaf. |
| The same, and the record cannot be read for a permission error | claustrum attempts no delete. On Linux and macOS no branch step runs. The clause is the one above, with `RemoveAll <registration name>: permission denied` in the parentheses. |
| A registration that is not a direct child of the registrations directory | It stays. The text in the parentheses is `<registration> is not a direct child of <registrations directory>`. |
| The registrations directory is no longer the one that claustrum checked | The registration stays. The text in the parentheses is `<registrations directory> is no longer the directory that was checked`. |
| Windows | claustrum has neither test. It deletes the resolved path, adds no text for a failed delete, and runs the branch step. |

Both texts of the last rows hold absolute paths.
The delete of a direct child by its resolved path holds only for a registration inside the registrations directory.

The two guards are claustrum's own.
The identity guard runs after the open of the registrations directory, on the entry in that opened directory.
claustrum holds the accepted directory open until the create answers.
While that handle is open, a new directory does not get that identity.
If the open fails, the identity comes from a stat of the path.
This is divergence D24 (see [`DIVERGENCES.md`](../DIVERGENCES.md#d24)).

Evidence: [Rollback](#ev-rollback), [`branchKept` rows](#ev-branchkept).

### What the rollback leaves

| What | State after the rollback |
|---|---|
| `.git/worktrees/` directory | No rollback runs `git worktree remove`, so the directory stays. The first linked worktree's failed checkout leaves it empty. |
| The entry of a linked worktree used as `baseRepo` | It stays, and a retry at the same path fails the same way. |
| The attached branch in attach mode | It is kept. |
| The branch that the call created | The branch step decides. See [The branch step](../PROTOCOL.md#the-branch-step). |
| A retry at the same path | It succeeds with a new `branchName`. A retry with the same `branchName` succeeds in one case: the rollback deleted the branch. |
| After a refusal of a registration test, the stale entry test or the record test | Nothing is rolled back. The leaf holds its `.git` file only, the registration stays, and the branch stays. |

The rollback of a failed checkout works as follows. It empties the new directory. It removes the registration in the `.git/worktrees/` directory of the main repository. It runs the branch step on the branch that the call created. Then it removes the empty directory.
The git calls of the rollback are those of the branch step.

## Differences from the reference

The reference build is `89cb6289` unless a row names another build.

| D-number | claustrum | `89cb6289` | Link |
|---|---|---|---|
| D24 | In the rollback, a directory that replaced the new registration is kept. No directory is removed. | Removes the empty `w1` (cell B6) or the directory with the files of a sibling (cell B6b). | [D24](../DIVERGENCES.md#d24) |
| D2 | The home guard refuses an entry path that is the home directory or holds it, in the stale entry step and in the rollback. | This section does not state it. | [D2](../DIVERGENCES.md#d2) |
| D5 | With `-git-timeout` opted in, a killed copy call loses what that call gives the pass. The response stays `{"success":true}`. | This section does not state it. | [D5](../DIVERGENCES.md#d5) |
| D19 | On Windows, a junction at `.claude` or `.claude\worktrees` fails the parent step with `mkdir_failed`. | For a junction `<P>\J` above them, equal frames after 3 git calls (cells J-b-wt-pj, J-b-wt-pJ). | [D19](../DIVERGENCES.md#d19) |
| open | `branchKept` comes after `sourceBranch` and `branch`. | No measured frame with `branchKept` has `sourceBranch` or `branch`. | [DIVERGENCES.md](../DIVERGENCES.md) |
| open | The `rev-parse --show-toplevel` pair runs once with `baseRepo` as sent, before the record test refusal. | The pair runs twice with `baseRepo` as the resolved path (cells T2, T10). No frame differs. | [DIVERGENCES.md](../DIVERGENCES.md) |
| open | Two texts of a failed placement are claustrum's own: `the registration <entry> is not the folder that was tested after the add` and `the registration <entry> has no gitdir record that names this worktree`. | No cell measured the placement for these states. | [DIVERGENCES.md](../DIVERGENCES.md) |
| open | When an intermediate component of its destination is a symlink, a copy is dropped. | Whether the reference refuses the same is unmeasured. | [DIVERGENCES.md](../DIVERGENCES.md) |
| open | The batch budget of the copy step counts the `-c` options as zero. The values 131 072, 24 576, 130 985 and 24 489 are a fit to measured batch counts. | Whether `f6010b97` counts the `-c` options was not measured. | [DIVERGENCES.md](../DIVERGENCES.md) |
| open | claustrum compares the record of a stale entry by text. | The cells equal in the frame and on the disk. A tab or a CR at an end of the record, and a relative record with a `baseRepo` sent through a symlink, are not measured. | [DIVERGENCES.md](../DIVERGENCES.md) |
| open | If `rev-parse --absolute-git-dir` gave no answer, claustrum runs none of the four tests. | Not measured. | [DIVERGENCES.md](../DIVERGENCES.md) |
| open | On Windows, claustrum runs no registration test and moves the temporary index. | Not measured on Windows. | [DIVERGENCES.md](../DIVERGENCES.md) |

## Platform differences

| Behavior | Linux | macOS | Windows |
|---|---|---|---|
| `worktreeRoot` | Supported. | Supported. | Refused before any location test. |
| Spelling refusal (trailing dot or space, colon) | No. | No. | Yes, before containment. |
| `<p>` of "already exists" | Parent directory symlinks resolved, last name kept, cleaned. | Same as Linux. | On-disk letter case of each existing component. Volume name and 8.3 short name as sent. |
| Modes of created directories (0755 above the leaf, 0777 leaf, umask applies) | Yes. | Yes. | Not stated. |
| Mode 0700 for directories above the leaf with `worktreeRoot` | Yes. | Yes. | Not applicable, `worktreeRoot` is refused. |
| Junction in the path | Not applicable. | Not applicable. | Fails the parent step with `mkdir_failed`. |
| Ancestor test, root-chain tests, root owner and write tests | Yes. | Yes. | Not applicable. |
| Group-write test | `/etc/passwd` and `/etc/group`. A user known only to an NSS source such as LDAP counts as shared. | The same flat files. The directory service does not count. A stock user has no `/etc/passwd` line, so every group-writable root is refused. | Not applicable. |
| Passwd name with a leading space | Does not match the user. | Not stated. | Not applicable. |
| Stale entry step | Runs with `locked` test and root. | Same. | Earlier step. It compares the directory that holds the record path with `worktreePath`. No `locked` test. No root. |
| Four registration tests, stale entry test, record test | Run. | Run. | Not run. |
| Case of loose and packed refs for `sourceBranch` | Not stated. | A loose ref matches in any case, also under `Origin`. A packed ref does not. | A loose ref matches in any case. A packed ref does not. |
| `--work-tree` | New worktree, symlinks resolved. | Same as Linux. | Path as sent. |
| Index placement | New file made exclusively. | Same as Linux. | The temporary file is moved. If the move fails, there is no index and the create succeeds. |
| Group of the new index file | Group of a setgid directory, else the primary group. | Group of the registration directory. | Not stated. |
| Exec error for a killed checkout with empty stderr | `signal: killed`. | `signal: killed`. | `exit status 1`. |
| Delete of the registration in the rollback | Through a root. Direct-child and identity tests. Failed delete skips the branch step. | Same as Linux. | Deletes the resolved path. No tests. No text. The branch step runs. |
| Cause of a failed delete in the rollback | Go OS error text, for example `permission denied`. | Same as Linux. | Open handle, working directory in the leaf, running executable, ACL, trailing dot. |
| Batch budget of the copy step | 131 072 bytes. | 131 072 bytes. | 24 576 bytes. |
| Batches of the `.claude/` pass | 130 985 bytes. | 130 985 bytes. | 24 489 bytes. |
| Handles of the leaf and its parent | Held open until the answer. | Held open until the answer. | Held open until the answer, with delete sharing. |
| NFC and NFD spelling of the directory | Not stated. | Git records NFC. NFC sent in NFD is refused. NFD sent in NFC succeeds. | Not stated. |

## Evidence

Each table has these columns: cell or row, the state, the result on `89cb6289`, the result on claustrum, the VMs, and the runs.
When a row uses another build than `89cb6289`, the result column names it.
"Equal" means that claustrum answers the same frame and leaves the same disk.
A row with no cell name is a measurement that the text gives in a sentence.
"Not given" means that the source text gives no value.

### Evidence: repository and paths { #ev-path }

| Cell or row | State | Result on `89cb6289` | Result on claustrum | VMs | Runs |
|---|---|---|---|---|---|
| T9, U1a to U1d, U2a to U2c, U3a2 | The `already exists` text on Linux and macOS | The `<p>` has the symlinks of the parent directory resolved, the last name kept, and the path cleaned. | Equal | Linux, macOS | 2 each |
| W15 | The `already exists` text on Windows, no `worktreeRoot` | The `<p>` has the on-disk letter case of each component that exists. `f6010b97` does the same. | Same rule | Windows | Not given |
| (no cell) | Absent `baseRepo` | `f6010b97`: the empty string, so a space comes before the semicolon. | Same rule | Linux, macOS | Not given |
| (no cell) | Empty `worktreePath`, nested path, trailing slash, `//`, `/./`, `path` and undo texts quote `worktreePath` as sent | `f6010b97` and `90fca6e6` agree with the rules of Step 3. | Same rule | Linux, macOS | Not given |
| (no cell) | Modes of created directories: 0755 above the leaf, 0777 for the leaf, umask applies. Texts name the repository with symlinks resolved. | `f6010b97`: as the rules say. Both VMs ran umask 0000, which tells a leaf request of 0777 from 0775. | Same rule | Linux, macOS | Not given |
| (no cell) | Replace objects and grafts | `f6010b97`: the checkout holds the real blob and commit. Ancestry is the real ancestry. | claustrum sets `GIT_NO_REPLACE_OBJECTS=1` and `GIT_GRAFT_FILE=<null>` | Not given | Not given |

### Evidence: root tests { #ev-root }

| Cell or row | State | Result on `89cb6289` | Result on claustrum | VMs | Runs |
|---|---|---|---|---|---|
| R1n, R1u, R1r | `worktreeRoot` is `/` | The frame of the error table, sent after the excludes read and the repository test, 3 git calls. Nothing is created. | Not given | Linux | Not given |
| R1a | `worktreeRoot` is `/` | The same frame. | Not given | macOS | Not given |
| C7, C8, C9 | `baseRepo` is not absolute or has a `..` component | `f6010b97`: the refusal after the repository test. Nothing is created. | Tests it after the `worktreePath` spelling and before the two-level test (order not measured) | Linux, macOS | Not given |
| K1 to K4, K6 to K9, E2, W3, W4, Y11a to Y11c | The three checkout tests of the root | `f6010b97` and `89cb6289` refuse as Step 4 says. | Not given | Linux, macOS | Not given |
| K1, G1 to G41b, Y11d, T7, T7c | The ancestor test, texts, rules, limits | `f6010b97` and `89cb6289` refuse as Step 4 says. | Not given | Linux, macOS | Not given |
| G42, the macOS round 2 rows | The ancestor test | Measured against `89cb6289` only. | Not given | Not given | Not given |
| (no cell) | Root-chain tests, texts and order | `f6010b97`: `.git` entry or symlink loop refuses with `unsafe_path`. | Not given | Linux, macOS | Not given |
| (no cell) | Order of the `<directory>` file test and the search test | `f6010b97`: the file test comes after the writable-root and foreign-owner tests. The search test comes after the writable-root test. | Not given | Linux | Not given |
| (no cell) | Order of the symlinked `<directory>` test | `f6010b97`: before the `<directory>`-level tests. | Not given | Linux, macOS | Not given |
| (no cell) | `R/proj/w1/` names `R/proj`. `R/cp/w1/` is quoted as `R/cp/w1`. | `f6010b97` and `90fca6e6` agree. | Not given | Linux, macOS | Not given |
| U3a, U3a2 | `worktreeRoot` behind a symlink | Both paths are named with the link resolved. | Equal | Linux, macOS | Not given |
| (no cell) | Owner and writable texts, the four tests of the private group, the account-file line format | `f6010b97`, except where a line says otherwise. | Not given | Linux, macOS | Not given |
| G1c, G2c, G4c | `nested_base_repo` refusal | `f6010b97`: the frame with no git run and nothing created. | Not given | Linux, macOS | Not given |
| C1 (round 1) | `nested_base_repo` refusal | `f6010b97`: the frame. | Not given | Windows | Not given |
| K1, D1, D2, D4, J1, J3 (round 2) | `nested_base_repo` refusal | `f6010b97`: the frame. | Not given | Windows | Not given |
| C1 P1 (round 1) | Order against the `GIT_CONFIG_COUNT` check | The refusal comes first. | Not given | Windows | Not given |

### Evidence: group and account files { #ev-account }

| Cell or row | State | Result on `89cb6289` | Result on claustrum | VMs | Runs |
|---|---|---|---|---|---|
| (no cell) | Private group test: single `/etc/passwd` line with the gid | `f6010b97`: measured on Linux VMs only. Whether found by uid or by name is not measured. | Not given | Linux | Not given |
| (no cell) | Passwd name with a leading space | `f6010b97`: does not match the user (Linux only). | Same rule | Linux | Not given |
| (no cell) | `+name` line, `#` line, field counts 6 to 8, trimming of group members | `f6010b97`: as the list in Step 4 says. | Same rule | Linux, macOS | Not given |
| (no cell) | Stock macOS user, no `/etc/passwd` line | Every group-writable root is refused. | Same rule | macOS | Not given |

### Evidence: parents, marker and junctions { #ev-parents }

| Cell or row | State | Result on `89cb6289` | Result on claustrum | VMs | Runs |
|---|---|---|---|---|---|
| JCR1 | Junction at `.claude` | `f6010b97`: the `mkdir_failed` frame. Nothing is created. | Same frame | Windows | Not given |
| JCR2 | Junction at `.claude\worktrees` | `f6010b97`: the `mkdir_failed` frame. Nothing is created. | Same frame | Windows | Not given |
| J-b-wt-pj, J-b-wt-pJ | Junction `<P>\J` above `.claude` | Equal frames after 3 git calls. | Equal | Windows | Not given |
| (no cell) | Directories made above the leaf with `worktreeRoot` ask mode 0700. The marker has 285 bytes. An existing entry keeps its content and mode. A failed add keeps the marker. | `f6010b97`: as the rules say. | Same rule | Linux, macOS | Not given |
| (no cell) | A directory made before a later failure stays | `f6010b97`: it stays. | Same rule | Linux, macOS | Not given |

### Evidence: stale registration step { #ev-stale }

All rows ran on `89cb6289` with git 2.43 on the Linux VM and git 2.50 on the macOS VM, 2 runs each.
In every row claustrum equals `89cb6289` in the frame and on the disk.

| Cell or row | State | Result on `89cb6289` | Result on claustrum | VMs | Runs |
|---|---|---|---|---|---|
| A1, A2 | One stale entry, record `<L>/.git/` | The entry is removed. | Equal | Linux, macOS | 2 each |
| A3, A4 | One stale entry, record `<L>/.git` with and without a newline | The entry is removed. | Equal | Linux, macOS | 2 each |
| A5 | Record `<L>/.git//` | The entry is removed. | Equal | Linux, macOS | 2 each |
| A5b | Record `<L>/.git/.` | The entry is removed. | Equal | Linux, macOS | 2 each |
| A5c | Record `<L>/.git` with a blank and a newline | The entry is removed. | Equal | Linux, macOS | 2 each |
| A7 | Relative record `../../../.claude/worktrees/w1/.git` | The entry is removed. | Equal | Linux, macOS | 2 each |
| A8 | The entry is named `old9` | The entry is removed. | Equal | Linux, macOS | 2 each |
| A13 | Stale entries `old9` and `w1` | None is removed. | Equal | Linux, macOS | 2 each |
| S1 | Stale entries `old8`, `old9` and `w1` | None is removed. | Equal | Linux, macOS | 2 each |
| S2 | Stale entries `old8` and `old9` | None is removed. | Equal | Linux, macOS | 2 each |
| S6 | Two stale entries whose records differ in their spelling | None is removed. | Equal | Linux, macOS | 2 each |
| A13b, S5 | A stale entry and a second stale entry with a `locked` file | Both stay. | Equal | Linux, macOS | 2 each |
| S3, S4 | A stale entry beside an entry that is not stale | The stale entry goes. | Equal | Linux, macOS | 2 each |
| S7, S7b, S7c | A stale entry beside a regular file, a directory with no `gitdir` record, or an empty directory | The stale entry goes. | Equal | Linux, macOS | 2 each |
| A6, A6b | Records `<L>` and `<L>/` | The entry stays. | Equal | Linux, macOS | 2 each |
| A11 | An entry of a live worktree at another path | The entry stays. | Equal | Linux, macOS | 2 each |
| A12b | A record that spells the path through a symlink, request with the real path | The entry stays. | Equal | Linux, macOS | 2 each |
| A12c | A record with the real path, request through a symlink | The entry goes. | Equal | Linux, macOS | 2 each |
| A9, A9b | An entry that holds a `locked` file, an empty file | The entry stays. | Equal | Linux, macOS | 2 each |
| A10 | The entry has mode 0500 | Only `logs/HEAD` goes. | Equal | Linux, macOS | 2 each |
| A10b | The `worktrees` directory has mode 0555 | The files go and the empty directory stays. | Equal | Linux, macOS | 2 each |
| A1 p, A7 p | No daemon `GIT_*` variable, entry removed | The create succeeds with one entry `w1`. | Equal | Linux, macOS | 2 each |
| A9 p, A10 p, A13 p, S1 p | No daemon `GIT_*` variable, entry kept | Git names the new registration `w11` and the create succeeds. | Equal | Linux, macOS | 2 each |
| A6 p, A9b p, A10b p, S6 p | No daemon `GIT_*` variable, entry kept | The add fails with the text of git. | Equal | Linux, macOS | 2 each |
| P-p | The record is that of cell A1 | See the table of the stale entry test and record test. | Equal | Linux, macOS | Not given |

### Evidence: start commit { #ev-source }

All rows were measured side by side against `f6010b97`.

| Cell or row | State | Result on `89cb6289` | Result on claustrum | VMs | Runs |
|---|---|---|---|---|---|
| (no cell) | The two candidates L and R, suffix `feat~1`, slash name `team/feat`, symbolic ref `HEAD`, annotated tag, absent objects | `f6010b97`: as the rules of Step 7 say. | Same rule | Linux, Windows, macOS | Not given |
| (no cell) | Only `origin` is read. No fetch. | `f6010b97`: a stale tracking ref is used as it is. | Same rule | Linux, Windows, macOS | Not given |
| (no cell) | Loose and packed refs on a case-insensitive file system | `f6010b97`: a loose ref matches in any letter case, a packed ref does not. On macOS a loose ref under `Origin` counts. | Same rule | Windows, macOS | Not given |
| (no cell) | Selection by `merge-base --is-ancestor`, `merge-base` and the `diff --quiet` test | `f6010b97`: as the rules say. | Same rule | Linux, Windows, macOS | Not given |
| (no cell) | The add gets the full id. The reflog reads `branch: Created from <sha>`. No upstream. | `f6010b97`: as the rules say. | Same rule | Linux, Windows, macOS | Not given |
| (no cell) | `sourceBranch` omitted, or one that resolves to nothing | `f6010b97`: no start point, reflog `branch: Created from HEAD`, echo from `rev-parse --abbrev-ref HEAD`. | Same rule | Not given | Not given |

### Evidence: attach, fallback and failed add { #ev-add }

| Cell or row | State | Result on `89cb6289` | Result on claustrum | VMs | Runs |
|---|---|---|---|---|---|
| (no cell) | `existingBranch` that resolves, empty, or unknown | `19f30c46`: attach with no `-b`, or the new-branch path. A miss falls back silently. | Same rule | Not given | Not given |
| (no cell) | The attach add fails, and the fallback add succeeds or fails | `f6010b97` and `90fca6e6`: the success frame or the fallback frame of the error table. `90fca6e6` passes the ref name as the start point and `f6010b97` the full id. The new branch lands on the same commit. | claustrum passes the full id | macOS | Not given |
| (no cell) | A failed attach add deleted the leaf or put a new directory in its place | `f6010b97` and `90fca6e6`: no fallback, the reply is `git worktree add failed: <attach text>`, and the rollback of a failed add runs. | Same rule | macOS, Windows | Not given |
| (no cell) | A replacement leaf, ext4 | Both references held the leaf and its parent open. The replacement leaf got a new inode. | claustrum holds both open until it answers, so a replacement cannot reuse the inode of the leaf. | Linux, Windows | 12 of 12 |
| (no cell) | Rename under the handles of `f6010b97` | The leaf can be renamed. A rename of the parent fails with "Access is denied." while the leaf is inside it. | Both handles share delete. The identity of the leaf comes from its handle. | Windows | Not given |
| (no cell) | A stub git that fails after it wrote into the leaf | `f6010b97` and `90fca6e6`: the files, the registration and the branch stay. A retry answers `unsafe_path` "already exists". | Same rule | macOS, Windows | Not given |
| (no cell) | A failed attach add replaced the parent and made a new empty leaf | Both references kept the new leaf. | Same rule | Linux, macOS | 20 of 20 |
| (no cell) | Graft-file deprecation `hint:` lines in the add text | `f6010b97`: the text can start with them. `90fca6e6` prints no hint. | Same as `f6010b97` | Not given | Not given |
| (no cell) | Failure text `git worktree add failed: Preparing worktree (new branch 'dup') fatal: a branch named 'dup' already exists` | `90fca6e6`: this text. | Not given | Not given | Not given |
| (no cell) | `existingBranch:"main"` and no `sourceBranch` | `{"success":true,"path":"<p>","sourceBranch":"main","branch":"<branchName>"}` | Not given | Not given | Not given |

### Evidence: text rule { #ev-textrule }

| Cell or row | State | Result on `89cb6289` | Result on claustrum | VMs | Runs |
|---|---|---|---|---|---|
| (no cell) | The six steps of the text rule | `f6010b97` and `90fca6e6`: as the rule says. The measured set of non-printable runes includes `\t`, NUL, `\x7f`, U+0085, U+00A0, U+200B and U+2028. | claustrum uses Go's `unicode.IsPrint`, which fits every measured payload. That is an inference, not a proof. | macOS | Not given |

### Evidence: registration tests { #ev-tests }

The `.git` file holds the path that git wrote.
Git 2.43 runs on the Linux VM and git 2.50 on the macOS VM.

| Cell or row | State | Result on `89cb6289` | Result on claustrum | VMs | Runs |
|---|---|---|---|---|---|
| B-E1, B-E3, D-8, D8e | `baseRepo` has no `worktrees` directory. Git makes the registration in X, and the `.git` file names `<X>/.git/worktrees/w1`. | The "was not populated" text. | Not given | Linux, macOS (D-8: macOS) | B-E1 and B-E3: 10 of 10 |
| P-p | Directory present, no entry of the name | The text of test 1. | Equal | Linux, macOS | Not given |
| D-1, D-5, D-7 | `<git dir>/worktrees` is a symlink to `<F>/WTREG`. D-5 is a relative link. In D-7 `.git` is a symlink too. | macOS: git 2.50 writes the resolved path, `<F>/WTREG/w1`, and the text of test 1 follows. Linux: git 2.43 writes the path through the link, `<baseRepo>/.git/worktrees/w1`, and the worktree is created. | Not given | Linux, macOS | Not given |
| D-11 | The link of D-1 and a git wrapper that writes the resolved path | The refusal. | Not given | Linux | Not given |
| D-3 | The link target is `<F>/alt/worktrees` | Both VMs create the worktree. The `.git` file holds `<F>/alt/worktrees/w1` on the macOS VM. | Not given | Linux, macOS | Not given |
| D-4 | The link target is `<F>/alt/WORKTREES` | The macOS VM refuses and the Linux VM creates. | Not given | Linux, macOS | Not given |
| D-6 | `<baseRepo>/.git` is a symlink to `<F>/GITDIR`. The `.git` file holds `<F>/GITDIR/worktrees/w1`. | Both VMs create the worktree. | Not given | Linux, macOS | Not given |
| D-14 | A repository under `/tmp` | The worktree is created. | Not given | macOS | Not given |
| D-13 | A wrapper writes `../../x` into the `commondir` file of the registration after the add | The text of test 1. | Not given | Linux, macOS | Not given |
| P-c | `baseRepo` holds an old entry `w1` of another worktree. Git makes the new registration in another repository. | The text of the record test. | Not given | Linux, macOS | Not given |
| P-g | As P-c, with the `gitdir` record as a FIFO | The text of test 1. | Not given | Linux, macOS | Not given |
| P-i | As P-c, with the `gitdir` record as an empty directory | The text of test 1. | Not given | Linux, macOS | Not given |
| P-j | As P-c, with the `gitdir` record removed | The text of test 1. | Not given | Linux, macOS | Not given |
| P-h | As P-c, with the `commondir` file as a FIFO and the record as it was | The text of test 1. No read-tree runs. The old `index` keeps its inode, mtime and bytes. The FIFO cells answer in under 1 s. | Not given | Linux, macOS | Not given |
| P-n | As P-c, with the `commondir` file of the old entry removed | The text of test 1. No read-tree runs. | Not given | Linux, macOS | Not given |
| P-o | As P-c, with the `gitdir` record at mode 0000 | The text of test 1. No read-tree runs. | Not given | Linux, macOS | Not given |
| P-q | As P-c, with the directory of the old entry at mode 0000 | The text of test 1. No read-tree runs. The stat of the entry directory still answers. | Not given | Linux, macOS | Not given |
| D-12 | A wrapper writes `gitdir: /elsewhere/worktrees/w1` into the `.git` file after the add | Success. The `.git` file stays. The registration that git made holds the `index`. | Puts the index into the entry of test 2 | Linux, macOS | Not given |
| P-d, Pd2 | A wrapper writes `gitdir: /elsewhere/WTREG/w1` after the add. `<git dir>/worktrees` is a symlink in P-d and a plain directory in Pd2. | The text of test 1. | Not given | Linux, macOS | Not given |
| P-a, P-b | The layout of D-1 with attach mode (P-a) and with a `worktreeRoot` (P-b) | macOS: the text of test 1. Linux: both succeed and the `index` is in the link target. | Not given | Linux, macOS | Not given |
| P-e | `baseRepo` is a linked worktree of a repository T | The worktree is created. Its registration and its `index` are in T. | Not given | Linux, macOS | Not given |
| S-a | `baseRepo` is a subdirectory of a repository | The worktree is created. | Equal | Linux, macOS | Not given |
| S-b | `baseRepo` is a bare repository | `not_a_repo`. | Equal | Linux, macOS | Not given |
| S-c | A repository with a separate git directory | The worktree is created. | Equal | Linux, macOS | Not given |
| S-d | A daemon `GIT_DIR` alone | The worktree is created. | Equal | Linux, macOS | Not given |
| Se0, S-e | Attach, and an attach that falls back to a new branch, in a plain layout | The worktree is created. | Not given | Linux | Not given |
| B-E2, B-E4 | `GIT_COMMON_DIR` of X in the environment, different commit ids | The add fails. Both sides answer the add-failure frame. | Equal | Linux, macOS | Not given |
| (no cell) | `GIT_DIR` of X too in the environment | The create succeeds. Git makes the registration in X. It is probe row 2 of `git.worktree_remove`. | Finds the entry in X (from the code) | Linux, macOS | Not given |
| D-9 | `timeoutMs` 1 and an add that takes 3 s | The refusal of test 1. The leaf, the registration and the branch stay. | Not given | macOS | Not given |
| D-2 | A read-tree that fails | The refusal of test 1. No read-tree runs. | Not given | macOS | Not given |

### Evidence: stale entry test and record test { #ev-record }

| Cell or row | State | Result on `89cb6289` | Result on claustrum | VMs | Runs |
|---|---|---|---|---|---|
| A9 g, A9b g | The daemon has `GIT_COMMON_DIR` of another repository. The old entry `w1` has a record that names the new `.git`, and a `locked` file. | The stale entry text. The add is the last of 9 git calls. | Equal | Linux, macOS | 2 each |
| A10 g | The same, with the entry at mode 0500 | The stale entry text. | Equal | Linux, macOS | 2 each |
| A13 g, A13b g, S1 g, S5 g, S6 g | Two or more stale entries, every one stays | The stale entry text where the entry of test 2 is one of them. | Equal | Linux, macOS | Not given |
| P-c | An old entry with the record of another worktree. The daemon `GIT_COMMON_DIR` is of X, with equal commit ids. Git makes the new registration in X. | The text of the record test. The pair runs. Nothing changes after the add and the old `index` keeps its bytes. | Not given | Linux, macOS | Not given |
| T2, T10 | The record test | The call log shows the `rev-parse --show-toplevel` pair twice, with `baseRepo` as the resolved path. | The call log shows it once, with `baseRepo` as sent. No frame differs. | Linux, macOS | Not given |
| I07a | An NFC directory sent in NFD | Both references refuse, because git records the path in NFC. | Not given | macOS | Not given |
| I07b | An NFD directory sent in NFC | Both references succeed. | Not given | macOS | Not given |
| I01e | A `/tmp` path | Creates as usual on both references. | Equal | Not given | Not given |
| CSa to CSd | Creates | Succeeded on both references. | Succeeded | Linux | Not given |
| Z9a | A relative record that git wrote with `worktree.useRelativePaths` | The create succeeds. | Not given | macOS | Not given |
| S-f | Relative records in a plain layout, git 2.50 | Both create. | Both create | macOS | Not given |
| (no cell) | The path sent in another letter case (`<F>/t` for `<F>/T`), or in an NFD spelling | Both answer the record refusal. The frame and the disk are equal. | Equal | Not given | Not given |
| P-f | The state of P-c with `timeoutMs` 1 and an add that takes 3 s | The record refusal, not the `timeout` frame. The leaf with its `.git` file and the branch stay. | Not given | Linux, macOS | Not given |
| P-k | The old record is the relative path `../../../gone/w1/.git` | The record refusal. The old `index` keeps its inode, mtime and bytes. | Not given | Linux, macOS | Not given |
| P-l | The old record is an empty file | The record refusal. | Not given | Linux, macOS | Not given |
| P-m | The old record is as in P-c, and the old entry holds a `locked` file | The record refusal. | Not given | Linux, macOS | Not given |
| P-g, P-i, P-j against P-k, P-l | Which record counts | A record that cannot be read as a file gets the text of test 1. A record that was read and names anything else gets the record text. | Not given | Linux, macOS | Not given |
| P-p | The old record of P-c holds `<worktreePath>/.git/` with a slash at its end and no newline | The old entry is gone before the add. A system call trace shows that the daemon removes it, not git. The text of test 1. | Removes the entry in the step before the add and answers the text of test 1. | Linux, macOS (trace: Linux) | Not given |
| B-E1 | The same state with no `worktrees` directory in `baseRepo` | The "was not populated" text. | Equal | Linux, macOS | Not given |
| B4 | A wrapper removes the record after the read-tree | Success. The entry holds the index. The branch stays. | Equal | Linux, macOS | Not given |
| Z15 | A wrapper removes the record after the read-tree and sets the entry to mode 0500 | `git worktree add failed (checkout): <git text> openat w1/index: permission denied`, then the undo text with `(RemoveAll w1: permission denied)`. The entry and the branch stay. `logs/HEAD` is gone. `HEAD`, `commondir` and `logs` stay. | Equal | Linux, macOS | Not given |
| P-a, P-b, S-e, Se0 | Where the tests ran in attach mode and with `worktreeRoot` | See the table of the registration tests. | Same tests in every layout | Linux | Not given |

### Evidence: spelling of paths { #ev-spelling }

All rows ran on `89cb6289` and claustrum answers the same frame in each.

| Cell or row | State | Result on `89cb6289` | Result on claustrum | VMs | Runs |
|---|---|---|---|---|---|
| T1, T5, A12c g | A symlink at `baseRepo`, both request paths through it | The text of test 1. | Equal | Linux, macOS | 2 each |
| T4 | The same | The "was not populated" text. | Equal | Linux, macOS | 2 each |
| T3 | The same | The stale entry text. | Equal | Linux, macOS | 2 each |
| T2, T10 | The same | The text of the record test. | Equal | Linux, macOS | 2 each |
| T9 | The same | `already exists`. | Equal | Linux, macOS | 2 each |
| U1a | The leaf is a symlink to a directory | `already exists`, naming the leaf. | Equal | Linux, macOS | 2 each |
| U1b | The leaf is a dangling symlink | `already exists`, naming the leaf. | Equal | Linux, macOS | 2 each |
| U1c | The leaf is a regular file | `already exists`, naming the leaf. | Equal | Linux, macOS | 2 each |
| U1d | The leaf is a symlink to a file | `already exists`, naming the leaf. | Equal | Linux, macOS | 2 each |
| U2a to U2c | A slash at the end, a double slash, a `/./` part | `already exists`. | Equal | Linux, macOS | 2 each |
| U2d | The same paths | The text of the record test. | Equal | Linux, macOS | 2 each |
| U2e | The same paths | The "was not populated" text. | Equal | Linux, macOS | 2 each |
| U2f | The same paths | The text of test 1. | Equal | Linux, macOS | 2 each |
| U2g | The same paths | The stale entry text. | Equal | Linux, macOS | 2 each |
| U3a | A `worktreeRoot` behind a symlink, `worktreePath` through it | The "is not marked" text. | Equal | Linux, macOS | 2 each |
| U3a2 | The same | `already exists`. | Equal | Linux, macOS | 2 each |
| U3b | The same | The text of the record test. | Equal | Linux, macOS | 2 each |
| T6, T7 | A rollback | The leaf is named as sent in the undo clause. | Equal | Linux, macOS | 2 each |
| T8, U2h, U3c | A success | The leaf is named as sent in the `path`. | Equal | Linux, macOS | 2 each |
| T12b | A locked refusal of `git.worktree_remove` | The leaf is named as sent. | Equal | Linux, macOS | 2 each |

### Evidence: deadline { #ev-deadline }

| Cell or row | State | Result on `89cb6289` | Result on claustrum | VMs | Runs |
|---|---|---|---|---|---|
| D-9 | `timeoutMs` 1, add 3 s | Test 1 comes before the deadline test. | Not given | macOS | Not given |
| P-f | The record test state with `timeoutMs` 1 | The record test comes before the deadline test. | Not given | Linux, macOS | Not given |
| (no cell) | The deadline rules (no kill of the add, kill of the checkout, no kill of the copy step, retry after the rollback) | `f6010b97` and `90fca6e6`, except where a rule names another build. | Same rules | macOS | Not given |
| (no cell) | `90fca6e6` prints no hint before the kill. `f6010b97` and claustrum can. | As stated. | As stated | macOS | Not given |
| (no cell) | The cap on the drain of about 5 s from the exit of git, independent of `timeoutMs` | `4534d86`: measured. | Same rule | Not given | Not given |

### Evidence: checkout { #ev-checkout }

| Cell or row | State | Result on `89cb6289` | Result on claustrum | VMs | Runs |
|---|---|---|---|---|---|
| (no cell) | The checkout command, `--git-dir`, `--work-tree`, `GIT_INDEX_FILE`, and the configuration precursor | `f6010b97` and `90fca6e6`: as the rules say. | Same rule | Windows | Not given |
| D02, I01c, I01e, CSc | `--work-tree` on a create without `worktreeRoot` | `--work-tree` names the new worktree with its symlinks resolved, as on `f6010b97`. | Same rule | Linux, macOS | Not given |
| (no cell) | A failed checkout | `f6010b97`: the rollback and the end states of the rollback. Apart from the hint, the frame matches `90fca6e6` byte for byte. | Same rule | Not given | Not given |

### Evidence: index file { #ev-index }

| Cell or row | State | Result on `89cb6289` | Result on claustrum | VMs | Runs |
|---|---|---|---|---|---|
| A1 to A13 | The new index file | The facts of the list in Step 11. | Equal | Linux, macOS | Not given |
| (no cell) | Inode | A new inode, not the temporary file. | Equal | Linux, macOS | Every Linux row. 13 of 13 macOS runs that saw the temporary file. |
| A1, A3, A5, A6 | Group on macOS | The group of the directory. | Equal | macOS | Not given |
| A8 | Group on Linux, setgid directory | The group of the setgid directory. | Equal | Linux | Not given |
| A9 | Group on Linux, no setgid | The primary group. | Equal | Linux | Not given |
| A12 | Mode and umask | 0644 for umask 0022, and 0600 and 0664 for the umasks 0077 and 0002. | Equal | Linux, macOS | Not given |
| A13 | `core.sharedRepository group` | Mode 0644. | Equal | Linux, macOS | Not given |
| A10, A11 | Temporary directory on another file system | Mode 0644. | Equal | Linux, macOS | Not given |
| (no cell) | mtime | The mtime of the temporary index, rounded up to a whole microsecond. | Equal | Linux, macOS | Every Linux row. 15 of 15 macOS runs. |
| X8 | A file of mode 0600 at the index | A new inode of mode 0644. | Equal | Linux, macOS | Not given |
| Z7a | An empty directory at the index | Replaced by the index. | Equal | macOS | Not given |
| Z7b | A symlink at the index | The link goes, its target stays, and the index is a new file. | Equal | macOS | Not given |
| A14, Y3, Y7, X8, Z7a, Z7b, Z11a, Z11b | The order of the create | The order of claustrum fits these eight rows (Z7a, Z7b, Z11a and Z11b: macOS). | The order of claustrum | Linux, macOS | Not given |

### Evidence: failed placement { #ev-placefail }

| Cell or row | State | Result on `89cb6289` | Result on claustrum | VMs | Runs |
|---|---|---|---|---|---|
| A14, A14f | The registration directory loses its write bit during the checkout | `openat w1/index: permission denied`. The leaf is gone. The registration and the branch stay. The step 2 undo text follows. No git call runs after the checkout. | Not given | Linux | Not given |
| A14, A14b | The same | The same result. | Not given | macOS | Not given |
| X1 | The temporary index is gone at the exit of git with status 0 | `open <temporary directory>/index: no such file or directory`. The leaf, the registration and the branch go. | Not given | Linux, macOS | Not given |
| Y6 | The same, in attach mode | The same frame. | Not given | Linux | Not given |
| Y4 | As Y6 (attach mode), and the registration directory has no write bit | The step 2 undo text follows. | Not given | Linux | Not given |
| Y3 | A file at the index, and the registration directory has no write bit | `removeat w1/index: permission denied`. The step 2 undo text follows. | Not given | Linux | Not given |
| Y7 | A directory that holds one file at the index | `removeat w1/index: directory not empty`. The rollback runs. | Not given | Linux | Not given |
| Z10 | The registration is gone at the install | `openat w1/index: no such file or directory`. The whole rollback runs. | Not given | macOS | Not given |
| Z11a, Z11b | The registrations directory has mode 0600 after the checkout, with or without a file at the index | `openat w1/index: permission denied`. The step 2 undo text follows. The leaf goes. The registration and the branch stay. | Not given | macOS | Not given |
| A14, A14f | A stderr that ends with one newline | One space before the OS error. | Not given | Linux | Not given |
| Y2a | A stderr with no final newline | No space. | Not given | Linux | Not given |
| Y2b | A stderr with two final newlines | Two spaces. | Not given | Linux | Not given |
| X2 | No stderr | The text is the OS error alone. | Not given | Linux | Not given |
| Y1b | A stderr of 478 bytes | The whole OS error is kept. | Not given | Linux | Not given |
| Y1a | A stderr of 500 bytes | The first 12 bytes of the OS error, ` openat w1/in`, are kept. | Not given | Linux | Not given |
| Y1c, X3 | A stderr of 512 bytes, and of 1509 bytes | None of the OS error is kept. | Not given | Linux | Not given |

### Evidence: rollback { #ev-rollback }

| Cell or row | State | Result on `89cb6289` | Result on claustrum | VMs | Runs |
|---|---|---|---|---|---|
| B2-11 | Step 1 fails | No branch step runs. The step 1 text follows. | Not given | Not given | Not given |
| C08b | Step 1 fails in attach mode | The text reads `the worktree directory and its registration both remain`. | Not given | Not given | Not given |
| C08 | Attach mode, step 2 | No git call runs. | Not given | Not given | Not given |
| (no cell) | The order of the entries in step 1 | Not sorted. Measured against both references. | Same rule | Linux ext4, macOS APFS | Not given |
| (no cell) | Steps 1 to 3 and their texts | `f6010b97` and `90fca6e6`. | Same wordings | Windows | Not given |
| (no cell) | Step 4 | It follows `89cb6289`. | Same rule | Not given | Not given |
| (no cell) | The causes of a failed delete on Windows | Open handle. Process with its working directory in the leaf. Running executable. ACL that denies the delete. File name with a trailing dot. | Not given | Windows | Not given |
| (no cell) | The OS error text on Linux and macOS | Both references gave the same text, for example `permission denied`. | Same text | Linux, macOS | Not given |
| A14, A14f, A14b | A failed placement of the index | The registration text. | Not given | Linux (A14, A14f), macOS (A14, A14b) | Not given |
| X5 | A failed placement, attach mode | `the worktree registration remains; remove it by hand before retrying (RemoveAll <registration name>: <OS error>)` | Equal | Linux, macOS | Not given |
| X11, Y5 | Step 3 fails too (Y5: attach mode) | The registration text alone. | Equal | Linux | Not given |
| Z5 | A failed read-tree with a registration of mode 0500 | The same clause. The branch stays. | Equal | macOS | Not given |
| Z6 | A `timeout` frame of a deadline that expired during the checkout | The same clause. | Equal | macOS | Not given |
| Z9b | A registration whose `gitdir` record is relative | The same path. | Equal | macOS | Not given |
| Z11a, Z11b | The registrations directory has mode 0600 | The same path. | Equal | macOS | Not given |
| Z16, B0a | The record is rewritten to `/nonexistent/.git`, with a read-tree that fails | The entry is removed. | Equal | Linux, macOS | Not given |
| B7a, B7b | The state of Z16 in attach mode (B7a) and with a `worktreeRoot` (B7b) | The entry is removed. | Equal | Linux, macOS | Not given |
| B1 | The record names a live sibling worktree | The entry is removed. | Equal | Linux, macOS | Not given |
| Z18, B0b | The `.git` file of the leaf names a directory outside the repository | The entry is removed. The outside directory stays. | Equal | Linux, macOS | Not given |
| Z18r | The same | The same. | Equal | Linux | Not given |
| B2 | The `.git` file of the leaf names the registration `w9` of a sibling | `w1` goes and `w9` stays. | Equal | Linux, macOS | Not given |
| B3 | The same, and the record of `w9` names the leaf | `w1` goes and `w9` stays. | Equal | Linux, macOS | Not given |
| B5 | The `.git` file of the leaf is removed | The entry is removed. | Equal | Linux, macOS | Not given |
| B9 | A daemon `GIT_DIR` of X, and `GIT_COMMON_DIR` of X too | After the failed read-tree the registration that git made in X is gone. `baseRepo` has no `worktrees` directory. The frame is the plain failed checkout. | Equal | Linux, macOS | Not given |
| B9b | A daemon `GIT_DIR` alone | The same. | Equal | Linux, macOS | Not given |
| B6 | A wrapper renames `w1` to `w1x` and makes a new empty directory `w1` | `89cb6289` removes the empty `w1`. | claustrum removes no directory. The frames are equal. | Linux, macOS | 2 each |
| B6b | The wrapper renames `w1` to `w1x` and the registration `w9` of a live sibling to `w1` | `89cb6289` removes that directory with the files of the sibling. | claustrum removes no directory. The frames are equal. | Linux, macOS | 2 each |

### Evidence: `branchKept` rows { #ev-branchkept }

| Cell or row | State | Result on `89cb6289` | Result on claustrum | VMs | Runs |
|---|---|---|---|---|---|
| C02, C04, C05, C09, C10, B2-10 | The rollback check finds commits that no other ref reaches | The failure frame ends with `"branchKept":true` after `errorCode`. | Puts the member after `sourceBranch` and `branch` | Not given | Not given |
| C05 | A failed leaf rmdir | The branch part follows the rmdir text after `; `. | Same rule | Not given | Not given |
| (no cell) | Branch and reflog after a failed checkout | `f6010b97`: they always go. `89cb6289`: if another ref reaches the tip, they go. Otherwise they stay. | The branch step decides | Not given | Not given |

### Evidence: copy step { #ev-copy }

| Cell or row | State | Result on `89cb6289` | Result on claustrum | VMs | Runs |
|---|---|---|---|---|---|
| (no cell) | The rules of the manifest and the next three rules | `f6010b97`, except the rows named below. | Same rules | macOS | Not given |
| (no cell) | Version parse, opening rules, counts, batches and error arms | `f6010b97`: re-checked. | Same rules | Linux | Not given |
| (no cell) | The Windows batch budget | `f6010b97`: measured. | Same rule | Windows | Not given |
| (no cell) | The prefix rule for one glob segment, with its case folding. The literal form after a leading `/`. | `f6010b97`: measured. | Same rule | Linux, macOS, Windows | Not given |
| (no cell) | The glob after a leading `/`. `build//`, `build//a.txt`, `BUILD//`, and a lone `\` that ends the first segment. | `f6010b97`: measured. | Same rule | Linux, Windows | Not given |
| (no cell) | The other `//` and `\` rows | `f6010b97`: measured. | Same rule | Linux | Not given |
| I15c, I15d | Runtime-state paths in the full scan | `f6010b97` drops them. | Same rule | Linux, macOS | Not given |
| D16 | The nested-repository rule in the full scan, old scan forced, git 2.25.1 | `f6010b97`: the rule holds. | Same rule | Linux | Not given |
| A03 to A05, A14, A16 to A18 | Directory paths that the file batches also print | `f6010b97` sends the same paths. | Same rule | Linux, macOS | Not given |
| D16 | A nested repository, which ends in `/` | `f6010b97` sends the same paths. | Same rule | Linux, macOS | Not given |
| C02 to C05, I15c | A Claude runtime-state path | `f6010b97` sends the same paths. | Same rule | Linux, macOS | Not given |
| C02, C03, C05, D23, I14b, I15d | A root `.claude/` that holds only `worktrees` | `f6010b97`: no pathspec. | Same rule | Linux, macOS | Not given |
| Cl_anydepth | The same | `f6010b97`: no pathspec. | Same rule | Windows | Not given |
| (no cell) | Batch budget on Linux and macOS | Every value from 131 070 to 131 073 fits the directory batches. | 131 072 | Not given | Not given |
| (no cell) | Batch budget on Windows | A one-byte bisection pins 24 576 from both sides. A command line of 32 412 characters started and one of 36 012 characters did not. | 24 576 | Windows | Not given |
| (no cell) | The temporary file name | A 27-byte prefix, as in the argv of `f6010b97`. A random decimal suffix of 8 to 10 digits, in both daemons. | Same rule | Not given | Not given |
| (no cell) | The `.claude/` pass names the same children | `f6010b97`: measured. | Same rule | Linux, macOS, Windows | Not given |
| (no cell) | The case rule of the `.claude/` pass | `f6010b97`: measured. | Same rule | Linux | Not given |
| (no cell) | The split points of the `.claude/` batches, to the byte. The largest rows had 2 400 children on Windows and 30 000 on Linux. | `f6010b97`: measured. | 130 985 and 24 489 bytes | Linux, Windows | Not given |
| (no cell) | `git config -z --list` before each batch | `f6010b97` makes that call before each batch too. | Same rule | Not given | Not given |
| (no cell) | The `.claude/` listing | `19f30c46` and `90fca6e6` alike. | Same rule | Not given | Not given |
| (no cell) | The nine runtime-state names and both boundary cases | `19f30c46` and `90fca6e6`. `19f30c46` copies eight of the nine. | Same rule | Not given | Not given |

## Not measured

Each line is a statement that the source text labels "not measured" or "from the code".

- The place of `branchKept` after `sourceBranch` and `branch` is claustrum's choice (not measured).
- Whether the line of a private group in `/etc/passwd` is found by uid or by name is not measured.
- claustrum uses the effective gid. No capture told the real and the effective gid apart (not measured).
- A file of accounts that cannot be read makes the group shared (not measured).
- A `/etc/passwd` line with fewer than 6 or more than 8 fields is skipped (not measured).
- A `/etc/group` line with other than 4 fields is skipped (not measured).
- A line with a non-numeric uid or gid is skipped (not measured).
- Two identical group lines for the user pass (not measured).
- A group member is also trimmed of trailing spaces. No other field is trimmed (not measured).
- A user known only to a Linux NSS source, such as LDAP, counts as shared (not measured).
- On Windows without `worktreeRoot`, claustrum keeps the volume name and any 8.3 short name as sent. Neither is measured.
- The case rule of the `<p>` text with `worktreeRoot` is not measured.
- The order of the `baseRepo` test, after the `worktreePath` spelling and before the two-level test, is not measured.
- The order of the three checkout tests on create is not measured.
- The cases of the root `/` refusal that the error table gives as not measured stay unmeasured.
- A tab or a carriage return at an end of a stale record is not measured. claustrum cuts them.
- A relative stale record with a `baseRepo` that is sent through a symlink is not measured. claustrum counts from the resolved entry directory.
- Windows is not measured for the stale entry step of `89cb6289`. From the code: claustrum keeps its earlier step there.
- A `worktrees` directory that holds entries of other names and none of this name is not measured. claustrum answers the text of test 1 there, from its rule.
- Windows is not measured for the four registration tests. claustrum runs none of them there.
- A leaf path that fails test 1 and test 2 together is not measured. claustrum answers test 1.
- A `worktreeRoot` is measured in cell P-b only. Attach mode is measured in cell P-a, and in a plain layout in cells S-e and Se0 (Linux VM). claustrum runs the same tests in every layout.
- A relative `<path>` in these layouts is not measured.
- A link target named `worktrees` in another letter case on a case-blind volume is not measured, beyond row D-4.
- A leaf with no `.git` file that claustrum can read is not measured. claustrum does not refuse it.
- A registrations directory or an entry whose stat fails with another error than "does not exist" is not measured. Two examples are a registrations directory with no search permission and a file in its place.
- From the code: in those states the `commondir` read fails too, and claustrum answers the text of test 1.
- From the code: a registrations directory that is a symlink to nothing counts as absent (not measured).
- From the code: when the daemon has `GIT_DIR` of X, git answers X as the git directory, and claustrum finds the entry in X.
- If `rev-parse --absolute-git-dir` gave no answer before the add, the four tests do not run (not measured). From the code: claustrum puts the index into `<path>`, and it reads the record right before it places the index.
- The place of the stale entry test against the deadline test is not measured. From the code: claustrum runs it before the deadline test.
- A parent directory that does not resolve gives the cleaned path as sent (not measured).
- On Windows the texts keep their earlier spelling, and `89cb6289` is not measured there.
- The raw bytes of the record are not measured. The byte compare is inferred from rows I07a and I07b.
- Row I07a is not measured on Linux.
- A relative record beyond cells Z9a, P-k and S-f is not measured.
- Cell R1, a create with `worktree.useRelativePaths`, is not measured on Linux. Git 2.43 on the Linux VM cannot write relative records.
- A state that fails test 1 and a later test is not measured.
- No cell measured divergence D24 at the placement of the index.
- The atime of the index file is not measured. claustrum leaves it alone.
- A temporary mtime that is a whole microsecond is not measured. claustrum keeps a whole microsecond.
- A file system with no group or mode is not measured.
- A placement that fails after a drain overrun is not measured. claustrum answers it as the failed placement.
- If the caller `timeoutMs` expired before the placement failed, the case is not measured. claustrum answers it as the failed placement.
- An entry that appears at the index between the remove and the second create is not measured. claustrum then fails the placement with `openat <registration name>/index: file exists`.
- A failed set of the mtime is not measured, and no state that reaches it is known. claustrum fails the placement with `chtimesat <registration name>/index: <OS error>`.
- Windows is not measured for the placement of the index. claustrum moves the temporary file there.
- The same undo texts after the two other `timeout` frames are claustrum's choice (not measured).
- If the registrations directory cannot be opened in the rollback, the text holds `open <path>: <OS error>` (not measured).
- If the open of the accepted directory fails, the identity comes from a stat of the path (not measured).
- If git gave no answer to `rev-parse --absolute-git-dir`, the rollback takes the directory that the `.git` file names (not measured).
- Windows is not measured for the delete of the registration in the rollback.
- That `unicode.IsPrint` fits every measured payload is an inference, not a proof.
- A symlinked `worktreeRoot` is not measured for `--work-tree`. On Windows the resolved form is not measured.
- A full scan in which every path is dropped, so that no check-ignore call runs, is not measured.
- Whether `f6010b97` counts the `-c` options in the batch budget was not measured.
- The batch values are a fit to the measured batch counts. They are not values read from the reference.
- The `.claude/` batches on macOS were not measured.
- Whether the reference refuses a copy through a symlinked intermediate component is unmeasured. No probe has a fixture for it.

## History

- `branch` was added by `19f30c46`.
- `existingBranch` was added by `19f30c46`, as the `git.worktree_create.existingBranch` capability.
- `timeoutMs` was added by `4534d86`.
- Since `f6010b97`, the git-directory trust check runs on `baseRepo`, after the managed-worktrees test and before anything is created.
- When there is no `worktreeRoot`, `7d193f89` confines the worktree to inside the repository.
- Since `7d193f89`, the add fails with `worktree_add_failed` on an unborn HEAD.
- On `f6010b97`, the branch and its reflog always go after a failed checkout. Since `89cb6289`, they go in one case: another ref reaches the tip.
- `90fca6e6` skips the Claude runtime state in both passes of the copy step. `19f30c46` copies eight of the nine names. It drops `worktrees` as well.
- `90fca6e6` passes the ref name as the start point of the fallback add. `f6010b97` passes the full id.
- `90fca6e6` prints no graft-file deprecation hint. `f6010b97` and claustrum can print it.
- An earlier version of the protocol reference said the opposite of the copy rule for a filename that `git ls-files` C-quotes. It called this a reference limitation reproduced for parity. It was neither.
