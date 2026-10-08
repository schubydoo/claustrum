# git.worktree_create

`git.worktree_create` makes a linked git worktree for a new or an existing branch.
It runs on Linux, macOS and Windows, and the checks differ by system (see [Platform differences](#platform-differences)).
The method checks the paths, runs `git worktree add --no-checkout`, checks out the files, and then copies the ignored files that the repository lists.
A client must send `branchName` even with `existingBranch`, and `worktreeRoot` is refused on Windows.
A failure after the add rolls the worktree back, and a failure frame can end with `"branchKept":true`.

This page holds the contract. The rules of each step are on the [rules page](git-worktree-create-steps.md). The measured cells and rows are on the [evidence page](git-worktree-create-evidence.md).
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

In the texts, `<p>` and `<leaf>` are `worktreePath` in a fixed spelling (see [Step 9](git-worktree-create-steps.md#step-tests)).
`<repo>` is `baseRepo` as sent. An absent `baseRepo` gives the empty string, so a space comes before the semicolon.
The full texts of the root refusals (ancestor, repository, checkout, symlink loop) and of the other `mkdir_failed` frames are in the [error-string catalog](../PROTOCOL.md#error-string-catalogue).
A text that this page shortens with `…` is quoted as the source text quotes it.

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
| `git worktree add timed out after <n>ms (…)` | `timeout` | The deadline from `timeoutMs` fires (see [Step 10](git-worktree-create-steps.md#step-deadline)). |
| `the registration <entry> is not the folder that was tested after the add` | inside the `(checkout)` frame | Linux, macOS. The directory at the entry path is not the directory that the tests accepted. claustrum's own text. |
| `the registration <entry> has no gitdir record that names this worktree` | inside the `(checkout)` frame | Linux, macOS. The four tests did not run and the record cannot be read or names another path. claustrum's own text. |
| `<frame>; and the undo could not finish for <leaf>: …` | unchanged | A rollback step fails (see [Rollback](#rollback)). |

## What the method does, in order

1. [Read the parameters](git-worktree-create-steps.md#step-params). `branchName` is required, also with `existingBranch`. If `baseRepo` is absent, the daemon uses its cwd repository. On Windows, any `worktreeRoot` is refused before any location test.
2. [Check the repository](git-worktree-create-steps.md#step-repository). A `baseRepo` under a managed-worktrees marker answers `nested_base_repo`. A git directory that the trust check refuses answers `worktree_add_failed`, and a directory with no repository answers `not_a_repo`. Every git step ignores replace objects and grafts.
3. [Check the path without `worktreeRoot`](git-worktree-create-steps.md#step-path). `worktreePath` must be absolute, carry no `..` component, sit strictly under `baseRepo` and not exist. Each failure answers `unsafe_path`. An empty `worktreePath` answers `mkdir_failed`.
4. [Check the path with `worktreeRoot`](git-worktree-create-steps.md#step-root). The worktree goes exactly two levels under the root, at `<worktreeRoot>/<directory>/<name>`. Tests on the root, its ancestors, its owner and write access, and the `<directory>` level refuse with `unsafe_path`. A `.git` entry in the chain refuses too.
5. [Create the parent directories and the marker](git-worktree-create-steps.md#step-parents). The daemon creates the missing parent directories. With `worktreeRoot`, it also writes a `.claude-managed-worktrees` marker. An existing marker stays. A marker that cannot be created stops the create with `mkdir_failed`, and so does a junction on Windows.
6. [Drop a stale registration](git-worktree-create-steps.md#step-stale). On Linux and macOS, the daemon removes a stale entry in `<baseRepo>/.git/worktrees` in one case. It must be the only stale entry, and it must hold no `locked` file. The remove is best effort, and no error of it reaches the frame.
7. [Pick the start commit](git-worktree-create-steps.md#step-source). A non-empty `sourceBranch` picks the start commit from the local branch or from `origin`. If neither resolves, the new branch starts at HEAD. The response echoes `sourceBranch` as sent.
8. [Run `git worktree add`](git-worktree-create-steps.md#step-add). The daemon runs `git worktree add --no-track --no-checkout`, or attaches to `existingBranch`. If the attach add fails, a fallback add creates `branchName`. A failed add answers `worktree_add_failed` and runs no git call after it.
9. [Test the new registration](git-worktree-create-steps.md#step-tests). On Linux and macOS, four tests, the stale entry test and the record test check the new registration. Each refusal is `unsafe_path`, runs no checkout and rolls nothing back. On Windows none of these tests run.
10. [Test the deadline](git-worktree-create-steps.md#step-deadline). A fired `timeoutMs` answers `timeout` with a parenthetical for the phase. The deadline does not kill the add or the copy step. It kills the checkout.
11. [Check out the files and place the index](git-worktree-create-steps.md#step-checkout). `read-tree` checks out the files, and the index goes into the registration. A failed checkout or a failed placement answers `worktree_add_failed` with `(checkout)`, and the rollback runs.
12. [Copy the ignored files](git-worktree-create-steps.md#step-copy). Two passes copy ignored files: the manifest pass (`.worktreeinclude`) and the `.claude/` pass. The copies are best-effort, and a failure never fails the request. A caller `timeoutMs` that expires before the copies end still fails it.
13. [Answer](git-worktree-create-steps.md#step-result). On success the response has `path`, `sourceBranch` and `branch`. On a failure after the add, the rollback runs first and the failure frame follows.

The [text rule for git messages](git-worktree-create-steps.md#step-textrule) applies to every step that quotes git.

## Rollback

After a successful add, a rollback runs in three cases. They are a failed checkout, a failed placement of the index and each `timeout` frame.
It runs four steps.
After a failed add, a smaller rollback runs. It makes no git call, and it removes the leaf in one case: the leaf is an empty directory.
The texts that the steps append, the cases of step 2 and the list of what stays are in the [full rollback section](git-worktree-create-steps.md#rollback).

| Step | What it does |
|---|---|
| 1 | Deletes the entries at the top of the leaf, one at a time. It stops at the first entry that cannot be deleted. |
| 2 | Deletes the registration. Then it runs the branch step on the branch that the call created. |
| 3 | Removes the leaf directory, which is now empty. |
| 4 | If the branch stays, appends the text of the branch step. |

## Differences from the reference

The reference build is `89cb6289` unless a row names another build.

| D-number or label | claustrum | `89cb6289` | Link |
|---|---|---|---|
| D24 | In the rollback, a directory that replaced the new registration is kept. No directory is removed. | Removes the empty `w1` (cell B6) or the directory with the files of a sibling (cell B6b). | [D24](../DIVERGENCES.md#d24) |
| D2 | The home guard refuses an entry path that is the home directory or holds it, in the stale entry step and in the rollback. | This section does not state it. | [D2](../DIVERGENCES.md#d2) |
| D5 | With `-git-timeout` opted in, a killed copy call loses what that call gives the pass. The response stays `{"success":true}`. | This section does not state it. | [D5](../DIVERGENCES.md#d5) |
| D19 | On Windows, a junction at `.claude` or `.claude\worktrees` fails the parent step with `mkdir_failed`. | For a junction `<P>\J` above them, equal frames after 3 git calls (cells J-b-wt-pj, J-b-wt-pJ). | [D19](../DIVERGENCES.md#d19) |
| claustrum's choice (not measured) | `branchKept` comes after `sourceBranch` and `branch`. | No measured frame with `branchKept` has `sourceBranch` or `branch`. | |
| | The `rev-parse --show-toplevel` pair runs once with `baseRepo` as sent, before the record test refusal. | The pair runs twice with `baseRepo` as the resolved path (cells T2, T10). No frame differs. | |
| D24 | The text of a failed placement is claustrum's own: `the registration <entry> is not the folder that was tested after the add`. | No cell measured the placement. | [D24](../DIVERGENCES.md#d24) |
| claustrum's own | The text of a failed placement is claustrum's own: `the registration <entry> has no gitdir record that names this worktree`. | Not stated. | |
| claustrum's own | When an intermediate component of its destination is a symlink, a copy is dropped. | Whether the reference refuses the same is unmeasured. | |
| not measured | The batch budget of the copy step counts the `-c` options as zero. The values 131 072, 24 576, 130 985 and 24 489 are a fit to measured batch counts. | Whether `f6010b97` counts the `-c` options was not measured. | |
| claustrum's own | claustrum compares the record of a stale entry by text. | The cells equal in the frame and on the disk. A tab or a CR at an end of the record, and a relative record with a `baseRepo` sent through a symlink, are not measured. | |
| not measured | If `rev-parse --absolute-git-dir` gave no answer, claustrum runs none of the four tests. | Not measured. | |
| not measured | On Windows, claustrum runs no registration test and moves the temporary index. | Not measured on Windows. | |

## Platform differences

| Behavior | Linux | macOS | Windows |
|---|---|---|---|
| `worktreeRoot` | Supported. | Supported. | Refused before any location test. |
| Spelling refusal (trailing dot or space, colon) | Not stated. | Not stated. | Windows only. It comes before containment. |
| `<p>` of "already exists" | Parent directory symlinks resolved, last name kept, cleaned. | Same as Linux. | On-disk letter case of each existing component. Volume name and 8.3 short name as sent (not measured). |
| Modes of created directories (0755 above the leaf, 0777 leaf, umask applies) | Yes. | Yes. | Not stated. |
| Mode 0700 for directories above the leaf with `worktreeRoot` | Yes. | Yes. | Not stated. `worktreeRoot` is refused. |
| Junction in the path | Not stated. | Not stated. | Fails the parent step with `mkdir_failed`. |
| Ancestor test, root-chain tests, root owner and write tests | Yes. | Yes. | Not stated. `worktreeRoot` is refused. |
| Group-write test | `/etc/passwd` and `/etc/group`. A user known only to an NSS source such as LDAP counts as shared. | The same flat files. The directory service does not count. A stock user has no `/etc/passwd` line, so every group-writable root is refused. | Not stated. |
| Passwd name with a leading space | Does not match the user. | Not stated. | Not stated. |
| Stale entry step | Runs with `locked` test and root. | Same. | Earlier step. It compares the directory that holds the record path with `worktreePath`. No `locked` test. No root. |
| Four registration tests, stale entry test, record test | Run. | Run. | The four tests do not run. The record test is off. The stale entry test: not stated. |
| Case of loose and packed refs for `sourceBranch` | Not stated. | A loose ref matches in any case, also under `Origin`. A packed ref does not. | A loose ref matches in any case. A packed ref does not. |
| `--work-tree` | New worktree, symlinks resolved. | Same as Linux. | Path as sent. |
| Index placement | New file made exclusively. | Same as Linux. | The temporary file is moved. If the move fails, there is no index and the create succeeds. |
| Group of the new index file | Group of a setgid directory, else the primary group. | Group of the registration directory. | Not stated. |
| Exec error for a killed checkout with empty stderr | `signal: killed` (the text gives it as the general case). | `signal: killed` (the text gives it as the general case). | `exit status 1`. |
| Delete of the registration in the rollback | Through a root. Direct-child and identity tests. Failed delete skips the branch step. | Same as Linux. | Deletes the resolved path. No tests. No text. The branch step runs. |
| Cause of a failed delete in the rollback | Go OS error text, for example `permission denied`. | Same as Linux. | Measured causes: an open handle, a working directory in the leaf, a running executable. An ACL that denies the delete and a trailing dot were causes too. |
| Batch budget of the copy step | 131 072 bytes. | 131 072 bytes. | 24 576 bytes. |
| Batches of the `.claude/` pass | 130 985 bytes. | 130 985 bytes. | 24 489 bytes. |
| Handles of the leaf and its parent | Held open until the answer. | Held open until the answer. | Held open until the answer, with delete sharing. |
| NFC and NFD spelling of the directory | Not stated. | Git records NFC. NFC sent in NFD is refused. NFD sent in NFC succeeds. | Not stated. |

## Further reading

- The rules of each step: [rules page](git-worktree-create-steps.md).
- The measurements: [evidence page](git-worktree-create-evidence.md).
- The other methods and the frames: [protocol reference](../PROTOCOL.md).
