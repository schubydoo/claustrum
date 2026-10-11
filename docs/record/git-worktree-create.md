# git.worktree_create: measurement record

This page is a record, not reading material. Its text moved here from the protocol reference on 2026-10-08. Only its cross-references changed in the move. Its measurements are against the reference build `89cb6289` and the earlier builds that it names. It holds the detailed rules of `git.worktree_create` and the measurements behind them. A new measurement of the method goes into this page. The tests of the repository pin these cases.

To use the method, read [git.worktree_create](../protocol/git-worktree-create.md).

`{baseRepo,branchName,worktreePath[,sourceBranch][,existingBranch][,worktreeRoot][,timeoutMs][,shadowDirOff]}` → `{"success":true,"path":"<worktreePath>","sourceBranch":"<b>","branch":"<b>"}`
- The checkout has a filter rule that follows `5fd08069`. See
  [Git filters of the checkout](#git-filters-of-the-checkout).
- A repository config that the daemon does not accept answers `errorCode`
  `worktree_add_failed`, and nothing is made. See
  [Repository config refusals](../PROTOCOL.md#repository-config-refusals) and the
  [record of the refusals](git-config-refusals.md).
- A failure frame ends with `"branchKept":true` after `errorCode` when the rollback
  check finds commits that no other ref reaches. Rows C02, C04, C05, C09, C10 and
  B2-10 of `89cb6289` show it. A check that does not finish, and a skipped name, add
  no member. The table in [The branch step](../PROTOCOL.md#the-branch-step) gives each case.
  The member is only ever true, and absent otherwise. No measured frame with it has `sourceBranch` or `branch`. claustrum
  puts it after them. That is claustrum's choice (not measured).
- The repo is `baseRepo`, not `path`. When `baseRepo` is absent, the daemon uses
  its cwd repo.
- A refused daemon `GIT_CONFIG_COUNT` changes the answers of this method (see "The
  daemon's own git environment").
- On Windows a junction at a directory between `baseRepo` and the leaf fails the
  parent step: `{"success":false,"error":"failed to create parent directory: <path
  of the junction> is not a directory","errorCode":"mkdir_failed"}`. Nothing
  is created. Measured against `f6010b97` on a Windows VM for `.claude` and
  `.claude\worktrees` (rows JCR1 and JCR2). For a junction `<P>\J` above them,
  `89cb6289` and the claustrum build of this change answer equal frames after 3 git calls
  (Windows VM, cells J-b-wt-pj and J-b-wt-pJ). See
  [`DIVERGENCES.md`](../DIVERGENCES.md) → D19.
- Missing `branchName` → `-32602 branchName is required`. It is required even when
  `existingBranch` is given.
- `branch` was added by `19f30c46`. It is the branch the worktree checks out. It
  follows `sourceBranch` on the wire and is present on every success. It holds the
  created `branchName`, or the attached `existingBranch`. It is absent on failure.
- `existingBranch` was added by `19f30c46`, as the
  `git.worktree_create.existingBranch` capability. It attaches the worktree to an
  already-existing local branch instead of creating one. When
  `show-ref --verify refs/heads/<existingBranch>` resolves, the add uses that
  branch as the commit-ish (`worktree add --no-checkout <path> <existingBranch>`,
  with no `-b`), and `branch` is `<existingBranch>`. When `existingBranch` is empty
  or names no branch, the `-b <branchName>` new-branch path runs and `branch` is
  `<branchName>`. A miss falls back silently rather than erroring. This is measured
  against `19f30c46`.
- If the attach add fails, the daemon runs
  `worktree add --no-track --no-checkout -b <branchName> <path> [<sha>]` and goes
  on. The fallback add gets the start commit that `sourceBranch` resolved to, as
  the new-branch path does, and the checkout then reads that commit. With no start
  commit, the add gets no start point, and the checkout reads
  `refs/heads/<branchName>`. `branch` is `<branchName>`. A rollback after this
  fallback runs the branch step on `<branchName>`, because the call created it. The natural trigger
  is an `existingBranch` that is checked out in `baseRepo`. With
  `existingBranch:"main"` and no `sourceBranch`, the reply is
  `{"success":true,"path":"<p>","sourceBranch":"main","branch":"<branchName>"}`.
  If the fallback add also fails, the reply is
  `{success:false,error:"git worktree add failed: <fallback text> (attaching to the existing branch <b> was refused first: <attach text>)",errorCode:"worktree_add_failed"}`.
  Measured against `f6010b97` and `90fca6e6` on a macOS VM. There, `90fca6e6`
  passes the ref name as the start point and `f6010b97` the full id. The new
  branch lands on the same commit, and claustrum passes the full id.
- No fallback runs when the failed attach add deleted the leaf or put a new
  directory in its place. The daemon tests the leaf's identity for that. The reply
  is then `git worktree add failed: <attach text>`, and the rollback of a failed
  add runs. Measured against `f6010b97` and `90fca6e6` on macOS and Windows VMs.
  On Linux and Windows VMs both references held the leaf and its parent open
  during the create. On ext4 their replacement leaf got a new inode in 12 of 12
  runs. Claustrum holds both open until it answers, so a replacement cannot reuse
  the inode of the leaf. On Windows both claustrum handles share delete. Under the
  handles of `f6010b97`, the leaf can be renamed. A rename of the parent fails with
  "Access is denied." while the leaf is inside it. Claustrum takes the leaf's
  identity from its handle.
- Since `f6010b97` the git-directory trust check runs on `baseRepo`, after the
  managed-worktrees test and before anything is created. A refused git directory
  answers `{success:false,error:<text>,errorCode:"worktree_add_failed"}`. "No
  repository" answers `not_a_repo` as below. Either way the daemon creates no
  worktree directory, no entry and no branch. When the daemon's environment
  carries `GIT_COMMON_DIR`, the text is
  `git worktree add failed: cannot locate the repository's git directory: <text>`.
  See [Git-directory trust check](../PROTOCOL.md#git-directory-trust-check).
- The resolved repo is not git → `{success:false,error:"not a git
  repository",errorCode:"not_a_repo"}`. The daemon tests this before the add.
- This method ignores replace objects and grafts. The checkout holds the real blob
  and the real commit, and ancestry is the real ancestry, even when `refs/replace`
  or `info/grafts` name others. This is measured against `f6010b97`. claustrum
  runs every git step of this method with `GIT_NO_REPLACE_OBJECTS=1` and
  `GIT_GRAFT_FILE=<null>`. See
  [Hardened git calls](../PROTOCOL.md#hardened-git-calls).
- By default, with no `worktreeRoot`, `7d193f89` confines the worktree to inside
  the repository. After the repo
  test, `worktreePath` must be absolute, carry no `..` component, sit strictly
  under `baseRepo`, and not already exist. Each failure is
  `{success:false,error:"refusing to create worktree: …",errorCode:"unsafe_path"}`:
  `"<p> is a relative path; …"`, `"<p> contains a \"..\" component; …"`, `"<p> has a
  component Windows reads as a different name (trailing dot or space, or a colon); …"`
  (Windows only, before the containment check), `"<p> is not
  inside the repository <repo>; session worktrees are only created and removed under
  <repository>/.claude/worktrees"`, and `"<p> already exists, and a new worktree is
  only ever created in a fresh directory"`. On Linux and macOS, in the `<p>` of
  that last text the parent folder of the leaf has its symlinks resolved,
  the last name is kept, and the path is
  cleaned (`89cb6289` and claustrum, cells T9, U1a to U1d, U2a to U2c and
  U3a2, Linux and macOS VMs). Take Windows without `worktreeRoot`.
  There the `<p>` of that last text has the on-disk letter case of each component
  that exists (row W15, both references). claustrum keeps the volume name and any
  8.3 short name as sent. Neither is measured. The case rule with `worktreeRoot` is
  not measured. `<repo>` is `baseRepo` as sent. An
  absent `baseRepo` gives the empty string, so a space comes before the semicolon
  (`f6010b97`, Linux and macOS VMs). The recommended location is
  `<repo>/.claude/worktrees/<id>`, but the enforced rule is only containment in the
  repo. An empty `worktreePath` is `{success:false,error:"failed to create parent
  directory: \"\" does not name a directory",errorCode:"mkdir_failed"}`. The daemon
  creates the parent directory before the add, so a nested path succeeds on a fresh
  repo. A `worktreePath` with a trailing slash, `//` or `/./` succeeds too. `path`
  and each undo text below quote `worktreePath` exactly as sent. Measured against
  `f6010b97` and `90fca6e6` on Linux and macOS VMs. On Linux and macOS a directory
  that the create makes above the leaf asks for mode 0755, and the leaf asks for
  0777. The umask applies. An existing directory keeps its mode. A directory that
  the call made before a later failure stays. The texts of this step name the repo
  with its symlinks resolved. Measured against `f6010b97` on Linux and macOS VMs.
  Both VMs ran umask 0000, which tells a leaf request of 0777 from 0775.
- `worktreeRoot` is the `external_root` capability. When the client supplies
  `worktreeRoot`, the worktree is placed OUTSIDE the repository, at
  `<worktreeRoot>/<directory>/<name>`, exactly two levels under the root. On
  Windows this capability is gated off. Any `worktreeRoot` is refused before any
  location test with `"refusing to {create,remove} worktree: <root> cannot be used:
  a custom worktree location is not supported on Windows hosts yet"`. The
  `errorCode` is `"unsafe_path"` on create, and there is none on remove. On unix
  the in-repo containment above is replaced by the tests below. A refusal among
  them has `errorCode:"unsafe_path"`. The error table gives the code of each
  failure. `worktreeRoot` and `worktreePath` must be absolute and
  `..`-free, and `worktreePath` must sit exactly two levels under the root
  (`"<p> is not <worktree location>/<directory>/<name> beneath <root>"`).
  A `worktreeRoot` that is the file system root `/` is refused too:
  `{"success":false,"error":"refusing to create worktree: / is a filesystem root; choose the worktree location by its absolute path, without \"..\", beneath the filesystem root","errorCode":"unsafe_path"}`.
  `89cb6289` sends that frame after the excludes read and the repo test, 3 git calls, and nothing
  is created (Linux VM, cells R1n, R1u and R1r, and macOS VM, cell R1a). The error
  table gives the cases that are not measured.
  `baseRepo` must also be absolute and carry no `..` component. The refusal uses
  the `worktreePath` texts and names `baseRepo` as sent. An absent `baseRepo` is a
  relative path with the empty string as its name. `f6010b97` sends this refusal
  after the repo test, and nothing is created (rows C7, C8 and C9 on Linux and
  macOS VMs). claustrum tests it after the `worktreePath` spelling and before the
  two-level test, as on remove. That order is not measured. Three tests refuse a
  root in a checkout of the repository. claustrum runs them after the two-level
  test, as on remove. That order is not measured on create. The first test
  compares the cleaned root with the cleaned `baseRepo` by whole components. Then
  the daemon makes the git calls that "Hardened git calls" lists. The second test
  refuses a root that leads into the git top level of `baseRepo` or into the main
  checkout. The third refuses a root in a linked worktree of the repository. A
  root that holds the repository passes, and so does `<B>/Tx` beside `<B>/T`. The
  three tests come before the root-chain tests, and nothing is created. The error
  table gives the texts. Measured against `f6010b97` and `89cb6289` on Linux and
  macOS VMs (rows K1 to K4, K6 to K9, E2, W3, W4 and Y11a to Y11c). After the
  git calls and before the second test, the ancestor test judges the directories
  above the root. A directory owned by a user other than the daemon's user or
  uid 0 refuses the create. So does a directory with the other-write bit, or
  with the group-write bit and a shared group, when the sticky bit is off. The
  error table gives the texts, the rules and the limits. Measured against
  `f6010b97` and `89cb6289` on Linux and macOS VMs (rows K1, G1 to G41b, Y11d,
  T7 and T7c). G42 and the macOS round 2 rows are measured against `89cb6289`
  only. Next, the
  daemon resolves the symlinks of the root. It tests each directory from `/` down
  to the root for a `.git` entry, top first. These root-chain tests come before
  the tests of the root's owner and write access. The symlinked `<directory>`
  test comes next, then the `<directory>`-level tests, then the non-empty and
  existing-leaf tests.
  The error table above gives each text. A `.git` entry or a symlink loop refuses
  the create with `errorCode:"unsafe_path"`. Measured against `f6010b97` on Linux
  and macOS VMs, texts and order. A Linux VM measured the `<directory>` file
  test after the writable-root and foreign-owner tests, and the search test after
  the writable-root test. Linux and macOS VMs measured the symlinked `<directory>`
  test before the `<directory>`-level tests. The root must be owned by
  the daemon's user (`"<root> is owned by uid <o>, not by you (uid <u>); …"`). The root must not be
  writable by its group or by every user on the host
  (`"<root> is writable by <who> (mode <perm>); … chmod go-w"`). The group-write
  bit counts unless the group is the private group of the daemon's user. For
  this test the answers follow only the flat files `/etc/passwd` and
  `/etc/group`. Members and primary-group users in the macOS directory service
  do not count. A directory-service group alone is not enough. A stock macOS
  user has no `/etc/passwd` line, so there every group-writable root is
  refused. A group that counts as shared on a root with mode 0777 gives the
  `<who>` text "its group and every user on this host". These texts, the four
  tests and the line format below were measured against `f6010b97` on Linux and
  macOS VMs, except where a line says otherwise.
  - A private group passes four tests:
    - Its gid is the daemon's gid.
    - `/etc/passwd` has exactly one line with that gid as its primary gid. That
      line must be the daemon user's line. Only Linux VMs measured this.
      Whether it is found by uid or by name is not measured.
    - At least one `/etc/group` line has that gid, and every such line has the
      name of that user.
    - No such line lists a member other than that user.
  - The account-file lines:
    - In both files a line that starts with `#` is skipped.
    - A `/etc/passwd` line with 6, 7 or 8 fields is read.
    - A passwd name with a leading space does not match the user (Linux only).
    - A `+name` line is not skipped.
    - A leading space and a trailing CR around a group member are trimmed, and
      an empty member is ignored.
  - Not measured:
    - claustrum uses the effective gid, and no capture told the real and the
      effective gid apart.
    - A file that cannot be read makes the group shared.
    - A `/etc/passwd` line with fewer than 6 or more than 8 fields is skipped.
    - A `/etc/group` line with other than 4 fields is skipped.
    - A line with a non-numeric uid or gid is skipped.
    - Two identical group lines for the user pass.
    - A member is also trimmed of trailing spaces. No other field is trimmed.
    - A user known only to a Linux NSS source, such as LDAP, counts as shared.

  The `<directory>` level must not be a symlink. Unless it is already marked, it
  must also start out empty (`"<dir> already exists, is not marked as a worktree
  directory, and holds other files (for example \"<name>\"); … must start out
  empty …"`). These two tests take `<directory>` from the cleaned
  `worktreePath`, so for `R/proj/w1/` the refusal names `R/proj`. With
  `worktreeRoot`, the "already exists" refusal also quotes the cleaned path, for
  example `R/cp/w1` for `R/cp/w1/`. Measured against `f6010b97` and `90fca6e6`
  on Linux and macOS VMs. With a `worktreeRoot` behind a symlink, `89cb6289`
  names both paths with the link resolved, and so does claustrum (cells U3a and
  U3a2, Linux and macOS VMs). The
  stale entry test below gives the spelling rule. With `worktreeRoot`, each directory that the create
  makes above the leaf asks for mode 0700. That covers every missing directory
  from the highest one down to `<directory>`. After the parent step and before
  the add, the daemon writes a 285-byte `.claude-managed-worktrees` marker at
  the `<directory>` level if no marker exists. An existing entry of that name
  keeps its content and its mode. A marker that cannot be created for another
  reason stops the create with `errorCode:"mkdir_failed"`, and the leaf is not
  made. A failed add keeps the marker. A directory that the call made before a
  later failure stays. Measured against `f6010b97` on Linux and macOS VMs.
  Independently, a `baseRepo` that itself sits under a managed-worktrees marker
  is refused
  `{success:false,error:"baseRepo is inside a managed worktrees directory …",errorCode:"nested_base_repo"}`.
  The same frame answers a `baseRepo` that fails claustrum's own trust-root test, as
  in `git.worktree_remove`. No git runs, and nothing is created. Linux and macOS VMs
  measured that against `f6010b97` in rows G1c, G2c and G4c. The Windows VM measured
  it in round 1 row C1 and in round 2 rows K1, D1, D2, D4, J1 and J3. The refusal
  comes before the check of the daemon's `GIT_CONFIG_COUNT` (round 1 row C1 P1 on the
  Windows VM).
- Other failure → `{success:false,error:"git worktree add failed: <text>",errorCode:"worktree_add_failed"}`.
  `<text>` is git's stderr, made by the text rule below. On `f6010b97` and on
  claustrum the text can start with git's graft-file deprecation `hint:` lines.
  Both set `GIT_GRAFT_FILE`, and git prints the hint when it reads that file.
  `90fca6e6` prints no hint. One `90fca6e6` example is
  `"git worktree add failed: Preparing
  worktree (new branch 'dup') fatal: a branch named 'dup' already exists"`. A
  failed add answers this frame even when the caller `timeoutMs` expired during the
  add.
- After a failed add, the daemon runs no git call. It removes the leaf only if the
  leaf is an empty directory. Files that the failed add left in the leaf stay, and
  so do a registration and a branch that it made. A retry at the same path then
  answers `unsafe_path` "already exists". Measured against `f6010b97` and
  `90fca6e6` on macOS and Windows VMs, with a stub git that failed after it wrote
  into the leaf. The common failures, such as a branch that already exists, leave
  the leaf empty, so a retry with a fresh branch succeeds. If the failed add
  replaced the leaf's parent and made a new empty leaf in it, the new leaf stays.
  The daemon tests the identity of the parent that it holds open for that. Both
  references kept the new leaf after a failed attach add, in 20 of 20 runs on
  Linux and macOS VMs.
- The text rule makes every git text in the failure frames of this method. That
  covers the add failure, each part of the attach-fallback frame, the checkout
  failure and the checkout that the deadline killed. Measured against `f6010b97`
  and `90fca6e6` on a macOS VM:
  1. Take stderr only. stdout is not quoted.
  2. Keep the first 512 bytes.
  3. Drop every byte that is not valid UTF-8, anywhere in the text. The cap comes
     first, so the bytes of a rune that the cap cut go too.
  4. Replace each rune that is not printable with one space, with no collapsing.
     So `\r\n` gives two spaces. The measured set includes `\t`, NUL, `\x7f`,
     U+0085, U+00A0, U+200B and U+2028. claustrum uses Go's `unicode.IsPrint`,
     which fits every measured payload. That is an inference, not a proof.
  5. Trim the spaces at both ends.
  6. If the result is empty, use the exec error. That is `exit status 128` for a
     git that failed with that status, and `signal: killed` for a killed checkout.
     On Windows the killed checkout gives the kill's own error, `exit status 1`.
- `timeoutMs` is caller-supplied and was added by `4534d86`. It is a per-request
  deadline in milliseconds over the add, the checkout and the copy step. An absent
  `timeoutMs`, or `0`, arms no deadline, so the reply is byte-identical to the
  default. A fired deadline answers
  `{success:false,error:"git worktree add timed out after <n>ms (…)",errorCode:"timeout"}`.
  The rules below were measured against `f6010b97` and `90fca6e6` on a macOS VM,
  except where a rule names another build.
  - The deadline does not kill `git worktree add`. The daemon waits for the add to
    exit. If the add failed, the reply is the add-failure frame above, not
    `timeout`. If the add succeeded and the deadline expired, the parenthetical is
    `deadline expired before the checkout started`. The reply thus waits for the
    add. In attach mode the daemon runs the fallback add first, as described
    above, and then tests the deadline.
    On Linux and macOS the four registration tests and the record test below
    come before this deadline test (`89cb6289`: row D-9 on a macOS VM for test 1,
    cell P-f on Linux and macOS VMs for the record test).
  - The deadline kills the checkout, a `read-tree`. The parenthetical is then
    `deadline expired during the checkout): <text>`. The text rule above makes
    `<text>` from the stderr of the killed git. With `f6010b97` and with
    claustrum, git can print graft-file `hint:` lines on stderr before the kill,
    and `<text>` then holds them. `90fca6e6` prints no hint.
  - The deadline does not kill the copy step that seeds the new worktree. The
    daemon lets the step finish and then tests the deadline. If it expired, the
    parenthetical is `deadline expired after the checkout finished`. The reply
    thus waits for the copy step.
  - The checkout git can exit 0 while a descendant it left, such as a smudge or
    hook filter, holds one of the daemon's output pipes. The daemon caps that
    drain at a fixed ~5s from git's exit, independent of `timeoutMs`. That cap is
    measured against `4534d86`. At the cap the daemon reaps the descendant. The
    checkout then counts as finished, so the copy step runs and the deadline test
    after it decides. When `timeoutMs` exceeds the drain, the reply is
    `{success:true}`. When it does not, the reply is the `timeout` frame with
    `deadline expired after the checkout finished`.
  - These timeouts roll back as a failed checkout does. See the failed-checkout
    and undo rules below. No rollback runs `git worktree remove`, so the
    `.git/worktrees/` directory stays. A retry at the same path then succeeds with a
    new `branchName`. It succeeds with the same one only when the rollback deleted
    the branch.
  - This is caller-activated. It is distinct from the operator-global
    `-git-timeout` divergence (D5), and it applies to create only, not to
    `git.worktree_remove`.
- A non-empty `sourceBranch` picks the start commit of the new branch. The rules
  below were measured side by side against `f6010b97` on Linux, Windows and
  macOS. Here `s` is the value as sent.
  - The daemon resolves two candidates. L is `refs/heads/<s>^{commit}`. R is
    `refs/remotes/origin/<s>^{commit}`. Each is plain string concatenation, so a
    revision suffix such as `feat~1` works, and a slash name such as `team/feat`
    works. A symbolic ref is followed, so `s = "HEAD"` reads
    `refs/remotes/origin/HEAD`. An annotated tag object in the origin ref is peeled
    to its commit. An origin ref that holds a missing object, a tree or garbage,
    or a local ref that holds a missing object, counts as absent. `s` is tried
    only under `refs/heads/` and `refs/remotes/origin/`.
  - Only the remote-tracking namespace `origin` is read. The remote's
    configuration does not matter. The daemon fetches nothing, so a stale tracking
    ref is used as it is.
  - On a case-insensitive file system, a loose ref matches `sourceBranch` in any
    letter case, and a packed ref does not (Windows, macOS). On macOS a loose ref
    under `Origin` also counts.
  - Only one candidate resolves → that one.
  - Both resolve, and `git merge-base --is-ancestor <L> <R>` exits 0 (L equals R or
    is behind it) → R.
  - Otherwise the daemon runs `git merge-base <R> <L>`. If it fails (no common
    history, a shallow cut, a missing parent commit) → R.
  - Otherwise the daemon runs `git diff --quiet --no-ext-diff --no-textconv
    --submodule=short <merge base> <L> -- ':(top,icase).claude'
    ':(top,icase).mcp.json'`. Exit 0 → L. Any other exit → R. So a local change to
    the repo-root `.claude` entry or the root `.mcp.json`, in any letter case,
    selects R. So does a diff that errors. The test is the net tree difference from
    the merge base. It is not the commit history, and it is not a comparison with
    R. A nested `sub/.claude`, a `.claude.json` or a `.claudex` directory does not
    count. Uncommitted state in `baseRepo` does not count.
  - The add gets the chosen commit's full id:
    `worktree add --no-track --no-checkout -b <branchName> <path> <sha>`. The
    checkout reads the same id. The new branch's reflog therefore reads
    `branch: Created from <sha>`. The branch gets no upstream configuration.
  - `sourceBranch` is echoed exactly as sent, whichever candidate was used.
  - Neither candidate resolves → the same result as an omitted `sourceBranch`.
  - `existingBranch` is resolved after these steps. When it attaches, the chosen
    commit goes unused, and `sourceBranch` is still echoed.
  - These git steps have no deadline of their own. With `-git-timeout` (D5) opted
    in, a killed step counts as a failed step under the rules above.
- `sourceBranch` omitted or `""` → origin is not read. A non-empty `sourceBranch`
  that resolves to nothing reads both candidates first. In each of these cases the
  add gets no start point, so the new branch starts at HEAD, and its reflog reads
  `branch: Created from HEAD`. The daemon echoes the current branch from
  `rev-parse --abbrev-ref HEAD`. On a detached HEAD the result omits
  `sourceBranch`. That is measured against `f6010b97` with `sourceBranch` omitted
  and with a `sourceBranch` that resolves to nothing. Since `7d193f89`, on an
  unborn HEAD the add fails with `worktree_add_failed`.
- A failed checkout (`read-tree`) fails the request with
  `{success:false,error:"git worktree add failed (checkout): <text>",errorCode:"worktree_add_failed"}`.
  The text rule above makes `<text>`. Where git prints graft-file deprecation
  `hint:` lines, the text starts with them on `f6010b97` and on claustrum.
  `90fca6e6` prints no hint. Apart from the hint, the frame matches `90fca6e6`
  byte for byte. The daemon empties the new directory and removes the worktree's
  registration in the main repository's `.git/worktrees/`. Then it runs the branch
  step on the branch that the call created, and then it removes the empty
  directory. The rollback's git calls are those of the branch step. The `.git/worktrees/`
  directory itself stays, so the first linked worktree's failed checkout leaves it
  empty. With a linked worktree as `baseRepo`, that worktree's own entry stays,
  and a retry at the same path fails the same way. In attach mode the attached
  branch is kept. These end states are measured against `f6010b97`. There the
  branch and its reflog always go. Since `89cb6289` they go only when another ref
  reaches the tip of the branch.
- The checkout is `read-tree -u --reset --no-recurse-submodules <rev>`, after the
  hardening `-c` options and `-c core.splitIndex=false -c core.commitGraph=false`.
  It runs with the new worktree as its working directory, and it passes no `-C`.
  `--git-dir` names the git dir of `baseRepo`, and `--work-tree` names the new
  worktree. For a linked-worktree `baseRepo`, the git dir is that worktree's own
  admin dir. The daemon gets it with `rev-parse --absolute-git-dir` before the add.
  The index goes to a file in a new temporary directory, named by `GIT_INDEX_FILE`.
  Its config precursor, `--git-dir=<git dir> config -z --list`, runs in
  the new worktree too. Measured against `f6010b97` and `90fca6e6` on a Windows VM.
  On Linux and macOS, `--work-tree` names the new worktree with its symlinks
  resolved, as on `f6010b97` and `89cb6289` (rows D02, I01c, I01e and CSc). Those
  rows are creates without `worktreeRoot`. A symlinked `worktreeRoot` is not
  measured. On Windows claustrum passes the path as sent. The resolved form is not
  measured there.
- Before the add, on Linux and macOS, the daemon looks for stale entries of
  `<baseRepo>/.git/worktrees`. The step runs after the test that `worktreePath`
  does not exist. An entry is stale if its `gitdir` record names the `.git` of
  the new worktree. The daemon removes a stale entry only if it is the only
  stale entry of the folder and holds no `locked` file. The A cells are those of
  `89cb6289` on a Linux VM with git 2.43 and a macOS VM with git 2.50, 2 runs
  each. So are the S cells. Below,
  `<L>` is the real path of `worktreePath`.
  - claustrum compares by text. This rule is claustrum's own fit of the cells
    below. The record loses blanks and newlines at both ends.
    A relative record counts from the entry folder. The path is then cleaned,
    and no symlink of it is resolved. The other side is `worktreePath` with the
    symlinks of its existing part resolved, plus `/.git`.
  - With one stale entry, `89cb6289` removes it for these records: `<L>/.git`
    with and without
    a newline (cells A3 and A4), `<L>/.git/` (cells A1 and A2), `<L>/.git//`
    (cell A5), `<L>/.git/.` (cell A5b), `<L>/.git` with a blank and a newline
    (cell A5c), and the relative path `../../../.claude/worktrees/w1/.git` (cell
    A7). The name of the entry does not count: an entry `old9` goes (cell A8).
    Cell P-p below has the record of cell A1.
  - With two or three stale entries, `89cb6289` removes none of them: `old9`
    and `w1` (cell A13), `old8`, `old9` and `w1` (cell S1), `old8` and `old9`
    (cell S2), and two entries whose records differ in their spelling (cell S6).
    With a stale entry and a second stale entry that holds a `locked` file, both
    stay (cells A13b and S5). With one stale entry beside an entry that is not
    stale, the stale entry goes (cells S3 and S4). It goes too beside a regular
    file, a folder with no `gitdir` record or an empty folder (cells S7, S7b and
    S7c).
  - `89cb6289` keeps the entry for the records `<L>` and `<L>/`, which have no
    `.git` part (cells A6 and A6b). It keeps the entry of a live worktree at
    another path (cell A11). A record that spells the path through a symlink
    stays for a request with the real path (cell A12b). A record with the real
    path goes for a request through a symlink (cell A12c).
  - An entry that holds a `locked` file stays (cells A9 and A9b, an empty file).
  - The remove is best effort, and no error of it reaches the frame. With the
    entry at mode 0500 only `logs/HEAD` goes (cell A10). With the `worktrees`
    folder at mode 0555 the files go and the empty folder stays (cell A10b).
  - With no daemon `GIT_*` variable, the add then decides. After a removed entry
    the create succeeds with one entry `w1` (cells A1 p and A7 p). After a kept
    entry git names the new registration `w11` and the create succeeds (cells
    A9 p, A10 p, A13 p and S1 p), or the add fails with the text of git (cells
    A6 p, A9b p, A10b p and S6 p).
  - Three guards are claustrum's own. The remove is one `os.Root.RemoveAll` of
    the entry name, through a root at the `worktrees` folder. The step lists
    the entries and reads each record through that same root. An entry that is
    not a real folder is passed over. The home guard (D2) runs on the entry path
    first.
  - Not measured: a tab or a carriage return at an end of the
    record. claustrum cuts them. Not measured: a relative record with a
    `baseRepo` that is sent through a symlink. claustrum counts from the
    resolved entry folder. claustrum remembers each stale entry that stays, for
    the stale entry test below.
  - Windows is not measured for `89cb6289`. From the code: on Windows claustrum
    keeps its earlier step. It compares the folder that holds the record path
    with `worktreePath`, both with their symlinks resolved, and it has no
    `locked` test and no root.
  - claustrum equals `89cb6289` in the frame and on the disk of each A cell and
    each S cell (Linux and macOS VMs, 2 runs each).
- Right after a successful add, the daemon reads the `.git` file of the new
  worktree. git writes one line there, `gitdir: <path>`. On Linux and macOS
  claustrum runs four tests. They fit the frames and the disk of `89cb6289` on a
  Linux VM with git 2.43 and on a macOS VM with git 2.50.
  1. The folder that holds `<path>` has the name `worktrees`. If it has another
     name, the create answers `{"success":false,"error":"refusing to create
     worktree: <leaf> carries a .git file that does not name this
     repository's own worktree admin directory","errorCode":"unsafe_path"}`.
  2. The registrations directory of `baseRepo` exists, and it holds an entry with
     the last name of `<path>`. That directory is `<common git dir>/worktrees` of
     the git directory that `rev-parse --absolute-git-dir` answered before the
     add. Folder absent: the create answers `{"success":false,"error":"refusing
     to create worktree: <leaf> was not populated by git worktree
     add","errorCode":"unsafe_path"}`. `89cb6289` answers that text in rows
     B-E1, B-E3 and D-8 and in cell D8e, and `baseRepo` has no `worktrees`
     folder in each. Folder present, no entry of the name: the create answers
     the text of test 1. `89cb6289` answers that text in cell P-p below (Linux
     and macOS VMs). The step before the add removed the old entry there. Not
     measured: a `worktrees` folder that holds entries of other names and none
     of this name. claustrum answers the text of test 1 there, from this rule.
  3. The `commondir` file of that entry leads back to the common git directory.
     claustrum joins a relative value to the entry path as spelled. If the result
     is another directory, the create answers the text of test 1. A `commondir`
     file that cannot be read as a file gets that text too.
  4. The `gitdir` record of that entry can be read as a file. If it cannot, the
     create answers the text of test 1.

  After each refusal no checkout runs and nothing is rolled back. The leaf holds
  its `.git` file only, the registration stays with no `index`, and the branch
  stays. The tests come before the deadline test that follows the add. With
  `timeoutMs` 1 and an add that takes 3 s, `89cb6289` answers the refusal and keeps
  all three (row D-9, macOS VM). With a read-tree that fails, it answers the
  refusal too, and no read-tree runs (row D-2, macOS VM). The rows:
  - `<git dir>/worktrees` is a symlink to `<F>/WTREG`. git 2.50 writes the resolved
    path, `<F>/WTREG/w1`, into the `.git` file. `89cb6289` then refuses with the
    text of test 1 (rows D-1, D-5 and D-7, macOS VM). Row D-5 is a relative link. In row
    D-7 `.git` is a symlink too. git 2.43 writes the path through the link,
    `<baseRepo>/.git/worktrees/w1`. `89cb6289` then creates the worktree (rows D-1,
    D-5 and D-7, Linux VM). On the Linux VM a git wrapper that writes the resolved
    path gets the refusal (row D-11). So the two VMs differ only in what the
    `.git` file holds.
  - The link target is `<F>/alt/worktrees`. Both VMs create the worktree (row
    D-3). The `.git` file holds `<F>/alt/worktrees/w1` on the macOS VM. With the
    target `<F>/alt/WORKTREES`, the macOS VM refuses and the Linux VM creates (row
    D-4).
  - `<baseRepo>/.git` is a symlink to `<F>/GITDIR`, and the `.git` file holds
    `<F>/GITDIR/worktrees/w1`. Both VMs create the worktree (row D-6). A
    repository under `/tmp` on the macOS VM creates too (row D-14).
  - A git wrapper writes `../../x` into the `commondir` file of the registration
    after the add. `89cb6289` refuses with the text of test 1 (row D-13, Linux and
    macOS VMs).
  - `baseRepo` holds an old entry `w1` of another worktree, and git makes the new
    registration in another repository, as in cell P-c below. One file of the old
    entry is changed before the request. With the `gitdir` record as a FIFO (cell
    P-g), as an empty folder (cell P-i) or removed (cell P-j), `89cb6289` answers
    the text of test 1. With the `commondir` file as a FIFO and the record as it
    was, it answers the text of test 1 too (cell P-h). Cell P-c differs from P-h
    in the `commondir` file only, and it gets the text of the record test. So
    test 3 comes before the record. In all four cells nothing changes after the
    add: no read-tree runs, and the old `index` keeps its inode, its mtime and its
    bytes. The two FIFO cells answered in under 1 s on both VMs, so no read
    waits on a FIFO. Linux VM with git 2.43 and macOS VM with git 2.50.
  - Three more cells start from the state of cell P-c (Linux VM with git 2.43 and
    macOS VM with git 2.50). In cell P-n the `commondir` file of the old entry is
    removed. In cell P-o its `gitdir` record has mode 0000. In cell P-q the folder
    of the old entry has mode 0000. `89cb6289` answers the text of test 1 in each.
    No read-tree runs, and the old `index` does not change.
  - A git wrapper writes `gitdir: /elsewhere/worktrees/w1` into the `.git` file
    after the add. That path does not exist. `89cb6289` answers success and keeps
    the `.git` file as it is (row D-12, Linux and macOS VMs). On both VMs the
    registration that git made holds the `index`. So claustrum puts the index into
    the entry of test 2, not into `<path>`.
  - A git wrapper writes `gitdir: /elsewhere/WTREG/w1` into the `.git` file after
    the add. `89cb6289` refuses with the text of test 1 (cell P-d, Linux and macOS
    VMs). On the Linux VM `<git dir>/worktrees` is a symlink in cell P-d and a
    plain folder in cell Pd2. Both get the refusal.
  - In the layout of row D-1, a create in attach mode and a create with a
    `worktreeRoot` get the text of test 1 on the macOS VM (cells P-a and P-b).
    On the Linux VM both creates succeed, and the `index` is in the link target.
    git 2.43 writes the path through the link there, as in row D-1.
  - `baseRepo` is a linked worktree of a repository T. `89cb6289` creates the
    worktree, and its registration and its `index` are in T (cell P-e, Linux and
    macOS VMs).
  - The S cells are equal on `89cb6289` and on claustrum, in the frame and on the
    disk. A `baseRepo` that is a subfolder of a repository creates (cell S-a).
    So do a repository with a separate git directory (cell S-c) and a daemon
    `GIT_DIR` alone (cell S-d). A bare repository answers `not_a_repo` (cell S-b).
    Those four ran on Linux and macOS VMs. An attach creates, and so does an
    attach that falls back to a new branch (cells Se0 and S-e, Linux VM).
  - The daemon environment holds `GIT_COMMON_DIR` of another repository X, and
    `baseRepo` and X hold a commit with the same id. git makes the registration in
    X, the `.git` file names `<X>/.git/worktrees/w1`, and `baseRepo` has no
    `worktrees` folder.
    `89cb6289` answers the "was not populated" text of test 2 in 10 of 10 runs (rows B-E1 and B-E3,
    Linux and macOS VMs). Row D-8 on a macOS VM and cell D8e on Linux and macOS
    VMs show the same text. With different ids the add itself
    fails, and both sides answer the add-failure frame (rows B-E2 and B-E4).

  With `GIT_DIR` of X in the daemon environment too, the create succeeds, and
  git makes the registration in X. That is probe row 2 of `git.worktree_remove`
  (`89cb6289`, Linux and macOS VMs). From the code: git answers X as the git
  directory there, and claustrum finds the entry in X.

  From the code: the four tests need the git directory that git answered. If
  `rev-parse --absolute-git-dir` gave no answer before the add, claustrum runs
  none of the four tests and puts the index into `<path>`. That is claustrum's
  choice (not measured). With a `baseRepo` that is a subfolder of a repository,
  the read-tree then fails, and the create answers the failed checkout below.
  With an answer, a subfolder creates (cell S-a, Linux and macOS VMs).

  Not measured: Windows. There claustrum runs none of the four tests and puts
  the index into `<path>`. A `<path>` that fails test 1 and test 2 together is not
  measured, and claustrum answers test 1. A `worktreeRoot` is measured in cell
  P-b only. Attach mode is measured in cell P-a, and in a plain layout in cells
  S-e and Se0 (Linux VM). claustrum runs the same
  tests there in every layout. A relative `<path>` in these
  layouts is not measured. Neither is a link target named `worktrees` in another
  letter case on a case-blind volume, beyond row D-4. A leaf with no `.git` file
  that claustrum can read is not measured, and claustrum does not refuse it. A
  registrations directory or an entry whose stat fails with another error than
  "does not exist" is not measured. Two examples are a registrations directory
  with no search permission and a file in its place.
  From the code: the `commondir` read fails there too, and claustrum answers the
  text of test 1. From the code: a registrations directory that is a symlink to
  nothing counts as absent (not measured). In cell P-q the stat of the entry
  folder still answers.
- After those four tests, on Linux and macOS, the daemon tests the entry of test
  2 against the step before the add. If that step left stale entries in place and
  the entry of test 2 is one of them, the create answers
  `{"success":false,"error":"refusing to create worktree: <leaf> carries
  a .git file naming an admin entry other than the one just created for
  it","errorCode":"unsafe_path"}`. It runs no checkout and rolls nothing back.
  `89cb6289` answers that text in three cells of Linux and macOS VMs, 2 runs
  each. In each the daemon has `GIT_COMMON_DIR` of another repository, and the
  old entry `w1` of `baseRepo` has a record that names the `.git` of the new
  worktree. In cells A9 g and A9b g the entry holds a `locked` file. In cell A10 g
  it has mode 0500. The add is the last of 9 git calls of `89cb6289` there. So
  claustrum runs this test before the pair of the record test below. Cell P-c has
  an old entry with the record of another worktree, and it gets the text of the
  record test. The rule "an entry that the step before the add left" is
  claustrum's own fit of these cells. With two or more stale entries every one
  stays, and `89cb6289` answers this text where the entry of test 2 is one of
  them (cells A13 g, A13b g, S1 g, S5 g and S6 g, Linux and macOS VMs).
  claustrum answers the same frame in each of these cells. Not measured: the place of this
  test against the deadline test. From the code: claustrum runs it before the
  deadline test.
  In the four texts of these tests and of the record test, `<leaf>` is
  `worktreePath` in this spelling: the parent folder of the leaf has its symlinks
  resolved, the last name is kept, and the path is cleaned. The `already exists`
  refusal before the add names the leaf in the same way, with and without
  `worktreeRoot`. With `worktreeRoot`, the "is not marked as a worktree directory"
  refusal names the folder that holds the leaf in that way too. `89cb6289`
  names the paths so in these cells of Linux and macOS VMs, 2 runs each:
  - A symlink at `baseRepo`, with both request paths through it: the text of
    test 1 (cells T1, T5 and A12c g), the "was
    not populated" text (cell T4), the stale entry text (cell T3), the text of
    the record test (cells T2 and T10) and `already exists` (cell T9).
  - A leaf that is a symlink to a folder (cell U1a), a dangling symlink (cell
    U1b), a regular file (cell U1c) or a symlink to a file (cell U1d), in
    `already exists`. The text names the leaf, not the target of the link.
  - A path with a slash at its end, a double slash or a `/./` part: `already
    exists` (cells U2a to U2c), the text of the record test (cell U2d), "was not
    populated" (cell U2e), the text of test 1 (cell U2f) and the stale entry
    text (cell U2g).
  - A `worktreeRoot` behind a symlink, with `worktreePath` through it: the "is
    not marked" text (cell U3a), `already exists` (cell U3a2) and the text of
    the record test (cell U3b).
  `89cb6289` names the leaf as sent in the undo clause of a rollback (cells T6
  and T7), in the `path` of a success (cells T8, U2h and U3c) and in the locked
  refusal of `git.worktree_remove` (cell T12b). The T cells and the U cells ran
  on Linux and macOS VMs. claustrum answers the same frame as `89cb6289` in each
  of them. A parent folder that does not resolve gives the cleaned path as
  sent (not measured). On Windows the texts keep their earlier spelling, and
  `89cb6289` is not measured there.
- After those four tests, and before the deadline test that follows the add, the
  daemon reads the `gitdir` record of the registration of
  the new worktree. On Linux and macOS that is the entry of test 2 above, not the
  folder that the `.git` file names. It compares the record
  with `<worktreePath>/.git` byte for byte, with `worktreePath` taken after symlink
  resolution. If they differ, the create answers `{"success":false,"error":"refusing to create
  worktree: <leaf> carries a .git file naming an admin directory whose own
  record is of a different worktree","errorCode":"unsafe_path"}`, runs no checkout
  and rolls nothing back. The leaf, the entry and the branch stay. Before the answer
  it runs the `rev-parse --show-toplevel` pair with `--git-dir` and `--work-tree=<baseRepo>`
  in the git dir. In cells T2 and T10 (Linux and macOS VMs) the call log of
  `89cb6289` shows that pair twice, with `baseRepo` as the resolved path. The
  call log of claustrum shows it once, with `baseRepo` as sent. No frame
  differs. On a macOS VM both references refuse an NFC folder sent in NFD,
  because git records the path in NFC (row I07a). An NFD folder sent in NFC succeeds
  (row I07b). The raw bytes of the record are not measured. The byte compare is
  inferred from I07a and I07b. The symlink resolution is claustrum's choice: a
  `/tmp` path creates as usual on both references (row I01e). Row I07a is not
  measured on Linux. Cells P-c, P-f and P-k to P-m below measured this test on a
  Linux VM. On a Linux VM the creates of rows CSa to CSd succeeded
  on both references and on claustrum. A relative record counts from the entry.
  git writes one with `worktree.useRelativePaths`, and `89cb6289` creates with
  such a record (cell Z9a, macOS VM). Cell S-f has such records in a plain
  layout on a macOS VM with git 2.50, and `89cb6289` and claustrum both create.
  With the path sent in another letter case (`<F>/t` for `<F>/T`), both answer
  this refusal. With the path sent in an NFD spelling, both answer it too. The
  frame and the disk are equal in all three. The check is off for a relative
  `worktreePath`, and on Windows. That is claustrum's choice.
  Cell P-c shows which record counts (`89cb6289`, Linux VM with git 2.43 and macOS
  VM with git 2.50). The
  daemon environment holds `GIT_COMMON_DIR` of another repository X, with equal
  commit ids. `baseRepo` holds an old entry `w1` of another worktree, with its own
  `index`. git makes the new registration in X, and the record there names the new
  worktree. The record of the old entry does not. `89cb6289` answers this refusal
  and runs that pair. Nothing changes after the add, and the old `index` keeps its
  bytes. With no `worktrees` folder in `baseRepo` the answer is the "was not
  populated" text of test 2 (row B-E1).
  Cell P-f is the state of cell P-c with `timeoutMs` 1 and an add that takes 3 s.
  `89cb6289` answers this refusal there, not the `timeout` frame. Nothing is
  rolled back: the leaf with its `.git` file and the branch stay. So this test
  comes before the deadline test, as test 1 does (row D-9). Cell P-f ran on a
  Linux VM with git 2.43 and a macOS VM with git 2.50.
  Three more cells change the old record of cell P-c, and `89cb6289` answers this
  refusal in each (Linux VM with git 2.43 and macOS VM with git 2.50). In cell P-k
  the record is the relative path `../../../gone/w1/.git`, which leads to the old
  worktree. In cell P-l it is an empty file. In cell P-m the record is as in P-c,
  and the old entry holds a `locked` file. Nothing changes after the add, and the
  old `index` keeps its inode, its mtime and its bytes. Cells P-g, P-i and P-j
  against P-k and P-l show the split. A record that cannot be read as a file gets
  the text of test 1. A record that was read and names anything else gets this
  text.
  Cell P-p changes the old record of cell P-c (Linux VM with git 2.43 and
  macOS VM with git 2.50). The record holds `<worktreePath>/.git/`,
  with a slash at its end and no newline. On `89cb6289` the old entry is gone
  before the add. A system call trace on the Linux VM shows that the daemon
  removes the entry, not git, and that it does so before the add. On the macOS
  VM the entry is gone at the reply. `baseRepo` then has a `worktrees` folder
  with no entry `w1`, and `89cb6289` answers the text of test 1. claustrum
  removes that entry in the step before the add and answers the text of test 1
  (Linux and macOS VMs).
  Not measured: a relative record beyond cells Z9a, P-k and S-f. Cell R1, a create
  with `worktree.useRelativePaths`, is not measured on Linux: git 2.43 on the
  Linux VM cannot write relative records.
  claustrum runs the tests in this order: test 1, test 2, test 3, test 4, the
  stale entry test, this record test, then the deadline test. The rows show test
  3 before the record test (cell P-h against cell P-c). They show tests 1 to 4
  and the record test before the deadline test (row D-9, cell P-f). Not
  measured: a state that fails test 1 and a later test.
  On Linux and macOS the index goes into the entry of test 2 with no second read
  of the record. Two cells of `89cb6289` show the same result with the record
  gone. In cell B4 (Linux and macOS VMs) a wrapper
  removes the record after the read-tree. `89cb6289` answers success, the entry
  holds the index, and the branch stays. In cell Z15 (Linux and macOS VMs) a
  wrapper removes the record after the read-tree and sets the entry to mode 0500.
  `89cb6289` answers `git worktree add failed (checkout): <git text> openat
  w1/index: permission denied`. The undo text for a registration and a branch
  that remain follows, with `(RemoveAll w1: permission denied)`. The entry and the
  branch stay. In the entry `logs/HEAD` is gone, and `HEAD`, `commondir` and
  `logs` stay. claustrum answers the same frame and leaves the same disk in both
  cells (Linux and macOS VMs).
  One state places nothing: the folder at the path of the entry is not the folder
  that the tests accepted. claustrum tests that through the registrations
  directory that the placement opened. The create then answers the failed placement below
  with claustrum's own text, `the registration <entry> is not the folder that was
  tested after the add`. That is divergence D24. No cell measured it at the
  placement.
  With no answer to `rev-parse --absolute-git-dir` the four tests do not run
  (not measured). From the code: claustrum then reads the record right before it
  places the index, in the folder that the `.git` file names. It places the index
  only if the record can be read and names the new worktree. In any other state
  of a folder that is there, it places nothing, the `index` there keeps its
  bytes, and the create answers the failed placement below with claustrum's own
  text: `the registration <entry> has no gitdir record that names this worktree`.
  Those states are a record that is missing, a FIFO or a folder, an empty record,
  and a record of another path, relative or not.
  One state is apart in both forms: an entry folder whose stat fails. The
  placement then runs and fails by itself, with the texts of cells Z10, Z11a and
  Z11b below. In both texts of claustrum `<entry>` is the path of the entry
  folder, not `worktreePath`. The 512-byte text rule above covers the git stderr
  and the text together, so a long stderr cuts the text or drops it.
  After git exits 0, the daemon puts that index in the registration of the new
  worktree, as the file `index`, and removes the temporary directory. On Linux and
  macOS the registration is the entry of test 2 above (row D-12). On Linux and macOS claustrum makes a new
  file there and copies the bytes. The file then equals the one of `89cb6289` on
  Linux and macOS VMs in these facts (rows A1 to A13):
  - It is a new inode, not the temporary file (every Linux row, and 13 of 13
    macOS runs that saw the temporary file).
  - Its group is the group that a new file gets in the registration folder. On
    macOS that is the group of the folder (rows A1, A3, A5 and A6). On Linux it is
    the group of a setgid folder (row A8), and else the primary group (row A9).
  - Its mode is 0666 less the umask of the daemon: 0644 for umask 0022, and 0600
    and 0664 for the umasks 0077 and 0002 (row A12). It is 0644 with `core.sharedRepository
    group` too (row A13), and with the temporary directory on another file system
    (rows A10 and A11).
  - Its mtime is the mtime of the temporary index, rounded up to a whole
    microsecond (every Linux row, and 15 of 15 macOS runs).

  An entry that exists already at the index is replaced. With a file of mode 0600
  there, the index of `89cb6289` is a new inode of mode 0644 on Linux and macOS
  VMs (cell X8). An empty folder there is replaced by the index (cell Z7a, macOS
  VM). A symlink there goes, its target stays, and the index is a new file (cell
  Z7b, macOS VM).

  claustrum takes this order: it creates the file exclusively, and any error but
  "file exists" is the answer. On "file exists" it removes the entry with one
  plain remove, and creates the file exclusively again. The order is claustrum's
  own. It fits these eight rows of `89cb6289`: A14, Y3, Y7 and X8, and the cells
  Z7a, Z7b, Z11a and Z11b of the macOS VM. The failed states are in the next item.

  Not measured: the atime, a temporary mtime that is a whole microsecond, and a
  file system with no group or mode. claustrum leaves the atime alone and keeps a
  whole microsecond. A placement that fails after a drain overrun is not measured
  either. claustrum answers it as the failed placement below. It does the same
  when the caller `timeoutMs` expired before the placement failed. An entry that
  appears at the index between the remove and the second create is not measured.
  claustrum then fails the placement with `openat <registration name>/index: file
  exists`. A failed set of the mtime, after the index is written, fails the
  placement with `chtimesat <registration name>/index: <OS error>`. That is not
  measured, and no state that reaches it is known. Windows
  is not measured. There claustrum moves the temporary file.
  If that move fails, the worktree has no index and the create still succeeds.
- On Linux and macOS, a placement of the index that fails is a failed checkout. The
  frame is `{success:false,error:"git worktree add failed (checkout):
  <text>",errorCode:"worktree_add_failed"}`, and the rollback below runs. These states are measured against `89cb6289`:
  - The registration folder loses its write bit during the checkout (rows A14 and
    A14f on a Linux VM, A14 and A14b on a macOS VM). The OS error is `openat
    w1/index: permission denied`, and the step 2 undo text below follows.
    `w1/index` is the registration name and the file, relative to the
    registrations directory. After the frame the leaf is gone, and the registration and
    the branch stay.
  - The temporary index is gone when git exits 0 (cell X1, Linux and macOS VMs).
    The OS error is `open <temporary directory>/index: no such file or directory`.
    In that cell the leaf, the registration and the branch go. The
    temporary directory has the prefix `claude-ssh-index-` (see Temporary names
    above).
    In attach mode the frame is the same (cell Y6, Linux VM). With a registration
    folder without its write bit too, the step 2 undo text follows (cell Y4, Linux
    VM).
  - A file exists at the index, and the registration folder has no write bit (cell
    Y3, Linux VM). The OS error is `removeat w1/index: permission denied`, and the
    step 2 undo text follows.
  - A folder that holds one file exists at the index (cell Y7, Linux VM). The OS
    error is `removeat w1/index: directory not empty`, and the rollback runs.
  - The registration is gone at the install (cell Z10, macOS VM). The OS error is
    `openat w1/index: no such file or directory`, and the whole rollback runs.
  - The registrations directory has mode 0600 after the checkout, with or without
    a file at the index (cells Z11a and Z11b, macOS VM). The OS error is `openat
    w1/index: permission denied`. The step 2 undo text follows, the leaf goes, and
    the registration and the branch stay.

  `<text>` is the stderr of the checkout and the OS error, joined with nothing
  between them. The text rule then applies to the joined text, so the 512-byte cut
  covers the OS error too. Measured against `89cb6289` on a Linux VM:
  - A stderr that ends with one newline gives one space before the OS error (rows
    A14 and A14f). With no final newline there is no space (cell Y2a). With two
    final newlines there are two spaces (cell Y2b).
  - With no stderr the text is the OS error alone (cell X2).
  - A stderr of 478 bytes keeps the whole OS error (cell Y1b). A stderr of 500 bytes
    keeps its first 12 bytes, ` openat w1/in` (cell Y1a). A stderr of 512 bytes or
    of 1509 bytes keeps none of it (cells Y1c and X3).
- A process that the checkout leaves behind starts in
  the new worktree. On Windows it then blocks the removal of the leaf, and the
  rollback reports it with the undo text below.
- Every rollback after a successful add runs four steps. This covers the checkout
  failure and each `timeout` frame. Steps 1 to 3 and their texts were measured
  against `f6010b97` and `90fca6e6` on a Windows VM. Step 4 follows `89cb6289`:
  1. Delete the entries at the top of the leaf, one at a time, in the order that
     the directory read returns them. The names are not sorted. Stop at the first
     entry that cannot be deleted. Then append `; and the undo could
     not finish for <leaf>: the worktree directory, its registration, and the
     branch all remain; remove them by hand before retrying (RemoveAll <entry>:
     <OS error>)` to the error, and undo nothing else. No branch step runs (row
     B2-11). In attach mode the call made no branch, and the text reads `the
     worktree directory and its registration both remain` (row C08b).
  2. Delete the registration. Then run the branch step on the branch that the call
     created. In attach mode no git call runs (rows C08 and C08b). On Linux and
     macOS, a registration that cannot be deleted stays. Then no branch step runs,
     step 3 still runs, and the text is `; and the undo could not finish for
     <leaf>: the worktree registration and the branch remain; remove them by hand
     before retrying (RemoveAll <registration name>: <OS error>)`. `89cb6289` gives
     that text after a failed placement of the index (rows A14 and A14f on a Linux
     VM, A14 and A14b on a macOS VM). On the Linux VM it runs no git call after
     the checkout there. In attach mode the call made no branch, and the text
     reads `the worktree registration remains; remove it by hand before retrying
     (RemoveAll <registration name>: <OS error>)` (cell X5, Linux and macOS VMs).
     If step 3 fails too, the frame carries the registration text alone and no
     step 3 text (cell X11, and cell Y5 for attach mode, Linux VM). Each of these cells is a failed placement of
     the index. A failed read-tree checkout with a registration of mode 0500 gives
     the same clause, and the branch stays (cell Z5, macOS VM). So does a `timeout`
     frame of a deadline that expired during the checkout (cell Z6, macOS VM). The
     same texts after the two other `timeout` frames are claustrum's choice (not
     measured). A registration whose `gitdir` record is a relative path, as git
     writes it with `worktree.useRelativePaths`, takes the same path (cell Z9b,
     macOS VM). So does a registrations directory of mode 0600 (cells Z11a and
     Z11b, macOS VM).
     On Linux and macOS step 2 deletes the entry that the tests after the add
     accepted, the entry of test 2. The rollback reads neither the `.git` file of
     the leaf nor the `gitdir` record again. `89cb6289` removes that entry in
     these cells, each with a read-tree that fails. The record is rewritten to
     `/nonexistent/.git` (cells Z16 and B0a). Cells B7a and B7b are that state
     in attach mode and with a `worktreeRoot`. The record names a live sibling
     worktree (cell B1). The `.git` file of the leaf names a folder outside the
     repository, and that folder stays (cells Z18 and B0b, and cell Z18r on a
     Linux VM only). The `.git` file of the leaf names the
     registration `w9` of a sibling: `w1` goes and `w9` stays (cell B2). The
     same holds when the record of `w9` names the leaf (cell B3). The `.git`
     file of the leaf is removed (cell B5). All these cells but Z18r ran on
     Linux and macOS VMs.
     Two more cells have a daemon `GIT_DIR` of another repository X: cell B9
     with `GIT_COMMON_DIR` of X too, and cell B9b with `GIT_DIR` alone (Linux
     and macOS VMs). After the failed read-tree the registration that git made
     in X is gone on `89cb6289`, and `baseRepo` has no `worktrees` folder.
     The frame is the plain failed checkout in each cell. claustrum answers the
     same frame and removes the same entry in each of these cells (Linux and
     macOS VMs, Z18r on a Linux VM). The delete is one `os.Root.RemoveAll` of the entry name, through a root
     at the registrations directory. If the registrations directory cannot be
     opened, the text holds `open <path>: <OS error>` in place of the
     `RemoveAll <registration name>: <OS error>` text (not measured).
     Two guards of that delete are claustrum's own. Each deletes nothing, adds no
     text and lets the branch step run. The home guard (D2) refuses an entry path
     that is the home folder or holds it. The identity guard refuses a folder
     that is not the folder that the tests accepted. It runs after the open of
     the registrations directory, on the entry in that opened directory.
     claustrum holds the accepted folder open until the create answers.
     While that handle is open, a new folder does not get that identity. If the
     open fails, the identity comes from a stat of the path (not measured).
     That is divergence D24, and
     two cells of Linux and macOS VMs show it, 2 runs each. In cell B6 a wrapper renames
     `w1` to `w1x` and makes a new empty folder `w1`. `89cb6289` removes the
     empty `w1`. In cell B6b the wrapper renames `w1` to `w1x`, and the
     registration `w9` of a live sibling worktree to `w1`. `89cb6289` removes
     that folder with the files of the sibling. claustrum removes no folder in
     both cells, and the frames are equal (Linux and macOS VMs). See
     [`DIVERGENCES.md`](../DIVERGENCES.md) → D24.
     If git gave no answer to `rev-parse --absolute-git-dir`, the tests did not
     run (not measured). claustrum then takes the folder that the `.git` file of
     the leaf names, and deletes it only if its `gitdir` record names the leaf.
     If claustrum cannot read that record for a permission error, it
     attempts no delete of the registration. On Linux and macOS it runs no branch step, and the clause
     is the one above, with `RemoveAll <registration name>: permission denied` in
     the parentheses. This holds only for a
     registration inside the registrations directory. There claustrum
     deletes only a registration that is a direct child of the registrations
     directory, by its resolved path. It also refuses the delete if the
     registrations directory is no longer the one that it checked. A refused
     registration stays, with claustrum's own text in the parentheses:
     `<registration> is not a direct child of <registrations directory>` or
     `<registrations directory> is no longer the directory that was checked`. Both
     texts hold absolute paths. On Windows claustrum has neither test. It deletes the resolved path, adds no text for a failed delete, and runs the branch step.
     Windows is not measured.
  3. Remove the leaf directory, which is now empty. If that fails, append `; and
     the undo could not finish for <leaf>: the worktree directory remains
     (re-populated while undoing?); remove it by hand before retrying (removeat
     <leaf base name>: <OS error>)`.
  4. If the branch stays, append its text after `; and the undo
     could not finish for <leaf>: `. After a step 3 failure, the step 3 text comes
     first, and the two parts are joined by `; `. See [The branch
     step](../PROTOCOL.md#the-branch-step).

  The `errorCode` does not change. `<leaf>` is `worktreePath` exactly as sent, and
  `<entry>` is a name at the top of the leaf. With a trailing slash on
  `worktreePath`, the `removeat` part still names the base name, such as `w1`. The
  step 1 order was measured against both references on Linux ext4 and macOS APFS
  VMs. The three wordings of steps 1 and 3 are fixed, and only `<OS error>` varies. On Windows the measured causes were an open handle, a
  process with its working directory in the leaf, and a running executable. An
  ACL that denies the delete and a file name with a trailing dot were causes too. On Linux and macOS
  claustrum gives the same wordings with the OS error text of Go, for example
  `permission denied`. Both references gave the same text on Linux and macOS VMs.

Worktree population works as follows. `git worktree add` checks out tracked files
only, so the daemon then seeds the new worktree. The copies are best-effort, and a
failure never fails the request. A caller `timeoutMs` that expires before the
copies end still fails it, as `timeoutMs` above describes:
- `.worktreeinclude` sits at the repo root and uses `.gitignore` syntax. It is an
  include filter over the git-ignored set. The daemon copies an untracked file only
  when the manifest names it and git's standard rules ignore it. A manifest match
  that git does not ignore is not copied. The manifest must be a regular file. A
  symlink or a directory copies nothing and runs no git. An empty regular file
  still runs `git version` and the scan, and it copies nothing. git reads a temp
  copy of the manifest bytes. The rules in this bullet and the next three were
  measured against `f6010b97` on a macOS VM, except the rows named below. A
  Linux VM re-checked the version parse, opening rules, counts, batches and
  error arms. A Windows VM measured the Windows batch budget. The prefix rule
  for one glob segment, with its case folding, was measured on Linux, macOS and
  Windows VMs. So was the literal form after a leading `/`. Linux and Windows
  VMs measured the glob after a leading `/`.
  They also measured `build//`, `build//a.txt`, `BUILD//` and a lone `\` that
  ends the first segment. The other `//` and `\` rows were measured on a Linux
  VM only.
- If the manifest is a regular file, the daemon runs `git version`. The call has no `-c`
  option and no `-C`. It runs in the daemon's working directory, with the
  daemon's environment unchanged. The first `git version ` in the output counts,
  even after other text. A digit must follow it. Git 2.32.0 or later gets the
  directory scan. Older git gets the full scan, and so does output that does not
  parse. A non-zero exit also gets the full scan, even with valid output. Major
  and minor compare as numbers. A number too large for an int counts as very
  large. Text after the numbers is ignored, so `2.32.0.windows.1` gets the
  directory scan.
- The full scan runs
  `git ls-files --others --ignored --exclude-from=<manifest copy> -z -- ':(exclude).claude/worktrees'`.
  `git check-ignore --stdin -z` then keeps the paths that git's standard rules
  ignore. A runtime-state path (see below) or a nested repository does not go
  to check-ignore. `f6010b97` drops the runtime-state paths on Linux and macOS
  VMs (I15c, I15d). A Linux VM measured the nested-repository rule in this scan
  (D16, with the old scan forced and with git 2.25.1). If every path is dropped,
  no check-ignore call runs. That case was not measured.
  If either call fails, nothing is copied. The full scan searches every ignored
  directory.
- The directory scan first lists the ignored entries with
  `git ls-files --others --ignored --exclude-standard --directory`. If the listing
  fails, nothing is copied. Every ignored file in the listing is a candidate. An
  ignored directory is searched only when the manifest opens it. An any-depth
  pattern therefore does not reach a file in a closed directory: `*.txt` does not
  copy `build/a.txt` when git ignores `build/`. The opening rules follow:
  - A literal name of one segment opens each listed directory that has the name
    as any of its segments. `build` opens `build`, `sub/build` and `build/x`. So
    does `**/` and then one literal. Case does not matter.
  - One segment after a leading `/` opens each listed directory whose first
    segment matches it as a glob. `/build/` opens `build`, not `sub/build`.
    `/a?b/` opens `aXb` and `a_b`, not `ab`. The prefix rule does not apply
    here. A `\` escapes the next character, so `/ab\q/` opens nothing. Case
    does not matter.
  - One glob segment without a leading `/` opens by its literal prefix. A glob
    segment holds `*`, `?`, `[` or `\`. The prefix ends before the first of
    these characters. The segment opens each listed directory whose path starts
    with the prefix. The rest of the segment is not used. Case does not matter.
  - So `b*` opens `build`, not `sub/build`. `su*` opens `sub/build`.
    `a?b/` and `a[_]b/` open `ab`, `a b` and `a/c`. `sub\build/` opens `sub`,
    `sub/build` and `subbuild`. `AB\q/` opens `ab`.
  - `**/` and then a literal and more segments matches the rest of the pattern
    from any segment of a listed directory. `**/sub/build/` opens `sub/build`.
  - A pattern of two or more segments opens each listed directory that it matches
    segment by segment, and the listed parents of that directory.
  - In such a pattern, a segment that ends in a lone `\` matches any name. So
    `sub\/x/` opens each listed directory of one segment and each listed
    `<name>/x`. Git reads that line as `sub/x/`. This holds for the first, a
    middle and the last segment. An even run of `\` at the end of a segment is
    a literal `\`, so `zz\\/x/` opens only `zz\/x`. A run of three acts like a
    run of one. Directly after `**/`, such a segment opens nothing.
  - A `//` in a pattern leaves an empty segment. `build//`, `build//a.txt` and
    `BUILD//` open `build`, not `sub/build` or `a/x/build`. `build//` alone
    opens no dot directory. The empty segment matches no name, so `//x` opens
    nothing and `a//b` opens only a listed `a`.
  - One segment without a leading `/` that starts with `*`, `?`, `[` or `\` has
    an empty prefix. It opens no directory. Neither does `**/` and then a glob. A negation and a
    comment open nothing too.
  - Git's own match still reads a `\` as an escape. The manifest goes to git
    unchanged. So `a\ b/` opens `ab`, but git copies from `ab` only the files
    that another manifest line matches. Git does not read `\` as a separator.
  - In a pattern of two or more segments, a `\` does not cut a prefix.
    `sub/b\q/` opens only `sub`.
  - Only the first 256 counted patterns can open a directory. A negation counts.
    Blank lines, comments and patterns over a cap do not count. A line of only
    tabs and spaces is blank. So is a line that is empty after one leading and one
    trailing `/` are removed, such as `//`.
  - A pattern opens nothing if it has more than 1024 bytes after one leading and
    one trailing `/` are removed. It also opens nothing if it has more than 32
    segments.
  - An any-depth pattern also opens the listed dot directories. A pattern of one
    segment is any-depth, unless it starts with `/`. A pattern that starts with
    `**/` is any-depth too. The 256 count does not apply to this. At most 128 dot
    directories open, in listing order. The root `.claude/` takes a place unless
    an explicit pattern matches it. The flag never opens these 14 names, in any case. They
    take no place: `.angular`, `.cache`, `.dart_tool`, `.gradle`, `.next`,
    `.nuxt`, `.parcel-cache`, `.pnpm-store`, `.svelte-kit`, `.terraform`, `.tox`,
    `.turbo`, `.venv` and `.yarn`. An explicit pattern still opens a skipped
    directory, or one past the 128th. A dot directory that an explicit pattern
    matches takes no place. `.claude/worktrees/` never opens, even when a
    pattern names it.
  - The root `.claude/` opens by the rules above. It does not become one
    pathspec. Its children take its place in the directory batch, in the order
    of the directory read. `worktrees` is left out in any case. This is the list
    of the `.claude/` pass below. So a root `.claude/` that holds only
    `worktrees` adds no pathspec. `f6010b97` does the same on Linux and macOS
    VMs (rows C02, C03, C05, D23, I14b and I15d). The Windows VM shows it too
    (`Cl_anydepth`).
  - The candidates go to `git ls-files --exclude-from=<manifest copy>` in batches.
    Files and directories never share a batch. A directory pathspec has no
    trailing `/`. A batch fills in listing order. When the next path does not
    fit, a new batch starts. Each argument after the `-c` options costs its
    length plus 3 bytes. That covers the fixed `ls-files` arguments, the
    `--exclude-from` argument and the paths. One call costs at most 131 072
    bytes on Linux and macOS, and at most 24 576 bytes on Windows. The `.claude/`
    pass below uses the same budget. Each value is a fit to the measured batch
    counts. It is not a value read from the reference. The `-c` options do not
    count in claustrum. Whether `f6010b97` counts them was not measured. On Linux and macOS, every value from 131 070 to
    131 073 fits the directory batches. On Windows, a one-byte bisection pins
    24 576 from both sides. On a Windows VM, a command line of 32 412 characters started, and
    one of 36 012 characters did not. The temp file name starts with a 27-byte
    prefix, the same length as in the measured `f6010b97` argv. A random
    decimal suffix follows it. The measured suffix had 8 to 10 digits in both
    daemons.
    A failed batch is skipped, and the other batches are still copied.
  - Only the paths from the directory batches go to `git check-ignore --stdin -z`.
    It keeps the paths that git's standard rules ignore. The file candidates are
    copied without it. If it fails, the directory paths are dropped, and the file
    candidates are still copied.
  - Three kinds of directory path do not go to check-ignore. The first is a
    path that the file batches also print. The second is a nested repository,
    which ends in `/`. The third is a Claude runtime-state path. If no path is
    left, no check-ignore call runs. `f6010b97` sends the same paths on Linux
    and macOS VMs. The first rule comes from A03 to A05, A14 and A16 to A18. The
    second comes from D16. The third comes from C02 to C05 and I15c.
  - If the ignored files of the listing total more than 1 MiB, the full scan runs
    instead. Each file counts as its path length plus 3 bytes.
- A nested repository inside an ignored directory is not copied, and no empty
  directory is left for it.
- `.claude/` is copied separately, with no manifest entry. A second pass runs
  `git --literal-pathspecs ls-files --others --ignored --exclude-standard -z --`
  with one pathspec for each child of `.claude/`, such as `.claude/settings.json`.
  It leaves out `worktrees` in any case. If no other child exists, the pass runs
  no git. `f6010b97` names the same children on Linux, macOS and Windows VMs. The
  case rule was measured on Linux only. The pathspecs go into batches in the
  order of the directory read, one git call for each batch. The budget is the
  budget of the directory batches above. The fixed arguments cost 87 bytes. So
  the pathspecs of one call cost at most 130 985 bytes on Linux and macOS, and
  at most 24 489 bytes on Windows. A Linux VM and a Windows VM measured these
  split points to the byte against `f6010b97`. The largest rows had 2 400
  children on Windows and 30 000 on Linux. On
  macOS the `.claude/` batches were not measured. Before each batch, the daemon
  runs `git config -z --list`, as it does before most hardened
  calls. `f6010b97` also makes that call before each batch. A failed batch is
  skipped, and the other batches still copy. The pass copies what git lists, minus
  the exclusions in the bullets below. A `.claude/` the repo
  git-ignores is therefore seeded into the new worktree. A `.claude/` that is
  merely untracked is not, because that view cannot see it. `.claude/worktrees/` is
  always skipped, because that is where session worktrees live. The listing is
  limited to the repo-root `.claude/`, so a nested one is reached only by the
  manifest pass. This is measured against `19f30c46` and `90fca6e6` alike.
- Claude runtime state is skipped by both passes since `90fca6e6`. The names
  are `scheduled_tasks.json`, `scheduled_tasks.lock`, `routines/.state`,
  `worktrees`, `checkpoints`, `mailbox`, `agent-registry.json`, `first-run` and
  `assistant-daemon-state.json`. They match as whole path components, directly
  under `.claude/`, case-insensitively. `.claude/Checkpoints/` is therefore
  dropped, while `.claude/mailboxes/` and `.claude/nested/mailbox/` are both
  copied. All nine names and both boundary cases are measured against `19f30c46`
  and `90fca6e6`. `19f30c46` copies eight of the nine. It drops `worktrees` as
  well, so both builds drop `worktrees`.
- The daemon skips symlinks. A filename that `git ls-files` C-quotes, with a tab, a
  quote, a backslash or a non-ASCII byte, IS copied. Both passes use `-z` and split
  on NUL. An earlier version of this document said the opposite and called it a
  reference limitation reproduced for parity. It was neither.
- The copies do not preserve the source mode. The daemon creates them
  0666-subject-to-umask, so an executable arrives non-executable and a `0400`
  source is widened. This matches the reference. Treat the manifest as a way to
  name configuration, not secrets or scripts.
- Destination containment on both passes is claustrum's own. Every copy resolves
  its destination component by component inside the new worktree. The copy is
  dropped when an intermediate component is a symlink, so a link checked out into
  the worktree cannot carry a copy outside it. Whether the reference refuses the
  same is unmeasured. No probe behind this section has a symlinked-intermediate
  fixture. Treat it as claustrum hardening, not parity. A `..` component cannot
  occur, because `git ls-files` never prints one.
- An opted-in `-git-timeout` (D5) that kills a git call loses what that call
  gives the pass. A killed listing loses the pass, and a killed batch loses that
  batch. A killed `git check-ignore` loses the whole full scan, or the directory
  paths of the directory scan. A killed `git version` selects the full scan.
  The reply is still `{"success":true}`, so the loss is silent and
  wire-invisible. Each git call has its own deadline, so the manifest copy can
  succeed while `.claude/` files are lost, or the other way round. The `.claude/` pass has no manifest
  precondition. It runs git on every create where `.claude/` holds a child other
  than `worktrees`. This is off by default.

## Git filters of the checkout

The rules of this section follow `5fd08069`. The pinned build `89cb6289` has none of them: it runs every filter. A Linux VM with git 2.43.0 ran three daemons side by side in each cell. They are `89cb6289`, `5fd08069`, and a claustrum build of `main` before this change. That claustrum build equals `89cb6289` in every cell, in the reply, the markers and the disk. The tables are from that Linux VM. A Windows VM ran 23 of the cells: see [Windows](#windows). The part [claustrum and the filter rule](#claustrum-and-the-filter-rule) says what claustrum does now. The tests in `gitfilter_test.go` pin the rules.

In each cell the request creates the branch `w1` at `<repo>/.claude/worktrees/w1`. A filter is a marker script, unless the row gives its value. A marker that fired proves that the filter program ran. Without a daemon, plain `git worktree add` fires the marker in every row whose `89cb6289` column says "fired". `git-lfs` is not installed on the Linux VM.

- "ok" is `{"success":true,"path":"<repo>/.claude/worktrees/w1","sourceBranch":"main","branch":"w1"}`, with the worktree folder, the registration and the branch on disk.
- "refused" is the frame below with the filter name `m`, with no worktree folder, no registration and no branch on disk. No marker fired in a refused row.

`{"success":false,"error":"Claude doesn't run the \"m\" git filter, because only this repository's own git config defines it. If you trust that filter, copy the [filter \"m\"] section from that config (usually .git/config) into ~/.gitconfig on the remote machine, then try again.","errorCode":"repository_only_filter"}`

### Which keys make a definition

The filter is `m`, and `.gitattributes` holds `a.txt filter=m`.

| Cell | Config | `89cb6289` | `5fd08069` |
|---|---|---|---|
| A1 | `smudge` in `.git/config` | ok, fired | refused |
| A2 | `process` in `.git/config` | `worktree_add_failed` with the text of git, fired, nothing left | refused |
| A3 | `clean` in `.git/config` | ok | ok |
| A4 | `required = true` in `.git/config` | ok | ok |
| A5 | `smudge` with an empty value in `.git/config` | ok | ok |
| A7 | `clean` in `.git/config`, `smudge` in `~/.gitconfig` | ok, the marker of `~/.gitconfig` fired | ok, the marker of `~/.gitconfig` fired |
| A8 | `smudge` in `.git/config`, `clean` in `~/.gitconfig` | ok, fired | refused |
| O4 | `smudge` in `.git/config`, `required = true` in `~/.gitconfig` | ok, fired | refused |
| O5 | `smudge` in `.git/config`, an empty `smudge` in `~/.gitconfig` | ok, fired | refused |
| O6 | `smudge` in `.git/config` and in the system config | ok, the marker of the repository fired | ok, the marker of the system config fired |

### Where the definition is

| Cell | The `smudge` key is in | `89cb6289` | `5fd08069` |
|---|---|---|---|
| B1 | a file that `.git/config` includes | ok, fired | refused |
| B2 | `config.worktree`, with `extensions.worktreeConfig` | ok, fired | refused |
| B3 | the system config (`GIT_CONFIG_SYSTEM`) | ok, fired | ok, fired |
| B4 | a file that `~/.gitconfig` includes | ok, fired | ok, fired |
| B5 | `$XDG_CONFIG_HOME/git/config` | ok, fired | ok, fired |
| B6 | `GIT_CONFIG_COUNT`, `GIT_CONFIG_KEY_0` and `GIT_CONFIG_VALUE_0` of the daemon | ok, fired | ok, fired |

### Which paths count

The `smudge` key is in `.git/config`.

| Cell | State | `89cb6289` | `5fd08069` |
|---|---|---|---|
| C1 | no `.gitattributes` file | ok | ok |
| C2 | the attribute names a filter `other` that no config defines | ok | ok |
| C3 | the attribute is in `sub/.gitattributes` | ok, fired | refused |
| C4 | the attribute is only in `.git/info/attributes` | ok, fired | refused |
| C5 | the attribute is only in the `core.attributesFile` of the user | ok, fired | refused |
| C6 | a macro attribute | ok, fired | refused |
| C7 | `filter=m`, then `-filter` | ok | ok |
| C8 | the `.gitattributes` file is not committed | ok | ok |
| C9 | the attribute is committed on another branch | ok | ok |
| O1 | only `sub/b.txt` uses the filter | ok, fired | refused |
| O2 | the file that uses the filter is empty | ok, fired | refused |
| O3 | only a symlink path uses the filter | ok | ok |

In cells C1 to C9, `5fd08069` refuses exactly the cells where plain git fires the marker.

### Values

The value is the `smudge` value in `.git/config`, unless the row names another key. No value of cells V1 to V9, V13 and V14 holds a marker.

| Cell | Filter name | Value | `89cb6289` | `5fd08069` |
|---|---|---|---|---|
| V1 | `m` | `cat` | ok | ok |
| V2 | `m` | `cat` and one space | ok | refused |
| V3 | `m` | `/bin/cat` | ok | refused |
| V4 | `m` | `cat -` | ok | refused |
| V5 | `m` | `cat %f` | ok | refused |
| V6 | `m` | `true` | ok | refused |
| V7 | `lfs` | `git-lfs smudge -- %f` | ok | ok |
| V8 | `m` | `git-lfs smudge -- %f` | ok | refused |
| V9 | `lfs` | `git lfs smudge -- %f` | ok | the refusal that names `lfs` |
| V10 | `lfs` | a marker script | ok, fired | the refusal that names `lfs` |
| V11 | `m` | `smudge = cat` and `process` with a marker script | `worktree_add_failed` with the text of git, fired, nothing left | refused |
| V12 | `m` | a marker script, with `clean = cat` and `required = true` | ok, fired | refused |
| V13 | `lfs` | `process = git-lfs filter-process` | `worktree_add_failed` | `worktree_add_failed`, nothing left |
| V14 | `m` | `cat` under the key spelled `Smudge` | ok | ok |

In cell V13 the text of `5fd08069` is `git worktree add failed (checkout): git-lfs filter-process: 1: git-lfs: not found fatal: the remote end hung up unexpectedly`. The text of `89cb6289` has `hint:` lines of git before that text.

### Names

| Cell | State | `89cb6289` | `5fd08069` |
|---|---|---|---|
| D1 | filters `m` and `b`, attribute lines for `m` then `b` | ok, both fired | refused |
| D2 | the same, attribute lines for `b` then `m` | ok, both fired | refused |
| D3 | `[filter "M"]` with `filter=m`, and the reverse | ok | ok |
| D4 | the name `a.b` | ok, fired | the refusal that names `a.b` |
| D5 | a name of 300 bytes | ok, fired | the frame below, nothing left |
| D6 | the name `q"t` | ok, fired | the refusal with `\"q\\\"t\"` at both places of the name |
| D6 | the name `s p`, attribute line `a.txt filter=s p` | ok | ok |
| N1 | `a.txt filter=b`, `z.txt filter=m` | ok, both fired | the refusal that names `b` |
| N2 | `a.txt filter=m`, `z.txt filter=b` | ok, both fired | refused |
| N3 | `1.txt filter=c`, `2.txt filter=a`, `3.txt filter=b` | ok, all three fired | the refusal that names `c` |
| N4 | `a.txt filter=b`, and `m` is defined and not used | ok, the marker of `b` fired | the refusal that names `b` |

Cell D5 on `5fd08069`:

`{"success":false,"error":"git worktree add failed (checkout): this repository's git config cannot be used: a filter defined only there has a name longer than 200 bytes (it starts \"nnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnn\"); define it in ~/.gitconfig on the remote machine, or rename it","errorCode":"worktree_add_failed"}`

With the name `s p`, git reads the attribute line as `filter=s` and an attribute `p`. So no path uses the filter `s p`.

### The read-tree call of 5fd08069

A logging wrapper recorded each git call of cells A1, V1 to V14, N1 to N4 and O1 to O6.

- `89cb6289` has no `-c` pair with a `filter.` key on its read-tree call, in any cell.
- In each refused cell, `5fd08069` passes three pairs for each filter of `.git/config`: `filter.<name>.process=`, `filter.<name>.required=true` and `filter.<name>.smudge=false`.
- The pairs are in sorted order of the filter names. A filter that no path uses has them too (cell N4, pairs for `b` and `m`). In cell N3 the order is `a`, `b`, `c`.
- In cell O3 the call has the three pairs for `m`, exits 0, and the reply is ok.
- In cells V1, V14 and O6 the call has no such pair.
- In cell V7 the one pair is `filter.lfs.smudge=git-lfs smudge -- %f`. The call exits 0, and its stderr says that `git-lfs` was not found.
- In cell V13 the one pair is `filter.lfs.process=git-lfs filter-process`. The call exits 128.
- In each refused cell the call exits 128, and its stderr is one line: `fatal: <path>: smudge filter <name> failed`. The reply names that filter. The path is `a.txt` in cells N1, N2 and N4, and `1.txt` in cell N3.

### The order of the steps

| Cell | State | `89cb6289` | `5fd08069` |
|---|---|---|---|
| F1 | cell A1, with a poll of the disk each 3 ms | ok, fired | refused |
| F2 | the worktree folder exists | the `unsafe_path` refusal of a path that exists | equal. The empty folder stays, and nothing else is made |
| F3 | `baseRepo` is a plain folder | `{"success":false,"error":"not a git repository","errorCode":"not_a_repo"}` | equal |

In cell F1, `5fd08069` makes 19 git calls. Call 9 is `worktree add --no-track --no-checkout -b w1 <path>`. Calls 10 and 11 are `--git-dir=<repo>/.git config -z --list` in the new worktree. Call 12 is the read-tree call. Calls 13 to 19 follow, and the last one is `update-ref --no-deref -d refs/heads/w1 <sha>`. There is no `git worktree remove` call. The poll saw the worktree folder, its `.git` file, the registration and the branch, and each was gone after the reply. It saw no `a.txt` in the new worktree. The empty `<repo>/.claude/worktrees` folder stays.

### The switch and the other methods

| Cell | Request | `89cb6289` | `5fd08069` |
|---|---|---|---|
| E1 | cell A1 with `"shadowDirOff":true` | ok, fired | ok, fired, with 10 git calls as on `89cb6289` |
| E2 | `git.status` in a worktree with a changed `a.txt`, and a `clean` marker in `.git/config` | `{"isRepo":true,"clean":false,"changes":[" M a.txt"]}`, no marker | equal |
| E2 | the same with `"shadowDirOff":true` | the same reply, no marker | equal |
| E3 | `git.worktree_remove` in the repository of cell A1 | `{"success":true}`, the folder and the registration are gone, the branch stays | equal |
| E4 | `git.status` with a `diff.x.textconv` marker in `.git/config` | `{"isRepo":true,"clean":false,"changes":[" M a.txt"]}`, no marker | equal |

Plain `git status` runs no textconv program either. So cell E4 shows nothing about textconv.

### Later cells

The builds of this change that ran on the VMs have the names A to D, in the order of their rounds. A later round on the Linux VM ran 27 more cells on `89cb6289`, `5fd08069` and build A of this change. The table has the two reference builds. The part [claustrum and the filter rule](#claustrum-and-the-filter-rule) has claustrum.

| Cell | State | `89cb6289` | `5fd08069` |
|---|---|---|---|
| Q1 | `smudge` in `~/.gitconfig`, `process` in `.git/config` | `worktree_add_failed` with the text of git, the marker of the repository fired | ok, the marker of `~/.gitconfig` fired, no `filter.` pair |
| Q2 | `process` in `~/.gitconfig`, `smudge` in `.git/config` | `worktree_add_failed`, the marker of `~/.gitconfig` fired | `git worktree add failed (checkout): fatal: the remote end hung up unexpectedly`, the same marker, no `filter.` pair |
| Q3 | the name `a=b` | ok, fired | the frame below, nothing left |
| Q4 | a name of 200 bytes | ok, fired | the refusal that names it |
| Q4 | a name of 201 bytes | ok, fired | the frame of cell D5 |
| Q5 | a name of 300 bytes, `smudge = cat` | ok | ok |
| Q6 | a name of 300 bytes, also in `~/.gitconfig` | ok, the marker of the repository fired | ok, the marker of `~/.gitconfig` fired |
| Q7 | `smudge = cat`, with a wrapper named `cat` first on PATH | ok, the wrapper fired | ok, the wrapper did not fire |
| Q8 | two `smudge` values, `cat` and then a marker | ok, fired | refused |
| Q8 | two `smudge` values, a marker and then `cat` | ok | ok, no marker, no `filter.` pair |
| Q9 | an empty `process` and a marker `smudge` | ok, no marker | refused |
| Q10 | a `smudge` key with no value | `worktree_add_failed` with git's `missing value for 'filter.m.smudge'` text | refused, with the three pairs |
| Q11 | the name `a\b` | ok, fired | the refusal with `\"a\\\\b\"` at both places of the name |
| Q11 | the name `ä` | ok, fired | the refusal with `\"ä\"` |
| Q11 | a name with a tab | ok | ok |
| Q12 | `shadowDirOff` as `"true"`, `1`, `null`, `false` or `{}` | ok, fired | refused, in all five |
| Q12 | `shadowDirOff` as `true` | ok, fired | ok, fired |
| Q13 | cell A1 with `"timeoutMs":1` | `git worktree add timed out after 1ms (deadline expired before the checkout started)`, `timeout`, nothing left | equal |
| Q13 | cell A1 with `"timeoutMs":1000`, and a read-tree call that waits 3 s | `git worktree add timed out after 1000ms (deadline expired during the checkout): signal: killed`, `timeout`, nothing left, no marker | equal |
| Q14 | cell A1, with a git that answers exit 129 to `--show-scope` | ok, fired | refused |
| Q15 | the filter `lfs` with `clean`, `smudge`, `process` and `required`, and a stub `git-lfs` | `worktree_add_failed`, the `filter-process` marker fired | `git worktree add failed (checkout): fatal: the remote end hung up unexpectedly`, the same marker |

Cell Q3 on `5fd08069`:

`{"success":false,"error":"git worktree add failed (checkout): this repository's git config cannot be used: the filter \"a=b\" is defined only there, and Claude cannot handle \"=\" in a filter's name; define it in ~/.gitconfig on the remote machine, or rename it","errorCode":"worktree_add_failed"}`

- With the tab, git reads the attribute line as `filter=a` and an attribute `b`. So no path uses that filter.
- In cells Q13 the deadline expires before git answers, and the answer is the timeout frame.
- In cell Q15 the read-tree call of `5fd08069` has four pairs: `filter.lfs.clean=git-lfs clean -- %f`, `filter.lfs.process=git-lfs filter-process`, `filter.lfs.required=true` and `filter.lfs.smudge=git-lfs smudge -- %f`. The stub exits 1 for `filter-process`.
- In cell Q12, a `git.info` with `shadowDirOff` as `false` or as `{}` has `"settingsControl":"shadow"` on `5fd08069`. `89cb6289` has no such member.

### Cells of the third round

A third round on the Linux VM ran 10 more cells on `89cb6289`, `5fd08069` and build B of this change. Each cell has the filter of cell A1, unless the row says otherwise. `<home>` is the home folder of the cell. Nothing is left on disk in a failing cell, on each build.

| Cell | State | `89cb6289` | `5fd08069` |
|---|---|---|---|
| R1 | the filter is in a file that `includeIf "gitdir:<repo>/"` of `~/.gitconfig` names | ok, fired | ok, fired, no `filter.` pair |
| R2 | `GIT_DIR` and `GIT_WORK_TREE` of the daemon name another repository | `{"success":false,"error":"not a git repository","errorCode":"not_a_repo"}`, 2 git calls | refused, 17 git calls |
| R3 | `TMPDIR` is inside a third repository that defines the filter too | ok, the marker of the repository fired | refused |
| R4a | a syntax error in `.git/config` | `config-defined hooks could not be pinned off; git not run: listing the configuration in force: exit status 128: fatal: bad config line 8 in file <repo>/.git/config`, `worktree_add_failed` | `this repository's git config cannot be used: fatal: bad config line 8 in "<repo>/.git/config"`, `worktree_add_failed` |
| R4b | a syntax error in `~/.gitconfig` | `git cannot run on this host; git not run: exit status 128: fatal: bad config line 1 in file <home>/.gitconfig`, `worktree_add_failed` | equal |
| R4c | `~/.gitconfig` is a directory | the `config-defined hooks could not be pinned off` text with git's `unable to access '<home>/.gitconfig': Is a directory` warnings | `git could not check this repository on the remote machine: at "<repo>": ` and then git's warnings and `fatal: unknown error occurred while reading the configuration files`, `worktree_add_failed` |
| R4d | `~/.gitconfig` has mode 0000 | ok, fired | refused |
| R5a | `TMPDIR` names a folder that does not exist | `git worktree add failed (checkout): stat <home>/no-such-tmp: no such file or directory`, `worktree_add_failed` | refused |
| R5b | `TMPDIR` names a folder of mode 0500 | `git worktree add failed (checkout): mkdir <home>/tmp500/claude-ssh-index-<n>: permission denied`, `worktree_add_failed` | refused |
| R6 | no filter at all | ok | ok |

### Cells of the fourth round

A fourth round on the Linux VM ran 9 more cells on `89cb6289`, `5fd08069` and build C of this change.

| Cell | State | `89cb6289` | `5fd08069` |
|---|---|---|---|
| S1 | cell R1, with a git that answers exit 129 to `--show-scope` | ok, fired | ok, fired |
| S3 | a filter name of the two bytes `FF 78`, in `.git/config` and in `.gitattributes` | ok, fired | `git worktree add failed (checkout): fatal: a.txt: smudge filter x failed`, `worktree_add_failed`, no marker, nothing left |
| S5 | 60 filters of `.git/config` with names of 190 bytes, and one path that uses one of them | ok, fired, no `filter.` pair | the refusal that names the used filter, 180 `filter.` pairs |

- `89cb6289` and `5fd08069` make no `--show-scope` call. So for them cell S1 is cell R1 again, and cell Q14 is cell A1 again.
- In cell S3, the read-tree call of `5fd08069` has the three pairs for the name and exits 128. The byte `FF` that git wrote in its stderr is not in the reply. No reply of a build holds a byte that is not valid UTF-8.
- A translated git did not run: the VM has no translation of git.

Cells S2 have `"timeoutMs":1000` and a wrapper that waits 3 s on one listing after `worktree add`. Each cell ran one time, with the fixture of cell A1 and with no filter at all. "timeout" is `{"success":false,"error":"git worktree add timed out after 1000ms (deadline expired during the checkout): context deadline exceeded","errorCode":"timeout"}`, with nothing on disk and no marker.

| Cell S2: fixture, and the listing that waits | `89cb6289` | `5fd08069` | build C |
|---|---|---|---|
| A1, the `--show-scope` listing | ok, fired, 0.18 s | refused, 0.50 s | timeout, 3.2 s |
| A1, the first `--git-dir=… config -z --list` | timeout, 3.2 s | timeout, 3.4 s | timeout, 3.2 s |
| A1, the second such listing | ok, fired, 0.16 s | timeout, 3.5 s | refused, 0.24 s |
| no filter, the `--show-scope` listing | ok, 0.17 s | ok, 0.16 s | timeout, 3.4 s |
| no filter, the first such listing | timeout, 3.2 s | timeout, 3.5 s | timeout, 3.3 s |
| no filter, the second such listing | ok, 0.13 s | timeout, 3.4 s | ok, 0.19 s |

- A build answers "timeout" exactly where it makes the call that waits. Between `worktree add` and the read-tree call, `89cb6289` makes one `--git-dir=<repo>/.git config -z --list` call and `5fd08069` makes two. The build C makes the `--show-scope` listing and one such call.
- On the two reference builds the wait of the wrapper ran to its end. On build C the wrapper logged no end of its wait.

### Cells of the git-lfs round

A Windows VM with a working `git-lfs` 3.7.1 ran 8 cells on `89cb6289`, `5fd08069` and build D of this change, in two equal passes. `p.bin` is a committed git-lfs pointer under `p.bin filter=lfs`. Its object is not in the local store, unless the row says so. The agent is a marker program. "the two keys" are `lfs.standalonetransferagent = x` and `lfs.customtransfer.x.path` with the agent. The filter `lfs` is in the system config, apart from cell L1.

| Cell | State | Plain git | `89cb6289` | `5fd08069` | build D |
|---|---|---|---|---|---|
| L1 | cell X4: the filter `lfs` and the two keys only in `.git\config` | fails, agent ran | fails, agent ran | fails, no agent | T1, no agent |
| L2 | the two keys in `.git\config` | fails, agent ran | fails, agent ran | fails, no agent | T2, no agent |
| L3 | only the `lfs.customtransfer.x` section in `.git\config`, no remote | fails, no agent | fails, no agent | fails, no agent | T2, no agent |
| L4 | `lfs.<url>.standalonetransferagent = x` for the remote, and the `lfs.customtransfer.x` section, in `.git\config` | fails, agent ran | fails, agent ran | fails, no agent | T2, no agent |
| L5 | the two keys in the user config only | fails, agent ran | fails, agent ran | fails, agent ran | fails, agent ran |
| L6 | no such key, the object is in the local store | ok | ok | ok | ok |
| L7 | the two keys in `.git\config`, no `p.bin` and no `.gitattributes` | ok | ok | ok | ok |
| L8 | cell L2 with `"shadowDirOff":true` | fails, agent ran | fails, agent ran | fails, agent ran | fails, agent ran |

- Each failing daemon cell answers `worktree_add_failed`, apart from T1. No worktree folder, no registration and no branch is left.
- T1 is the filter refusal that names `lfs`, with `repository_only_filter`.
- T2 is `git worktree add failed (checkout): this repository's git config cannot be used: it names a transfer program for git-lfs ("lfs.customtransfer.x.path"); remove that setting, or put it into ~/.gitconfig on the remote machine`. In cell L4 it names that key too, not the key of the remote. After build D the words are "a program for git-lfs".
- In cells L1 to L4 the read-tree call of build D has `filter.lfs.process=`, `filter.lfs.required=true` and `filter.lfs.smudge=false`, and git-lfs prints nothing. In cell L7 it has them too and exits 0. In cells L5, L6 and L8 it has no `filter.` pair.
- The texts of `5fd08069` in cells L1 to L5 end with this sentence: `Claude doesn't use ssh, credential, or proxy settings from this repository's own git config. If the download needs one and you trust that setting, copy it into ~/.gitconfig on the remote machine, then try again.` In cell L8 the text has no such sentence.
- In cell L4 the text of `5fd08069` holds `dial tcp: lookup example.invalid: no such host`: git-lfs tried a DNS lookup of the remote host there. The texts of the other two builds hold no such words.
- In cell L6 `p.bin` in the new worktree is the object, on plain git and on each build. Each build leaves `dev\null\post-checkout`, `post-commit`, `post-merge` and `pre-push` in the new worktree. Plain git does not.
- In cell L3 plain git starts no agent. So "no agent" proves nothing in that cell.
- In cells L5 and L8 the reply of build D equals the reply of `89cb6289` byte for byte.
- The other 36 cells of the Windows VM ran again with build D. The reply, the markers and the disk equal `5fd08069` in 34 of them. Cells W2 and W4 differ in the reply.

The help text of that git-lfs gives a program or a command to these settings: `core.askpass`, `lfs.customtransfer.<name>.path`, `lfs.customtransfer.<name>.args`, and `clean` and `smudge` of `lfs.extension.<name>`. `lfs.standalonetransferagent` selects a custom transfer agent by its name. The text has no line for the form `lfs.<url>.standalonetransferagent`.

### claustrum and the filter rule

Four builds of this change ran beside `5fd08069`: build A, build B and build C on the Linux VM and on the Windows VM, and build D on the Windows VM. With the last build on each VM, the reply, the markers and the disk were equal in every cell of this section. The exceptions are in the list of differences below. After build D the code changed in one place: a key that starts with `lfs.extension.` counts as a program key of git-lfs, and the text of that rule says "a program for git-lfs". No VM ran that change.

What claustrum does:

- Before the read-tree call, claustrum runs `git --git-dir=<git dir> config -z --list --show-scope` in the new worktree. The scopes `local` and `worktree` are the config of the repository. Each other scope is outside it. A file that an `includeIf` section of the user config names is outside it, as in cell R1. The listing of the read-tree call follows as before.
- A git that refuses that option with exit code 129 gets two plain `config -z --list` calls in its place, both in the new worktree. The first has `GIT_DIR` set to the null device, and it has no `GIT_COMMON_DIR` and no `GIT_WORK_TREE` of the daemon. So git lists the config outside the repository. The second is the listing of the read-tree call. A git before 2.26 refuses the option so. Cell Q14 has a stub that does, and claustrum refuses the filter there (Linux VM).
- If the listings define a filter and the outside part does not, only the repository defines it. The last value of a key counts.
- If a listing fails for another reason, the request answers `git worktree add failed (checkout): <text of git>` and rolls back. No read-tree call runs.
- If the `timeoutMs` of the caller expires during one of these listings, the answer is the timeout frame of cells S2 (Linux VM).
- A filter that only the repository defines gets the three pairs of `5fd08069`, with the same order and place. The refusal needs one stderr line of the failed read-tree call, for a name that has the pairs. The line starts with `fatal:` and ends with `filter <name> failed`. With no such line, `filter <name> failed` anywhere in stderr counts. Any other failure answers `git worktree add failed (checkout): <text>`, as before.
- `smudge = cat` gets three pairs that turn the filter off: `filter.<name>.process=`, `filter.<name>.required=false` and `filter.<name>.smudge=`. A `cat` first on PATH then does not run (cell Q7, Linux VM). `5fd08069` passes no pair there.
- If only the repository defines the filter `lfs`, the two values of cells V7 and V13 get the pair of `5fd08069`.
- The filter `lfs` does not run if the config of the repository itself holds a program key of git-lfs. Such a key starts with `lfs.customtransfer.` or with `lfs.extension.`, or it starts with `lfs.` and ends with `.standalonetransferagent`. The filter `lfs` then gets the three pairs, wherever it is defined. The same keys in the user config or the system config do not count. This rule is claustrum's own. Cells L1 to L8 measured it (Windows VM). Each refused cell there has a `lfs.customtransfer.` key. Cells L1, L2 and L4 have a standalone key of one of the two forms beside it. The `lfs.extension.` keys are from the help text of git-lfs 3.7.1, and no VM ran them.
- With such a key, a path under an `lfs` filter that only the repository defines gets the refusal that names `lfs` (cell L1). If the user config or the system config defines the filter too, the answer is `git worktree add failed (checkout): this repository's git config cannot be used: it names a program for git-lfs ("<key>"); remove that setting, or put it into ~/.gitconfig on the remote machine`, with `worktree_add_failed`. Cells L2 to L4 have that answer with the words of build D. `<key>` is the first such key in sorted order. With no path under the filter the request succeeds (cell L7).
- A filter can have a definition outside the repository and other values of the three keys in the repository. claustrum then passes pairs with the outside values of `required` and `smudge`. An outside `process` value gets a pair too. So the outside filter runs, as in cells A7, O6 and Q6. `5fd08069` passes no pair there.
- A name over 200 bytes with a value that does not pass is refused before the read-tree call, with the frame of cell D5. A name with `=` is refused there too, with the frame of cell Q3.
- A filter that only the repository defines, with a name that is not valid UTF-8, is refused before the read-tree call on every system. The text is claustrum's own: `git worktree add failed (checkout): this repository's git config cannot be used: the filter "<name>" is defined only there, and its name is not valid UTF-8; define it in ~/.gitconfig on the remote machine, or rename it`, with `worktree_add_failed`. For the bytes `FF 78` the name in the text is `"\xffx"` (cells S3 and X1).
- The rollback of each refusal is the rollback of a failed checkout.
- With `"shadowDirOff":true` the scoped listing does not run and the read-tree call has no pair (cells E1 and L8). Its one other effect in claustrum is on the read of the repository config: see [Repository config refusals](../PROTOCOL.md#repository-config-refusals).
- Only the JSON value `true` is the switch. Any other value of the member is no error, on each git method, and changes no frame of claustrum.

Where claustrum differs from `5fd08069`. Each of these stays:

- Cell Q1 (Linux VM). `5fd08069` runs the outside `smudge` filter and answers ok. claustrum answers `git worktree add failed (checkout): the git filter "m" has values in this repository's git config that no option of git can replace for the checkout`, with `worktree_add_failed`. No filter runs and nothing is left. A `-c` pair cannot remove the `process` key of the repository, and an empty `process` value stops the outside `smudge` filter too.
- Cell Q10 (Linux VM). `5fd08069` answers the filter refusal. claustrum passes no pair and answers `worktree_add_failed` with git's `missing value for 'filter.m.smudge'` text. Nothing is left on both.
- Cells Q2, Q15 and V13 (Linux VM). git itself fails in the read-tree call. The text of claustrum has git's hint about `info/grafts` before the text of `5fd08069`. The markers and the disk are equal.
- Cell Q12 (Linux VM). A `git.info` of claustrum has no `settingsControl` member, as on `89cb6289`.
- Cell R2 (Linux VM). `5fd08069` answers the filter refusal. claustrum answers `not_a_repo` after 2 git calls, as `89cb6289` does.
- Cell R4c (Linux VM). A `~/.gitconfig` that is a directory gets another text on `5fd08069`. claustrum has the text of `89cb6289`. Cells R4a (Linux VM) and W2 (Windows VM) have a syntax error in the config of the repository. claustrum answers the text of `5fd08069` in that state (from the code). The code is `worktree_add_failed` on each build.
- Cells R5a and R5b (Linux VM) and W4 (Windows VM). With a temporary directory that cannot be used, `5fd08069` answers the filter refusal. claustrum fails as `89cb6289` does. Its line equals the line of `89cb6289` byte for byte in cells R5a and W4. In cell R5b only the random number of the folder name differs.
- Cell S1 (Linux VM). With a git that refuses `--show-scope`, claustrum refuses the filter of a conditional include of the user config. `5fd08069` and `89cb6289` run it. That is the accepted edge of an old git.
- Four of the cells S2 (Linux VM). The two builds make other listings, so a wait on a listing that only one of them makes gives "timeout" on that one alone. The table of cells S2 has each row.
- Cells S3 (Linux VM) and X1 (Windows VM): a filter name that is not valid UTF-8. claustrum refuses with its own text and makes no read-tree call, on both systems. On Linux `5fd08069` answers the text of cell S3. On Windows `5fd08069` answers ok and makes the worktree, and no filter runs: its read-tree call gets the name with the code point U+FFFD in place of the byte.
- Cells L1 to L4 (Windows VM): a program key of git-lfs in the config of the repository. `5fd08069` lets git-lfs run, the agent does not run, and the request fails with the text of git-lfs. claustrum answers T1 or T2 and starts no program. Each build leaves nothing on disk.
- The call log. The listings before the read-tree call differ, as the table of cells S2 shows. In some cells the `filter.` pairs of the two differ. The stderr of a refused read-tree call of claustrum has git's hint before the `fatal:` line (Windows VM). No frame differs by that.

Test runs on the Windows VM, with the tests of `gitfilter_test.go`:

- Build build A: 15 tests in 45 runs with no failure, and 126 runs with the subtests.
- Build build B, with `TestHardenedGitCallShape` too: 21 tests in 63 runs with no failure. With the subtests that is 174 runs: 165 passed and 9 were skipped.
- Build build C, with `TestHardenedGitCallShape` too: 27 tests in 81 runs with no failure. With the subtests that is 213 runs: 204 passed and 9 were skipped.
- Build build D: 23 tests in 69 runs with no failure, and 207 runs with the subtests. None was skipped.
- The 9 skipped runs are 3 subtests of `TestHardenedGitCallShape` in each of the two rounds, with these reasons: `a worktreeRoot is refused on Windows before any git call`, `git on Windows accepts a .git/objects file (row H20)` and `Go sorts the environment block on Windows`.

### Not measured

- macOS. No cell of this section ran there, on any build.
- On Windows, the cells that [Windows](#windows) and the git-lfs round do not list. On Linux, the cells of the git-lfs round.
- The code after build D, on any VM.
- A key that starts with `lfs.extension.`, on any build.
- Cell L3 has no control that fires: plain git starts no agent there.
- The settings that git-lfs reads for a download. When the filter `lfs` runs, git-lfs downloads with the repository in view. In cells L1 to L5 the failure text of `5fd08069` says that it does not use ssh, credential or proxy settings of that config. claustrum does not hide those settings from git-lfs. No cell measured a program start through them.
- A working `git-lfs` with a filter `lfs` that only the repository defines and no program key.
- A translated git. The refusal reads an English line of git, and the read-tree call keeps the language settings of the daemon. Under a translated git, claustrum answers the plain checkout failure with `worktree_add_failed`, and no filter runs. The Linux VM has no translation of git, so the state did not run on any build.
- A deadline that expires after git reported a filter failure. Cells Q13 and S2 have a deadline that expires before git answers. The order of the timeout answer and the filter refusal after a filter failure is not measured.
- A key spelled `Smudge` with a marker value. Cell V14 shows only that the value `cat` under that spelling is not refused.
- A listing that waits and holds no child process. In cells S2 the reply came after 3.2 to 3.5 s on each build.
- A filter name with `=` and a value that passes, and such a name with a definition outside the repository.
- A `required` or a `process` key with no value.
- A filter name with a byte that the text changes, other than those of cells D6, Q11 and S3.
- A `shadowDirOff` value other than `true` on `git.list_branches` of `5fd08069`. Cells V3 of the [record of the refusals](git-config-refusals.md) ran such values on `git.info` and `git.status`. Each other effect of that member there. That record has one effect, on the read of the repository config (cells X1 and V2). It also has the member name in upper case, which is the switch on `5fd08069` and on claustrum (cells V3).
- How `5fd08069` tells the config of the repository from the other sources. Its two listings before the read-tree call are plain `config -z --list` calls.
- A listing of the filter rule that fails for a reason other than a deadline, on `5fd08069`.
- A git older than 2.43.0, on any build. Cells Q14 and S1 have git 2.43.0 behind a stub.

### Windows

A Windows VM with Git for Windows and `git-lfs` 3.7.1 ran 23 cells in two passes, on the same three daemons. Both passes are equal. A later round there ran cells Q4, Q12 and Q13. The claustrum build of `main` before this change equals `89cb6289` in every cell. The system git config of that VM defines `filter.lfs.clean`, `filter.lfs.smudge`, `filter.lfs.process` and `filter.lfs.required`.

- These cells equal their Linux rows on both builds, in the reply, the markers and what stays. They are A1, A3, A5, A7, A8, B1, B3, B6, C1, C4, C7, D3, D5, E1, F2, N1, N2 and V1. The report of that round names cell V1 as A6.
- Cell A2: `89cb6289` answers ok and the marker fired. `5fd08069` refuses.
- Cell V8: `89cb6289` answers ok, and `5fd08069` refuses, as on Linux. On `89cb6289` the new worktree holds the `dev\null` files of the last bullet.
- Cell V3 has the value `C:/o/w/prog.sh`, a marker script. `89cb6289` answers ok and the marker fired. `5fd08069` refuses.
- In cell A1, the read-tree call of `5fd08069` has the three pairs first, before 23 more `-c` pairs. In each refused cell the call exits 128 with the one stderr line `fatal: <path>: smudge filter <name> failed`. Cells N1 and N2 have the pairs for `b` and then for `m`.
- A filter that no path uses has the pairs too, and the call exits 0: cells C1 and C7 for `m`, and cell D3 for `M`, as the config spells it.
- Three more cells equal their Linux rows on both builds. They are Q4 (200 and 201 bytes), Q12 (`"true"`, `1`, `null`, `false` and `true`) and Q13 (`"timeoutMs":1`).
- Cells W1: `smudge = cat` with a stand-in `cat.exe` or `cat.cmd` first on PATH. The reply is ok on each build, and the stand-in fired on none, also not with plain git. The shell of Git for Windows has `/usr/bin` before the PATH of the caller.
- Cell W2: a syntax error in `.git\config`. `89cb6289` answers the `config-defined hooks could not be pinned off` text with `fatal: bad config line 10 in file C:\o\w\repo\.git/config`. `5fd08069` answers `this repository's git config cannot be used: fatal: bad config line 10 in "C:\\o\\w\\repo\\.git\\config"`. Both have `worktree_add_failed`.
- Cell W3: a syntax error in the user config. Each build answers `git cannot run on this host; git not run: exit status 128: fatal: bad config line 3 in file C:/o/w/home/.gitconfig`, with `worktree_add_failed`.
- Cell W4: `TEMP` and `TMP` name a folder that does not exist. `89cb6289` answers `git worktree add failed (checkout): GetFileAttributesEx C:\o\w\notemp: The system cannot find the file specified.`, with `worktree_add_failed`. `5fd08069` refuses the filter.
- In cells W2, W3 and W4 nothing is left on disk, on each build.
- Cell X2: the name `ä`. `89cb6289` answers ok and the marker fired. `5fd08069` answers the refusal with `\"ä\"`.
- Cell X3: 60 filters of `.git\config` with names of 190 bytes. `89cb6289` answers ok and the marker fired. `5fd08069` answers `git worktree add failed (checkout): fork/exec <git>: The filename or extension is too long.`, with `worktree_add_failed`. No read-tree call runs there, and nothing is left. claustrum equals `5fd08069` in cells X2 and X3, and in cell S5 of the Linux VM.
- In cell D5, `5fd08069` makes no read-tree call. It makes 18 git calls there, and 19 in a refused cell.
- `5fd08069` makes 12 git calls where it answers ok, 10 in cell E1, and 2 in cell F2. `89cb6289` makes 10 in every cell, and 2 in cell F2.
- Cells V7 and V13 are ok on all three daemons. The filter `lfs` has a definition in the system config there. The read-tree call of `5fd08069` has the pair `filter.lfs.smudge=git-lfs smudge -- %f` in cell V7 and `filter.lfs.process=git-lfs filter-process` in cell V13.
- After cells V7 and V13 on all three daemons, the new worktree holds `dev\null\post-checkout`, `post-commit`, `post-merge` and `pre-push`. After cell V8 it holds them on `89cb6289`. Plain git leaves no `dev` folder. The cause is not measured.
