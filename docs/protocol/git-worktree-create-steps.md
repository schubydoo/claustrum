# git.worktree_create: the rules of each step

This page holds the rules of each step of [`git.worktree_create`](git-worktree-create.md) in full.
The contract is on the [contract page](git-worktree-create.md). The measurements are on the [evidence page](git-worktree-create-evidence.md).

### Step 1: Read the parameters { #step-params }

- `branchName` is required. It is required with `existingBranch` too. If it is missing, the answer is `-32602 branchName is required`.
- The repository is `baseRepo`, not `path`. When `baseRepo` is absent, the daemon uses its cwd repository.
- On Windows, any `worktreeRoot` is refused before any location test. The refusal has `errorCode:"unsafe_path"` on create.
- `path` and each undo text quote `worktreePath` exactly as sent.
- A refused daemon `GIT_CONFIG_COUNT` changes the answers of this method. See [The daemon's own git environment](../PROTOCOL.md#the-daemons-own-git-environment).

Evidence: [Paths without `worktreeRoot`](git-worktree-create-evidence.md#ev-path).

### Step 2: Check the repository { #step-repository }

- A `baseRepo` that sits under a managed-worktrees marker is refused with `errorCode:"nested_base_repo"`. The same frame answers a `baseRepo` that fails claustrum's own trust-root test, as in `git.worktree_remove`. No git runs and nothing is created.
- This refusal comes before the check of the daemon's `GIT_CONFIG_COUNT`.
- Then the git-directory trust check runs on `baseRepo`. It runs after the managed-worktrees test and before anything is created. See [Git-directory trust check](../PROTOCOL.md#git-directory-trust-check).
- A refused git directory answers `{success:false,error:<text>,errorCode:"worktree_add_failed"}`. "No repository" answers `not_a_repo`. Either way the daemon creates no worktree directory, no entry and no branch.
- When the daemon environment carries `GIT_COMMON_DIR`, the text is `git worktree add failed: cannot locate the repository's git directory: <text>`.
- If the resolved repository is not git, the answer is `{success:false,error:"not a git repository",errorCode:"not_a_repo"}`. The daemon tests this before the add.
- This method ignores replace objects and grafts. If `refs/replace` or `info/grafts` name others, the checkout still holds the real blob and the real commit. Ancestry is still the real ancestry.
- claustrum runs every git step of this method with `GIT_NO_REPLACE_OBJECTS=1` and `GIT_GRAFT_FILE=<null>`, except `rev-parse --absolute-git-dir`. See [Hardened git calls](../PROTOCOL.md#hardened-git-calls).

Evidence: [Paths without `worktreeRoot`](git-worktree-create-evidence.md#ev-path) (replace objects and grafts).

### Step 3: Check the path without `worktreeRoot` { #step-path }

Without `worktreeRoot`, the worktree must sit inside the repository.
After the repository test, `worktreePath` must be absolute, carry no `..` component, sit strictly under `baseRepo`, and not exist.
Each failure answers `{success:false,error:"refusing to create worktree: …",errorCode:"unsafe_path"}`.
The texts are in the [error table](git-worktree-create.md#refusals-and-errors).
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

Evidence: [Paths without `worktreeRoot`](git-worktree-create-evidence.md#ev-path).

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
claustrum runs them in this order. The order of the checkout tests on create is not measured (see [Not measured](git-worktree-create-evidence.md#not-measured)).

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

Evidence: [Root tests](git-worktree-create-evidence.md#ev-root), [Group and account files](git-worktree-create-evidence.md#ev-account).

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

Evidence: [Parents, marker and junctions](git-worktree-create-evidence.md#ev-parents).

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

Evidence: [Stale registration step](git-worktree-create-evidence.md#ev-stale).

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

Evidence: [Start commit](git-worktree-create-evidence.md#ev-source).

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

Evidence: [Attach, fallback and failed add](git-worktree-create-evidence.md#ev-add).

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
The [registration tests table](git-worktree-create-evidence.md#ev-tests) gives the result for each layout.
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

Evidence: [Registration tests](git-worktree-create-evidence.md#ev-tests), [Stale entry test and record test](git-worktree-create-evidence.md#ev-record), [Spelling of paths](git-worktree-create-evidence.md#ev-spelling).

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

Evidence: [Deadline](git-worktree-create-evidence.md#ev-deadline).

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

Evidence: [Checkout](git-worktree-create-evidence.md#ev-checkout), [Index file](git-worktree-create-evidence.md#ev-index), [Failed placement](git-worktree-create-evidence.md#ev-placefail).

### Step 12: Copy the ignored files { #step-copy }

`git worktree add` checks out tracked files only, so the daemon then seeds the new worktree.
The copies are best-effort, and a failure never fails the request.
A caller `timeoutMs` that expires before the copies end still fails it (see [Step 10](#step-deadline)).
Two passes copy: the manifest pass and the `.claude/` pass.
This section groups the rules by subject:

- What the manifest is: [The manifest file](#copy-manifest).
- Which scan runs: [Choice of scan](#copy-choice), [The full scan](#copy-full) and [The directory scan](#copy-dirscan).
- How a pattern opens a directory: [Opening rules](#copy-open), [Limits](#copy-limits) and [Dot directories](#copy-dot).
- Budgets: [Batches and budgets](#copy-batches).
- The second pass: [The `.claude/` pass](#copy-claude).
- What both passes skip and how they write: [What both passes skip](#copy-skip) and [How the files are written](#copy-write).
- Failures: [Failures](#copy-fail).

#### The manifest file { #copy-manifest }

- `.worktreeinclude` sits at the repository root and uses `.gitignore` syntax. It is an include filter over the git-ignored set.
- The daemon copies an untracked file in one case: the manifest names it and git's standard rules ignore it. A manifest match that git does not ignore is not copied.
- The manifest must be a regular file. A symlink or a directory copies nothing and runs no git.
- An empty regular file still runs `git version` and the scan, and it copies nothing.
- Git reads a temporary copy of the manifest bytes.

#### Choice of scan { #copy-choice }

- If the manifest is a regular file, the daemon runs `git version`. The call has no `-c` option and no `-C`. It runs in the working directory of the daemon, with the environment of the daemon unchanged.
- The first `git version ` in the output counts, even after other text. A digit must follow it.
- Git 2.32.0 or later gets the directory scan. Older git gets the full scan, and so does output that does not parse.
- A non-zero exit also gets the full scan, even with valid output.
- Major and minor compare as numbers. A number too large for an int counts as very large.
- Text after the numbers is ignored, so `2.32.0.windows.1` gets the directory scan.

#### The full scan { #copy-full }

- It runs `git ls-files --others --ignored --exclude-from=<manifest copy> -z -- ':(exclude).claude/worktrees'`.
- `git check-ignore --stdin -z` then keeps the paths that git's standard rules ignore.
- A runtime-state path (see [What both passes skip](#copy-skip)) or a nested repository does not go to check-ignore.
- If every path is dropped, no check-ignore call runs.
- If either call fails, nothing is copied.
- The full scan searches every ignored directory.

#### The directory scan { #copy-dirscan }

- It first lists the ignored entries with `git ls-files --others --ignored --exclude-standard --directory`. If the listing fails, nothing is copied.
- Every ignored file in the listing is a candidate.
- The daemon searches an ignored directory in one case: the manifest opens it.
- An any-depth pattern thus does not reach a file in a closed directory. When git ignores `build/`, `*.txt` does not copy `build/a.txt`.
- If the ignored files of the listing total more than 1 MiB, the full scan runs instead. Each file counts as its path length plus 3 bytes.
- A nested repository inside an ignored directory is not copied, and no empty directory is left for it.

#### Opening rules: when a manifest pattern opens a directory { #copy-open }

| Pattern form | What it opens | Examples |
|---|---|---|
| A literal name of one segment, or `**/` and then one literal | Each listed directory that has the name as any of its segments. Case does not matter. | `build` opens `build`, `sub/build` and `build/x`. |
| One segment after a leading `/` | Each listed directory whose first segment matches it as a glob. The prefix rule does not apply here. A `\` escapes the next character. Case does not matter. | `/build/` opens `build`, not `sub/build`. `/a?b/` opens `aXb` and `a_b`, not `ab`. `/ab\q/` opens nothing. |
| One glob segment without a leading `/` (a glob segment holds `*`, `?`, `[` or `\`) | It opens by its literal prefix. The prefix ends before the first of these characters. The segment opens each listed directory whose path starts with the prefix. The rest of the segment is not used. Case does not matter. | `b*` opens `build`, not `sub/build`. `su*` opens `sub/build`. `a?b/` and `a[_]b/` open `ab`, `a b` and `a/c`. `sub\build/` opens `sub`, `sub/build` and `subbuild`. `AB\q/` opens `ab`. |
| `**/` and then a literal and more segments | It matches the rest of the pattern from any segment of a listed directory. | `**/sub/build/` opens `sub/build`. |
| A pattern of two or more segments | Each listed directory that it matches segment by segment, and the listed parents of that directory. | None given. |
| In such a pattern, a segment that ends in a lone `\` | It matches any name. Git reads that line as `sub/x/`. This holds for the first, a middle and the last segment. | `sub\/x/` opens each listed directory of one segment and each listed `<name>/x`. |
| An even run of `\` at the end of a segment | A literal `\`. A run of three acts like a run of one. Directly after `**/`, such a segment opens nothing. | `zz\\/x/` opens only `zz\/x`. |
| A `//` in a pattern | It leaves an empty segment. The empty segment matches no name. `build//` alone opens no dot directory. | `build//`, `build//a.txt` and `BUILD//` open `build`, not `sub/build` or `a/x/build`. `//x` opens nothing. `a//b` opens only a listed `a`. |
| One segment without a leading `/` that starts with `*`, `?`, `[` or `\` | It has an empty prefix. It opens no directory. Neither does `**/` and then a glob. A negation and a comment open nothing too. | None given. |
| A `\` in a pattern of two or more segments | It does not cut a prefix. | `sub/b\q/` opens only `sub`. |

Two more rules do not fit a row:

- Git's own match still reads a `\` as an escape. The manifest goes to git unchanged. So `a\ b/` opens `ab`, but git copies from `ab` only the files that another manifest line matches.
- Git does not read `\` as a separator.

#### Limits of the opening rules { #copy-limits }

- Only the first 256 counted patterns can open a directory. A negation counts. Blank lines, comments and patterns over a cap do not count.
- A line of only tabs and spaces is blank. So is a line that is empty after one leading and one trailing `/` are removed, such as `//`.
- If a pattern has more than 1024 bytes after one leading and one trailing `/` are removed, it opens nothing. If it has more than 32 segments, it opens nothing too.

#### Dot directories { #copy-dot }

- An any-depth pattern also opens the listed dot directories. A pattern of one segment is any-depth, unless it starts with `/`. A pattern that starts with `**/` is any-depth too.
- The 256 count does not apply to this. At most 128 dot directories open, in listing order.
- The root `.claude/` takes a place unless an explicit pattern matches it.
- The flag never opens these 14 names, in any case. They take no place.
- The first seven are `.angular`, `.cache`, `.dart_tool`, `.gradle`, `.next`, `.nuxt` and `.parcel-cache`.
- The last seven are `.pnpm-store`, `.svelte-kit`, `.terraform`, `.tox`, `.turbo`, `.venv` and `.yarn`.
- An explicit pattern still opens a skipped directory, or one past the 128th. A dot directory that an explicit pattern matches takes no place.
- If a pattern names `.claude/worktrees/`, that directory still does not open.
- The root `.claude/` opens by the rules above. It does not become one pathspec. Its children take its place in the directory batch, in the order of the directory read. `worktrees` is left out in any case. This is the list of the `.claude/` pass below. So a root `.claude/` that holds only `worktrees` adds no pathspec.

#### Batches and budgets { #copy-batches }

The directory scan sends its candidates to `git ls-files --exclude-from=<manifest copy>` in batches.

- Files and directories never share a batch.
- A directory pathspec has no trailing `/`.
- A batch fills in listing order. When the next path does not fit, a new batch starts.
- Each argument after the `-c` options costs its length plus 3 bytes. That covers the fixed `ls-files` arguments, the `--exclude-from` argument and the paths.
- The `-c` options do not count in claustrum.
- The temporary file name starts with a 27-byte prefix. A random decimal suffix follows it.
- A failed batch is skipped, and the other batches are still copied.

The budget of one call:

| Item | Linux and macOS | Windows |
|---|---|---|
| One `ls-files` call of the directory scan, at most | 131 072 bytes | 24 576 bytes |
| The pathspecs of one call of the `.claude/` pass, at most (the fixed arguments cost 87 bytes) | 130 985 bytes | 24 489 bytes |

The `.claude/` pass uses the same budget as the directory batches.

After the batches:

- Only the paths from the directory batches go to `git check-ignore --stdin -z`. It keeps the paths that git's standard rules ignore. The file candidates are copied without it.
- If it fails, the directory paths are dropped, and the file candidates are still copied.
- Three kinds of directory path do not go to check-ignore: a path that the file batches also print, a nested repository (which ends in `/`), and a Claude runtime-state path. If no path is left, no check-ignore call runs.

#### The `.claude/` pass { #copy-claude }

- `.claude/` is copied separately, with no manifest entry.
- A second pass runs `git --literal-pathspecs ls-files --others --ignored --exclude-standard -z --` with one pathspec for each child of `.claude/`, such as `.claude/settings.json`.
- It leaves out `worktrees` in any case. If no other child exists, the pass runs no git.
- The pathspecs go into batches in the order of the directory read, one git call for each batch. The budget is in [Batches and budgets](#copy-batches).
- Before each batch, the daemon runs `git config -z --list`, as it does before most hardened calls.
- A failed batch is skipped, and the other batches still copy.
- The pass copies what git lists, minus the exclusions in [What both passes skip](#copy-skip). A `.claude/` the repository git-ignores is thus seeded into the new worktree.
- A `.claude/` that is merely untracked is not, because that view cannot see it.
- `.claude/worktrees/` is always skipped, because that is where session worktrees live.
- The listing is limited to the repository-root `.claude/`, so a nested one is reached only by the manifest pass.
- The `.claude/` pass has no manifest precondition. It runs git on every create where `.claude/` holds a child other than `worktrees`.

#### What both passes skip { #copy-skip }

- Claude runtime state is skipped by both passes. The names are `scheduled_tasks.json`, `scheduled_tasks.lock`, `routines/.state`, `worktrees`, `checkpoints`, `mailbox`, `agent-registry.json`, `first-run` and `assistant-daemon-state.json`.
- They match as whole path components, directly under `.claude/`, case-insensitively. `.claude/Checkpoints/` is thus dropped, while `.claude/mailboxes/` and `.claude/nested/mailbox/` are both copied.
- The daemon skips symlinks.

#### How the files are written { #copy-write }

- A filename that `git ls-files` C-quotes, with a tab, a quote, a backslash or a non-ASCII byte, IS copied. Both passes use `-z` and split on NUL.
- The copies do not preserve the source mode. The daemon creates them 0666-subject-to-umask, so an executable arrives non-executable and a `0400` source is widened. This matches the reference.
- Treat the manifest as a way to name configuration, not secrets or scripts.
- Destination containment on both passes is claustrum's own. Every copy resolves its destination component by component inside the new worktree.
- When an intermediate component is a symlink, the copy is dropped. So a link checked out into the worktree cannot carry a copy outside it.
- A `..` component cannot occur, because `git ls-files` never prints one.
- Treat the containment as claustrum hardening, not parity.

#### Failures { #copy-fail }

With an opted-in `-git-timeout` (D5) that kills a git call (off by default):

- A killed listing loses the pass, and a killed batch loses that batch.
- A killed `git check-ignore` loses the whole full scan, or the directory paths of the directory scan.
- A killed `git version` selects the full scan.
- The reply is still `{"success":true}`, so the loss is silent and wire-invisible.
- Each git call has its own deadline, so the manifest copy can succeed while `.claude/` files are lost, or the other way round.

Evidence: [Copy step](git-worktree-create-evidence.md#ev-copy).

### Step 13: Answer { #step-result }

On success the response is `{"success":true,"path":"<worktreePath>","sourceBranch":"<b>","branch":"<b>"}` (see [Response](git-worktree-create.md#response)).
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

Evidence: [Text rule](git-worktree-create-evidence.md#ev-textrule).

## Rollback { #rollback }

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

Evidence: [Rollback](git-worktree-create-evidence.md#ev-rollback), [`branchKept` rows](git-worktree-create-evidence.md#ev-branchkept).

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

