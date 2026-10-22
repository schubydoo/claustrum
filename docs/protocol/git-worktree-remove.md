# git.worktree_remove
`{baseRepo,worktreePath[,branchName][,worktreeRoot]}` → `{"success":true}` (lenient), or `{"success":true,"branchKept":true}` when the branch step keeps the branch

- Since `f6010b97` the git-directory trust check runs on `baseRepo` only, before
  git runs. Without `worktreeRoot` it runs after the containment tests, the
  look into `baseRepo` and the leaf check below. When the worktree directory exists, a
  refused git directory and "no repository" both answer the lock-check refusal of
  the [error-string catalog](../PROTOCOL.md#error-string-catalogue).
  After a refusal the daemon deletes nothing. The worktree directory, its entry and
  its branch all stay. Damage to the entry of the worktree being removed does not
  stop the removal. See [Git-directory trust check](../PROTOCOL.md#git-directory-trust-check).
- A config of `baseRepo` that the daemon does not accept refuses the removal, and
  nothing is deleted. Without `worktreeRoot` the text ends with `is locked:
  <text>`, with no `; retry`. With `worktreeRoot` the text is the work-tree
  refusal below with `<text>` as the reason. See
  [Repository config refusals](../PROTOCOL.md#repository-config-refusals).
- A failed start test, a bad answer of the call about the repository, and a copy
  folder that cannot be made refuse the removal too. Nothing is deleted, and the
  text ends with `is locked: <text>`. If git names no repository in that call, the
  answer is the lock-check refusal. A remove makes that call
  two times after a bad answer. See
  [The settings copy](../PROTOCOL.md#the-settings-copy). With `worktreeRoot` claustrum gives
  each of those texts as the reason of the work-tree refusal below. With git 2.56
  `5fd08069` does so for a bad answer (round `tier0-27`, cells GF and N04, Linux
  VM). Other
  shapes are not measured.
- With `worktreeRoot`, the daemon determines the repository's work tree after the
  containment checks and before the dir-symlink check. A failure answers
  `{"success":false,"error":"failed to remove worktree: cannot determine the
  repository's work tree: <reason>"}`, with no `errorCode`, and nothing is deleted.
  It comes before the "already gone" success, so a missing worktree gets it too.
  The reasons, in order:
  1. Git cannot start in `baseRepo`. It is a regular file, a directory without
     search permission (mode 0600), or a path that the kernel does not follow.
     `<reason>` is the hooks refusal with the
     start error, for example `…listing the configuration in force: fork/exec
     /usr/bin/git: permission denied` or `…: not a directory`. The path is the git
     that `PATH` resolves. `git version` runs after that listing (`89cb6289`, row
     L16b). If it fails too, the text starts with `git cannot run on this host`
     instead, which is not measured here. A `baseRepo` behind a chain of 45
     symlinks, which the kernel does not follow, gets this reason too. Its text ends
     with `chdir <baseRepo>: too many levels of symbolic links`, and nothing is
     deleted (`89cb6289`, rows LNKb-g and LNKr-g on a Linux VM). The one git call
     there is the read of the user excludes. Not measured: chain
     lengths other than 5 and 45, absolute link targets, a chain to a plain folder
     and a chain to nothing. Nor are a chain with a locked or marked worktree, a
     chain without `worktreeRoot`, and a path that is too long for the kernel.
  2. The trust check refuses the git directory of `baseRepo`. `<reason>` is the
     refusal text M2 to M5 or S1 to S3. A plain folder as the target gets this text
     too.
  3. The trust check finds no repository. `<reason>` is `exit status 128`. Examples
     are a plain directory, an empty nested `.git` directory, a `.git` file with no
     `gitdir:` line, and a linked worktree whose entry is gone. A `.git` file whose
     target is gone and is not a worktree entry goes on to the next step instead.
     On Linux and macOS, a configuration listing that fails there gives the refusal
     text of its failure class as `<reason>`. `89cb6289` answers so with an empty
     `GIT_CONFIG_KEY_0` of the daemon (rows A-N5b and A-N5c on Linux and macOS VMs,
     row A-N5 on a Linux VM).

     On claustrum the read of the repository config comes here. Its refusal text
     is `<reason>` (`5fd08069`, cells N14, Linux and macOS VMs).
  4. Git cannot list the configuration. `<reason>` is the refusal text of the
     failure class. When the listing says git finds no repository, `<reason>` is `git
     finds no repository here: <git text>`. With git older than 2.56 a `.git` file
     with a gone target gets it with `fatal: not a git repository: <target>`
     (`89cb6289`, row N04 on Linux and macOS VMs). `f6010b97` gave the hooks refusal
     text there. With git 2.56 it gets the text of a failed call about the
     repository (round `tier0-27`, cells GF and N04, Linux VM).
  5. `baseRepo` is itself a git directory, such as a bare repository or a `.git`
     directory. `<reason>` is `exit status 128`. A repository whose config sets
     `core.bare` still passes.
  6. The hardened `git rev-parse --absolute-git-dir` fails in `baseRepo`. `<reason>`
     is its exec error, for example `exit status 128`. A daemon `GIT_DIR` that names
     nothing is one example.

  With no `git` on `PATH`, claustrum gives a `baseRepo` that exists one reason
  before these six. It is the exec error alone, `exec: "git": executable file not
  found in $PATH`. Both references answer so in nine rows. One is a git directory
  that the trust check refuses (`89cb6289`, row L13t on a Linux VM). Class 1 of the
  failed listing gives the other rows and the place of the `GIT_CONFIG_COUNT` check
  (see [Git-directory trust check](../PROTOCOL.md#git-directory-trust-check)). The other examples of
  reason 3 are not measured with no `git`.

  A `baseRepo` that does not exist gets no work-tree refusal, and no git call runs
  for this step. That holds with `git` on `PATH` and without it (`89cb6289`, Linux
  VM). The removal then goes on. Each request sent a `branchName`. The answer
  depends on the worktree directory:
  - It is gone, and the `<directory>` level exists (row L13wb), or the root exists
    and is empty (row L13wc): `{"success":true,"branchKept":true}`. The twins with
    `git` answer the same.
  - It is gone, and the root does not exist (row L13wr): the "is not reachable"
    refusal. The twin with `git` answers the same.
  - It holds a file and no `.git` file. With `git` it gets the "is not a worktree of"
    refusal (row L13wa-g). With no `git` it gets `could not check whether
    <worktreePath> is locked (the repository at <baseRepo> could not be read); retry`
    (row L13wa).
  - It is empty. With `git` the directory is removed, and the answer is success with
    `branchKept` (row L13we-g). With no `git` the lock-check text above answers, and
    the directory stays (row L13we).
  - Its `.git` file names an entry of the missing repository, and it holds other
    files. With `git` the directory is deleted, and the answer is success with
    `branchKept` (row DG1-g). With no `git` the lock-check text above answers, and
    the directory stays (row DG1). `f6010b97` does the same, with no `branchKept`.

  Any other stat error of `baseRepo` keeps the six reasons and the no-`git` answer.
  claustrum runs no `GIT_CONFIG_COUNT` check then (not measured).
  A `baseRepo` that is not a directory gets reason 1. Each of the six reasons and
  the order around this step were measured side by side against `f6010b97` on a
  Linux VM.
- By default, with no `worktreeRoot`, `7d193f89` confines the removal to inside the
  repository. `worktreePath` must be absolute, carry no `..` component, and sit
  strictly under `baseRepo`. Otherwise the reply is `{"success":false,"error":"refusing
  to remove worktree: <p> …"}`, with no `errorCode`, and with the same three reasons
  as `worktree_create`. The "not inside the repository" text names `baseRepo` as
  sent. An absent or empty `baseRepo` gives the empty string, so a space comes before
  the semicolon (`f6010b97`, Linux and macOS VMs). A Windows VM measured an absent
  one. An empty `worktreePath` is
  `{"success":false,"error":"failed to remove worktree: \"\" does not name a
  directory"}`. Only a path that passes these tests reaches the delete below.
- With `worktreeRoot`, the path must sit two levels under that root, under the same
  external containment as `worktree_create`. An empty `worktreePath` is a relative
  path there. After the `worktreePath` checks and before the two-level check,
  `baseRepo` must be absolute and carry no `..` component. The refusal uses the
  `worktreePath` texts and names `baseRepo` as sent, for example
  `{"success":false,"error":"refusing to remove worktree: <baseRepo> contains a \"..\" component; choose the session folder by its absolute path, without \"..\""}`.
  An absent `baseRepo` is a relative path with the empty string as its name. No git
  runs before these refusals. Under a refused daemon `GIT_CONFIG_COUNT`, a `..`
  `baseRepo` still gets the `..` text. Measured against `f6010b97` on Linux and
  macOS VMs. The `..` test is by whole component: `<T>/sub/..` is refused, and
  `<B>/d..d/T` and `<T>/.` pass (Linux and macOS VMs).
- With `worktreeRoot`, a root that is the file system root `/` is refused with no
  git call, and nothing is deleted. The [error table](../PROTOCOL.md#error-string-catalogue) gives the frame and the cells.
- With `worktreeRoot`, more tests refuse the root, and nothing is deleted. The error
  table gives each text and its cells. Cells C-S1 to C-S3 of round `tier0-20`
  refuse before any git call and cells C-S4 to C-S8 after `worktree list`
  (`5fd08069`, Linux VM). The order inside each group is claustrum's (not
  measured). claustrum runs the `<directory>` symlink check before these tests (not
  measured). A root that holds the repository or a linked worktree passes (rows
  Q15s and Y7). So does a root in another repository beside it (rows Q20 and Q20b).
  If `worktree list --porcelain -z` fails, the daemon runs
  `worktree list --porcelain`. If that fails too, nothing is deleted (`89cb6289`,
  row DG2s-g, Linux VM). A `baseRepo`
  that does not exist runs neither call.
- The managed-worktrees refusal skips a `baseRepo` that does not exist. Measured
  against `f6010b97` on a macOS VM.
- claustrum's own trust-root test also gives the managed-worktrees refusal. It fails a
  `baseRepo` that `os.Stat` does not report as missing and that Go's
  `filepath.EvalSymlinks` cannot resolve. The test is fitted to the measured rows of
  `f6010b97`, and every measured row fits it. On Linux and macOS, rows G1, G2 and G4
  got the refusal. They send `<T>/a.txt/..`, `<T>/loop/..` (a link to itself) and a
  path through a directory with mode 000. There `os.Stat` fails with ENOTDIR, ELOOP or
  EACCES. On Linux and macOS, `<T>/missing/..` and `<T>/dl/..` (a dangling link) are
  missing, and skip the test (rows A1 and G3). On Windows `<T>\missing\..` stats,
  because Windows removes `missing\..` by name, but it does not resolve. The Windows
  VM rows K1, D1, D2, D4, J1 and J3 fit this rule too. They put a missing name, a
  regular file or a dangling directory symlink before `..`, or a junction mid-path. No
  git runs, and nothing is deleted. The refusal comes before the `worktreePath`
  checks, the `worktreeRoot` refusal of Windows and the check of the daemon's
  `GIT_CONFIG_COUNT`. Only the Windows VM measured that order (round 1 rows O1 to O4,
  WR1 and A1 P1).
- The daemon runs no `git worktree remove` and no `git worktree prune`. It deletes the
  worktree directory itself, then the entry of the worktree under `<git dir>/worktrees`.
  Without `worktreeRoot`, the entry stays for a `baseRepo` that does not exist.
  Without `worktreeRoot` and with a daemon `GIT_DIR` and `GIT_COMMON_DIR`, the entry
  that goes is in the repository that `GIT_DIR` names. The rows are below.
  `<git dir>` is the repository git directory that the trust check pinned for
  `baseRepo`. For a main repository that is `<baseRepo>/.git`. For a linked worktree
  it is the git directory of the main repository. For a subdirectory of a repository
  and for a submodule it is the git directory of that repository. With a daemon
  `GIT_DIR` alone, it is the directory that `GIT_DIR` names. The `worktrees`
  directory itself stays. After its last entry goes, it stays as an empty
  directory. The rules below were measured side by side against `f6010b97` on
  VMs. On macOS all 107 cases are equal. On Linux every remove case is equal. On
  Windows every case is equal except the junction rows of D19. The full Windows
  set was measured on an earlier build, and the fixed rows again on the shipped
  one. A sentence marked "not measured" has no row behind it.
- Without `worktreeRoot`, these rows of `89cb6289` set which entry goes. T is the
  repository of `baseRepo`, S is its worktree `s0`, and X is another repository.
  - The daemon has `GIT_DIR` and `GIT_COMMON_DIR` of X in its environment. The
    folder of S goes. The entry `s0` of T and the branch `s0` of T stay. Probe row
    1 shows that on Linux, macOS and Windows VMs, and battery row E13 on Linux and
    Windows VMs.
  - The same environment, and the daemon created the worktree `w1` itself. git
    registered `w1` in X. The removal deletes the entry `w1` of X and the branch
    `w1` of X (row 2, the same three systems).
  - The same environment, and X holds an entry `zz` whose `gitdir` record names
    `<S>/.git`. The entry `zz` of X goes, and the entry `s0` of T stays (row 3, the
    same three systems). The entry `zz` was made by hand.
  - The same environment, and X holds a merged branch `s0`. The branch `s0` of X
    goes. The entry and the branch `s0` of T stay (row 4, the same three systems).
  - The daemon has `GIT_COMMON_DIR` of X alone. The entry `s0` of T goes (row 5,
    the same three systems). The same holds on Linux and macOS VMs in three more
    rows. In row p2a T is a submodule. In row p2b `baseRepo` is a subfolder of T.
    In row p2c `baseRepo` is bare. The branch result of these rows and of row 5
    depends on the commits of the fixture. In rows p2a to p2c `89cb6289` keeps the
    branch on the Linux VM (7 calls) and deletes it on the macOS VM (9 calls).
  - The daemon has `GIT_DIR` of X alone. The entry `s0` of T stays (row 6, the same
    three systems).
  - The daemon has `GIT_DIR` of X and `GIT_COMMON_DIR` of a third repository Y. The
    entry `s0` of T stays. An entry of X that names S goes, and such an entry of Y
    stays (rows p3, p3b and p3c on Linux and macOS VMs, row p3b on Windows too).
  - Two removals at once with the environment of row 1, each for an entry in X.
    Both folders and both entries go (row p5 on a Linux VM, 10 runs). `89cb6289`
    makes 22 git calls there and claustrum 21: `89cb6289` reads the user excludes
    in each request, and claustrum reads them once.
  - `baseRepo` does not exist as sent. The folder of S goes and its entry stays.
    The one git call is the read of the user excludes. Row 7c sends
    `<T>/missing/..` with a `branchName` on Linux and macOS VMs, and the reply
    carries `branchKept`. Battery rows A1, A3 and G3 send `<T>/missing/..`,
    `<F>/missing/../T` and `<T>/dl/..` (a dangling link) with no `branchName` on a
    Linux VM. A folder that is already gone is not measured. With no git on
    `PATH`, the answer is the lock-check refusal, and nothing is deleted. Rows p1
    and p1b show that for `<T>/missing/..` and `<T>/dl/..` on Linux and macOS VMs.
  - `baseRepo` holds an empty `.git` folder and lies in an outer repository, and
    the folder is gone. The reply carries `branchKept`, and the branch of the
    outer repository stays (row p4 on Linux and macOS VMs).
  - A worktree locked in the `.git` folder of `baseRepo` is refused. That is
    divergence D22: see the lock rule below.
  - On Windows, both paths go through a `subst` drive, or through a junction to
    the repository. The folder and its entry go (probe row D-subst, battery rows
    J2, Q1 and Q2). The claustrum test of the junction row runs on Windows only.
  - The request is in 8.3 short names on Windows, or in another letter case on
    macOS. The folder and the branch go, and the entry stays (rows D-83 and D-case).
- The git calls of a removal without `worktreeRoot`, as the call logs of `89cb6289`
  show them. Each row below ran on Linux, macOS and Windows VMs unless its system
  is named. "The check" is a heavy `config -z --list` and
  `rev-parse --absolute-git-dir` in `baseRepo`. `<G>` is the answer of the check.
  "The pair" is two calls in `<G>`. The first is `--git-dir=<G> config -z --list`.
  The second is `--git-dir=<G> --work-tree=<baseRepo> rev-parse --show-toplevel`.
  - A removal in a `baseRepo` that exists starts with the check, after the read
    of the user excludes. A worktree whose `.git` file names its own
    entry under `<G>/worktrees` gets no other call before the branch step (rows K0,
    2 and 5, and row 9 on Linux and macOS).
  - A `.git` file that names a git dir which is not such an entry gets the check
    once more (rows 1, 3, 4, 6, 12 and 14).
  - An entry under `<G>/worktrees` whose record names another path gets the pair
    first, then the check (row 13, and row D-83 on Windows). On Windows the pair
    alone follows through a `subst` drive, and the entry goes (row D-subst).
  - If `<G>/worktrees` exists, the request makes the pair (rows 3, 11, 12, 13, 14,
    15 and 15b, row K1 on Linux, row R17a on Linux and macOS). An empty directory
    gets it too (battery row W01 on Windows). If the directory is missing, the
    request does not make the pair (rows 1, 4 and 6). claustrum runs the pair
    before it looks at the entries by path.
  - If no entry names the folder, the request makes the pair once more (rows K1
    and K1b on Linux, battery row E13 on Linux and Windows, row D-83 on Windows).
    claustrum runs it after the delete of the folder. With the folder already
    gone, the request makes the pair twice too (battery rows W01 to W07-b, W14,
    W15 and W16 on Windows).
  - A `baseRepo` that holds no repository gets the check twice before the
    lock-check refusal (row A5). With the folder gone, it gets the check once (row
    16). Each call there carries `GIT_DIR=<null device>`, the calls of the branch
    step too. Rows 16, A5, R18, R18b and N00 show that on Linux and macOS VMs, and
    rows 16, A5 and N00 on a Windows VM. With the folder gone, a listing that fails
    refuses: `could not check whether <worktreePath> is locked (<text>); retry`.
    `89cb6289` then makes the listing and `git version` (row A-N4 on Linux and
    macOS VMs). In cell P3-rmN on a Windows VM the excludes read comes before the
    two. That request is the first of its daemon.
  - A `baseRepo` whose `.git` file names a git dir that is gone, with the folder
    gone: the request makes two listings, and each fails. Both carry
    `GIT_COMMON_DIR=<that git dir>` (row N04 on Linux and macOS VMs).
  - A daemon `GIT_DIR` that names a file, with the folder gone: the reply carries
    `branchKept`, and the request makes no git call (row N05a on Linux and macOS
    VMs).
  - A git directory that the trust check refuses, with the folder present: the
    one git call is the read of the user excludes (row DG2i-g on a Linux VM).
  - claustrum's tests compare the calls before the branch step with these rows:
    the working directory, the profile, the arguments and the `GIT_DIR` and
    `GIT_COMMON_DIR` values. For the branch step they compare the count of calls.
- With `worktreeRoot`, the call log of `89cb6289` shows `rev-parse
  --absolute-git-dir` before `worktree list`, and the check once more after it.
  That second check does not run for a `worktreeRoot` below a folder of mode 000. That
  request answers after 7 calls (row T6 on a Linux VM). For a `baseRepo` that
  holds no repository, the request makes a light listing and `rev-parse
  --show-toplevel`. Both carry `GIT_DIR=<null device>` (rows L13z-g and DG2h-g on
  a Linux VM). For a `.git`
  file that names no verified entry, the calls follow the rules above (rows Q20,
  Q20b and Y8w on a Linux VM). The pair carries `--work-tree=<worktreePath>` there
  (rows Q20b and Y8w). Nothing is deleted in those rows.
- On Windows, a junction at a directory between `baseRepo` and the leaf is refused.
  Nothing is deleted, and the branch stays. The reply is
  `{"success":false,"error":"failed to remove worktree: openat <path from
  baseRepo>: path escapes from parent"}`. `<path from baseRepo>` is the folder that
  holds the leaf, relative to `baseRepo`. That text is claustrum's own. From the
  code: claustrum opens that folder in one `os.Root` step, and the text is the
  error of that open. For a junction at `.claude` or at `.claude\worktrees` the
  path is `.claude\\worktrees` in the frame. `f6010b97` answers `{"success":true}`
  there, deletes nothing, and deletes the branch. The whole-build check of issue
  442 measured `89cb6289` the same (Windows rows JC, J03 to J05, JCR1 and JCR2),
  for a branch that another ref reaches. A branch tip that no other ref reaches is
  not measured there. Two cells have a junction `<P>\J` above `.claude`, with the
  `worktreePath` `<P>\J\sub\.claude\worktrees\w1` and `<P>\J\.claude\worktrees\w1`.
  `89cb6289` answers `{"success":true}` there after 2 git calls, a listing and
  `rev-parse --absolute-git-dir`. The claustrum build of this change answers the text with
  `J\\sub\\.claude\\worktrees` and with `J\\.claude\\worktrees`, and runs no git
  call. In both cells the leaf did not exist and no branch existed. Nothing
  changed on disk on either side (Windows VM, cells J-b-wt-pj and J-b-wt-pJ). Not
  measured: a live worktree behind such a junction, and the branch step there. See
  [`DIVERGENCES.md`](../DIVERGENCES.md) → D19.
- The last component of `<p>` is checked first. In the repository no git runs before
  this check. A symbolic link answers `{"success":false,"error":"refusing to remove
  worktree: <t> is a symbolic link, not a worktree directory"}`. A file that is not a
  directory answers `refusing to remove worktree: <t> is not a directory`. Nothing is
  deleted. `<t>` is `<p>` with the symbolic links of `baseRepo` resolved. With
  `worktreeRoot`, the links of the root are resolved instead. Under
  macOS `/tmp`, `<t>` therefore reads `/private/tmp/…`. Before, a link to a sibling
  worktree made claustrum delete that sibling, its entry and its branch.
- The entry comes from the `.git` file of `<p>`. The file must hold `gitdir: ` and a
  path of the form `<something>/worktrees/<name>`. The entry `<git dir>/worktrees/<name>`
  must be a directory. Its `commondir` must lead back to `<git dir>`, or without
  `worktreeRoot` to the git directory that `rev-parse --absolute-git-dir` answered in
  `baseRepo`. Its `gitdir` record must name `<p>`. Without `worktreeRoot`, a record in another spelling of
  `<p>` passes too: `<p>` with its `baseRepo` part as git names that folder. So a
  request through a `subst` drive or a junction drops its entry (Windows VM, rows
  D-subst, J2, Q1 and Q2). No row tests each of these steps on its own. The path match is exact, so on macOS a spelling of `<p>` that
  differs only in letter case does not match. On Windows the match ignores letter
  case and slash direction, because git records its paths with forward slashes there.
  An 8.3 short name does not match on Windows, so such a remove keeps the entry. Rows
  C01 and C02 measure the case rule. Rows N83_leaf and N83_base measure the 8.3 rule.
- If a verified entry holds a `locked` entry of any kind, it is locked. A dangling
  symbolic link counts. Without a verified entry, the daemon checks every entry whose `gitdir`
  record names `<p>`. If the worktrees directory exists but cannot be read, the reply
  is the lock-check refusal above. A locked worktree answers the
  `is locked (git worktree lock)` refusal, and nothing is deleted.
- claustrum refuses a worktree that is locked in the `.git` folder of `baseRepo`.
  That is divergence D22. The one exception is cell L1, in the paragraph of a gone
  worktree below. In four states `89cb6289` answers success for such a
  worktree (rows p6, p6d, p6e and p6f, see the entry). claustrum answers the
  locked refusal above, and in row p6f the gone-and-locked refusal below. If the
  removal reads its entries from another git directory, claustrum also reads the
  entries under `<baseRepo>/.git/worktrees`. It makes no git call for that, with
  or without `worktreeRoot`. If that folder exists and cannot be read, the reply is
  the lock-check refusal. With `worktreeRoot` and a lock in T, `89cb6289` deletes
  nothing (rows q2 to q4 on a Linux VM). In rows q2 and q3 it answers the
  transient `could not verify` reply below, with the detail `open
  <X>/.git/worktrees: no such file or directory`. In row q4 it answers
  `{"success":true}`. claustrum answers the
  locked refusals there. A `baseRepo` with no `.git` folder of its own, such as a
  subfolder of T, a linked worktree or a submodule, is not covered. With a daemon
  `GIT_DIR` of another repository, claustrum deletes a worktree that is locked
  there, as the reference does in row p6. A lock on an entry of X that names the
  worktree is refused on both sides (row p6b). So is a lock with no daemon
  environment (row p6c on Linux, macOS and Windows VMs, row p6g on a Linux VM).
  See [`DIVERGENCES.md`](../DIVERGENCES.md) → D22.
- The delete first removes each entry of `<p>` except `.git`, in the order of the
  directory read. It stops at the first failure. Then it removes the rest, `.git`
  included, and `<p>` itself. Every delete goes through an `os.Root`, so no delete
  follows a symbolic link out of `<p>` or out of its parent. That is a property of
  `os.Root`, not measured. A failure answers `{"success":false,"error":"failed to
  remove worktree: RemoveAll <entry>: <errno text>"}`, for example `RemoveAll zz:
  permission denied`. If an entry other than `.git` fails, that entry, every entry
  after it, and `.git` stay. If the removal of `<p>` itself fails at the end, `<p>`
  stays empty and `.git` is gone. The entry of the worktree and the branch stay in
  both cases. Both cases are measured on macOS, Linux and Windows.
- After the tree, the daemon deletes a verified entry. If that delete fails, the reply
  is the `could not drop its registration` text of the [catalog](../PROTOCOL.md#error-string-catalogue), and the branch
  step still runs.
  A kept branch adds `"branchKept":true` after `error` (rows R19 and B2-06, Linux VM).
  Without a verified entry, the daemon deletes the one entry whose record names `<p>`.
  If two entries match, or a match is locked, it deletes nothing. It does not
  report a failure of that delete (not measured). A plain directory inside the repository is
  therefore deleted, and the reply is `{"success":true}`. Treat `worktreePath` as a
  path you ask the daemon to remove, not as a filter.
- With `worktreeRoot`, a `<p>` without a verified entry is removed in two cases only.
  An empty directory is removed, and the request then goes on as for a gone worktree.
  A stale worktree of `baseRepo` is removed too. Its `.git` names an entry of
  `<git dir>` that is gone, or the whole `<git dir>/worktrees` is gone. A lock by path
  is checked before both cases. Only the lock before a stale or damaged worktree is
  measured. Any other path is refused and left in place, with
  `{"success":false,"error":"refusing to remove worktree: <p> is not a worktree of
  <repo> (<reason>), so it is left in place; remove it by hand if it is a
  leftover"}`. Here `<p>` is the path as sent, a trailing slash included. `<reason>`
  is one of `<t> has no .git file`, `<t>/.git is not a regular file`, `<t>/.git does
  not name a git dir`, `<t> carries a .git file that does not name this repository's
  own worktree admin directory`, or `<t> carries a .git file naming an admin directory
  whose own record is of a different worktree`. Another read error gives the
  transient reply `{"success":false,"error":"failed to remove worktree: could not
  verify that <p> is a worktree of <repo> (<detail>); retry"}`. There `<p>` is the
  cleaned path. Two inputs are measured, with `git` on `PATH`, on a Linux VM. Row
  DG1c-g is a missing `baseRepo` and a `.git` file that names an entry of another
  missing repository. `89cb6289` sends this frame there, and nothing is deleted.
  The `<detail>` is the hooks refusal with `chdir <baseRepo>: no such file or
  directory`. The one git call is the read of the user excludes. claustrum sends
  that `<detail>` for every such remove with a `baseRepo` that does not exist. Only
  row DG1c-g measures it. Row Q20 is a `baseRepo` that exists with no `worktrees`
  directory. The `<detail>` is the error of the open there: `open
  <baseRepo>/.git/worktrees: no such file or directory`. A refused daemon `GIT_CONFIG_COUNT` changes these answers (see
  [The daemon's own git environment](../PROTOCOL.md#the-daemons-own-git-environment)).
- For a worktree whose directory is gone, the daemon checks the registration by path.
  A locked one answers the `is gone but its registration is locked` refusal. Otherwise the one entry whose record names `<p>` is
  deleted. Here the `baseRepo` part of `<p>` also counts in its resolved form, with
  junctions and 8.3 names resolved on Windows. The rest of `<p>` counts as sent. So a
  `baseRepo` sent in 8.3 form or through a junction still finds a locked registration
  when the worktree and its parent are gone (rows GL1 and GL2, with GL0 as the
  control). With `worktreeRoot`, a root that does not exist is refused before
  this path. A worktrees directory that cannot be read
  does not stop this path: with `worktreeRoot` and no daemon environment the reply
  is `{"success":true}` (row K13, Linux VM). On Linux and macOS, nor does a symbolic link, with a
  locked entry behind it (cell L1 of round `tier0-26`, see the [catalog](../PROTOCOL.md#error-string-catalogue) row). If the removal reads its entries from
  another git directory and `<baseRepo>/.git/worktrees` cannot be read, the reply
  is the lock-check refusal (D22). Without `worktreeRoot` the case
  is not measured. Without `worktreeRoot`, two more answers
  come first. A configuration listing that fails, or a refused git directory, answers
  the `is locked (<reason>); retry` refusal.
  `<reason>` is the hooks refusal, the `git cannot run` text, a limit refusal or the
  trust refusal text. A listing that says git finds no repository skips the
  registration instead, and so does a `PATH` with no `git`. The reply is then success,
  with `branchKept` when the branch step keeps a branch (see
  [Git-directory trust check](../PROTOCOL.md#git-directory-trust-check)). A `baseRepo` that holds
  no repository answers `{"success":true}`, and so does one that does not exist.
  The branch step runs on this path too (rows R17a and R17b). Without a repository its
  for-each-ref fails, so a request that names a branch gets `"branchKept":true`
  (rows R18 and R18b, Linux VM). A `baseRepo` that does not exist gets the member
  too, and its only git call is the excludes read (row X3, Linux VM). There
  `f6010b97` answers a bare `{"success":true}`.
  A refused daemon `GIT_CONFIG_COUNT` changes these answers (see [The daemon's own
  git environment](../PROTOCOL.md#the-daemons-own-git-environment)).
- The branch goes last, through [the branch step](../PROTOCOL.md#the-branch-step). A kept branch adds
  `"branchKept":true` after `success`, or after `error` when there is one (rows R02
  and R19). The member is only ever true. The step adds no text to the reply. A
  `branchName` that is empty or starts with `-` or `+` is skipped. A request whose
  branch the listing and rev-parse show absent answers a bare `{"success":true}`.
  That is what "lenient" means here.
- Without `worktreeRoot`, a `baseRepo` that the daemon can open but not search
  (mode 0600) answers `{"success":false,"error":"failed to remove worktree: statat
  .claude: permission denied"}`. One that it cannot open at all (mode 0000) answers
  `failed to remove worktree: open <baseRepo>: permission denied`. Nothing is
  deleted in either case. Measured against `f6010b97` on Linux and macOS VMs.
  On Linux and macOS the `statat` text names the first component of `<p>` below
  `baseRepo`. For a `baseRepo` of mode 0600, `89cb6289` answers `statat wt` for
  `<baseRepo>/wt` (cell U19 on Linux and macOS VMs), also with the folder gone
  (cell N4, Linux VM) and with mode 0400 (cell N5, Linux VM). It answers `statat
  a` for `<baseRepo>/a/wt` and `<baseRepo>/a/b/c/wt` (cells N6a and N6b, Linux
  VM), and `statat .claude` for a path under `.claude` (cell N7a, Linux VM). On
  Windows claustrum names `.claude`. Windows is not measured.
- On Linux and macOS, a folder that the daemon cannot read or search is refused.
  With `worktreeRoot`, that holds from the root down to `<p>`. Without it, that
  holds between `baseRepo` and `<p>`. Nothing is deleted: the worktree, its entry and the
  branch stay. Each text below follows `failed to remove worktree: `. `89cb6289`
  sends the same frames on Linux and macOS VMs. The daemon was not root.

  | level and mode | text | rows |
  |---|---|---|
  | `worktreeRoot` 0300, 0100, 0000 or 0200 | `open <root>: permission denied` | B3, B4, B5, B6 |
  | `worktreeRoot` 0600 or 0400 | `statat .: permission denied` | B1, B2, B18x (macOS VM only) |
  | `<directory>` 0300, 0100 or 0000 | `openat <directory>: permission denied` | B10, B11a, B11b |
  | `<directory>` 0600 or 0400 | `statat .: permission denied` | B8, B9 |
  | the worktree folder 0300, 0100 or 0000, with `worktreeRoot` | `openat <name>: permission denied` | B14a to B14c |
  | the worktree folder 0600 or 0400, with `worktreeRoot` | `statat .: permission denied` | B13a, B13b |
  | `.claude` 0600, without `worktreeRoot` | `statat .: permission denied` | B17a |
  | `.claude` 0300, without `worktreeRoot` | `openat .claude: permission denied` | B17b |
  | `.claude/worktrees` 0600, without `worktreeRoot` | `statat .: permission denied` | B17c |
  | `.claude/worktrees` 0300, without `worktreeRoot` | `openat worktrees: permission denied` | B17d |

  `<root>` is `worktreeRoot` with its symbolic links resolved. A request that
  spells `/tmp/…` on macOS gets `/private/tmp/…` in the text (row B18, macOS VM only). `<directory>`
  and `<name>` are one path component each. A `worktreeRoot` of mode 0500 does not
  stop the removal (row B7), and a `.claude` of mode 0500 does not either (cell
  U12). If the folder above `worktreeRoot` has mode 0600, the text is `lstat
  <root>: permission denied` (row B16). With mode 0300 or 0100 on that folder,
  the removal succeeds (cells U16a and U16b).

  A second round measured more states against `89cb6289` on Linux and macOS VMs.
  Nothing is deleted in the states of this list.
  - With two restricted levels, the first one from the top gives the text (cells
    U7 to U10b).
  - A worktree folder, a `<directory>` or a `.claude/worktrees` that is gone
    under a restricted level gets the text of that level. The entry and the
    branch stay (cells U1 to U6b).
  - The other modes of `.claude` and `.claude/worktrees` follow the table: 0000,
    0100 and 0200 give `openat`, and 0400 gives `statat .` (cells U11a to U11h).
  - A `worktreeRoot` of mode 0300 answers `open <root>` before these refusals: a
    `<directory>` that is a symlink (cell U13b, and cells N1a to N1c on a Linux
    VM for modes 0100, 0000 and 0200), a locked worktree (cell U14),
    and a worktree path that is a symlink or a file (cells U15a and U15b). With
    mode 0600 and a symlinked `<directory>`, the text is `statat .` (cell U13a).
  - A worktree at `<baseRepo>/a/wt` or `<baseRepo>/a/b/c/wt` gets the same texts
    for a restricted `a` or `b`: `openat a`, `openat b` or `statat .` (cells U17a
    to U18b).
  - A `worktreeRoot` that another user owns, with mode 0711 or 0751, answers
    `open <root>` (cells U20a and U20b). On a macOS VM, an ACL on the root that
    denies `list` to the daemon user does the same (cell U21).

  A daemon that runs as root removes the worktree under a `worktreeRoot` of mode
  0300, as `89cb6289` does (cell U22, Linux VM only). Windows is not measured. On
  Windows claustrum opens `.claude\worktrees` in one step, as before.
- A home-directory `worktreePath` is refused. The `7d193f89` containment now does
  it, as parity. A `~`-expanded home path is not strictly under `baseRepo`, so it is
  refused with the reference's `"…is not inside the repository…"` wording before any
  delete. The claustrum-only D2 frame (`"worktreePath must not be or contain
  the home directory: …"`) is now behind that containment on this method's default
  branch. It fires only in the exotic case of a repository that is itself an
  ancestor of home. On the `worktreeRoot` / `external_root` branch the in-repo
  containment does not apply, so there D2 is the active home guard. D2
  remains the primary guard for `files.extract_tar`, which gained no containment. See
  [`DIVERGENCES.md`](../DIVERGENCES.md) → D2.
- A relative `worktreePath` is refused upfront (`"…is a relative path…"`). Send an
  absolute path under the repository.
