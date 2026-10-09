# git.list_branches: measurement record

This page is a record, not reading material. Its text moved here from the protocol reference on 2026-10-09. Only its cross-references changed in the move. It holds the detailed rules of `git.list_branches` and the measurements behind them, and it names the reference build of a measurement where the old text did. A new measurement of the method goes into this page.

To use the method, read [git.list_branches](../protocol/git-list-branches.md).

`{path}` → `{"isRepo":true,"branches":[…sorted…]}`
- Non-repo → `{"isRepo":false,"branches":[]}`.
- A refused daemon `GIT_CONFIG_COUNT` changes the answers of this method (see [The daemon's own git
  environment](../PROTOCOL.md#the-daemons-own-git-environment)).
- A `path` inside a managed worktrees directory answers
  `{"isRepo":false,"branches":[]}` before any git call. That is a path beneath
  `.claude/worktrees`, or beneath a directory that holds a
  `.claude-managed-worktrees` marker. Linux, macOS and Windows VMs. claustrum also
  runs this test on `baseRepo` (not measured).
- A `path` that is not empty and does not resolve answers
  `{"isRepo":false,"branches":[]}` before any git call. The rule is the one that
  `git.status` applies to `baseRepo`. Examples are a missing `path` (Linux, macOS
  and Windows VMs) and `<dir>\missing\..` (Windows VM). On Windows a `path` with a
  junction before its last component, such as `<dir>\<junction>\T`, does not
  resolve (Windows VM).
- Since `f6010b97` the git-directory trust check runs on `path`. A refused git
  directory answers `-32603` with the refusal text. "No repository" answers
  `{"isRepo":false,"branches":[]}`. A `GIT_COMMON_DIR` in the daemon's environment
  turns the check off for this method. See
  [Git-directory trust check](../PROTOCOL.md#git-directory-trust-check).
- The daemon reads stdout only. A broken-ref `for-each-ref` warning must not become
  a branch.
- A failing `for-each-ref` → `-32603 exit status 128`. With opt-in D5 it can carry
  `signal: killed` (see [D5](../DIVERGENCES.md#d5)).
