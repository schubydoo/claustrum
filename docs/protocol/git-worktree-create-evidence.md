# git.worktree_create: evidence

A cell or a row is one measurement. A cell name such as `A1` or `P-c` names a probe state. A row with no cell name is a measurement that the source text gives in a sentence.
The reference build is `89cb6289` unless a row names another build. The VMs are Linux, macOS and Windows, and a row names its VMs.
The rules that these rows support are on the [rules page](git-worktree-create-steps.md). The contract is on the [contract page](git-worktree-create.md).

Each table has these columns: cell or row, the state, the result on `89cb6289`, the result on claustrum, the VMs, and the runs.
"Equal" means that claustrum answers the same frame and leaves the same disk.
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
| (no cell) | A replacement leaf, ext4 | Both references held the leaf and its parent open. The replacement leaf got a new inode. | claustrum holds both open until it answers, so a replacement cannot reuse the inode of the leaf. | Linux, Windows | 12 of 12 runs on ext4 (Linux) |
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
| D-1, D-5, D-7 | `<git dir>/worktrees` is a symlink to `<F>/WTREG`. D-5 is a relative link. In D-7 `.git` is a symlink too. | Git 2.50 writes the resolved path, `<F>/WTREG/w1`, and the text of test 1 follows. | Not given | macOS | Not given |
| D-1, D-5, D-7 | The same states | Git 2.43 writes the path through the link, `<baseRepo>/.git/worktrees/w1`, and the worktree is created. | Not given | Linux | Not given |
| D-11 | The link of D-1 and a git wrapper that writes the resolved path | The refusal. | Not given | Linux | Not given |
| D-3 | The link target is `<F>/alt/worktrees` | The worktree is created. The `.git` file holds `<F>/alt/worktrees/w1`. | Not given | macOS | Not given |
| D-3 | The same state | The worktree is created. | Not given | Linux | Not given |
| D-4 | The link target is `<F>/alt/WORKTREES` | The create is refused. | Not given | macOS | Not given |
| D-4 | The same state | The worktree is created. | Not given | Linux | Not given |
| D-6 | `<baseRepo>/.git` is a symlink to `<F>/GITDIR`. The `.git` file holds `<F>/GITDIR/worktrees/w1`. | The worktree is created on both VMs. | Not given | Linux, macOS | Not given |
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
| P-d | A wrapper writes `gitdir: /elsewhere/WTREG/w1` after the add | The text of test 1. | Not given | Linux, macOS | Not given |
| P-d | The same state, with `<git dir>/worktrees` as a symlink | The text of test 1. | Not given | Linux | Not given |
| Pd2 | The same wrapper, with `<git dir>/worktrees` as a plain directory | The text of test 1. | Not given | Linux | Not given |
| P-a, P-b | The layout of D-1 with attach mode (P-a) and with a `worktreeRoot` (P-b) | The text of test 1. | Not given | macOS | Not given |
| P-a, P-b | The same states | Both succeed and the `index` is in the link target. | Not given | Linux | Not given |
| P-e | `baseRepo` is a linked worktree of a repository T | The worktree is created. Its registration and its `index` are in T. | Not given | Linux, macOS | Not given |
| S-a | `baseRepo` is a subdirectory of a repository | The worktree is created. | Equal | Linux, macOS | Not given |
| S-b | `baseRepo` is a bare repository | `not_a_repo`. | Equal | Linux, macOS | Not given |
| S-c | A repository with a separate git directory | The worktree is created. | Equal | Linux, macOS | Not given |
| S-d | A daemon `GIT_DIR` alone | The worktree is created. | Equal | Linux, macOS | Not given |
| Se0, S-e | Attach, and an attach that falls back to a new branch, in a plain layout | The worktree is created. | Not given | Linux | Not given |
| B-E2, B-E4 | `GIT_COMMON_DIR` of X in the environment, different commit ids | The add fails. Both sides answer the add-failure frame. | Equal | Not given | Not given |
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
| B-E1 | The same state with no `worktrees` directory in `baseRepo` | The "was not populated" text. | Not given | Not given | Not given |
| B4 | A wrapper removes the record after the read-tree | Success. The entry holds the index. The branch stays. | Equal | Linux, macOS | Not given |
| Z15 | A wrapper removes the record after the read-tree and sets the entry to mode 0500 | `git worktree add failed (checkout): <git text> openat w1/index: permission denied`, then the undo text with `(RemoveAll w1: permission denied)`. The entry and the branch stay. `logs/HEAD` is gone. `HEAD`, `commondir` and `logs` stay. | Equal | Linux, macOS | Not given |
| P-b | The only cell that measures a `worktreeRoot` | See the table of the registration tests. | Same tests in every layout | Linux, macOS | Not given |
| P-a | The cell that measures attach mode | See the table of the registration tests. | Same tests in every layout | Linux, macOS | Not given |
| S-e, Se0 | Attach mode in a plain layout | See the table of the registration tests. | Same tests in every layout | Linux | Not given |

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

The source text gives no claustrum result for these rows, so the table has no such column.

| Cell or row | State | Result on `89cb6289` | VMs | Runs |
|---|---|---|---|---|
| A14, A14f | The registration directory loses its write bit during the checkout | `openat w1/index: permission denied`. The leaf is gone. The registration and the branch stay. The step 2 undo text follows. No git call runs after the checkout. | Linux | Not given |
| A14, A14b | The same | The same result. | macOS | Not given |
| X1 | The temporary index is gone at the exit of git with status 0 | `open <temporary directory>/index: no such file or directory`. The leaf, the registration and the branch go. | Linux, macOS | Not given |
| Y6 | The same, in attach mode | The same frame. | Linux | Not given |
| Y4 | As Y6 (attach mode), and the registration directory has no write bit | The step 2 undo text follows. | Linux | Not given |
| Y3 | A file at the index, and the registration directory has no write bit | `removeat w1/index: permission denied`. The step 2 undo text follows. | Linux | Not given |
| Y7 | A directory that holds one file at the index | `removeat w1/index: directory not empty`. The rollback runs. | Linux | Not given |
| Z10 | The registration is gone at the install | `openat w1/index: no such file or directory`. The whole rollback runs. | macOS | Not given |
| Z11a, Z11b | The registrations directory has mode 0600 after the checkout, with or without a file at the index | `openat w1/index: permission denied`. The step 2 undo text follows. The leaf goes. The registration and the branch stay. | macOS | Not given |
| A14, A14f | A stderr that ends with one newline | One space before the OS error. | Linux | Not given |
| Y2a | A stderr with no final newline | No space. | Linux | Not given |
| Y2b | A stderr with two final newlines | Two spaces. | Linux | Not given |
| X2 | No stderr | The text is the OS error alone. | Linux | Not given |
| Y1b | A stderr of 478 bytes | The whole OS error is kept. | Linux | Not given |
| Y1a | A stderr of 500 bytes | The first 12 bytes of the OS error, ` openat w1/in`, are kept. | Linux | Not given |
| Y1c, X3 | A stderr of 512 bytes, and of 1509 bytes | None of the OS error is kept. | Linux | Not given |

### Evidence: rollback { #ev-rollback }

| Cell or row | State | Result on `89cb6289` | Result on claustrum | VMs | Runs |
|---|---|---|---|---|---|
| B2-11 | Step 1 fails | No branch step runs. The step 1 text follows. | Not given | Not given | Not given |
| C08b | Step 1 fails in attach mode | The text reads `the worktree directory and its registration both remain`. | Not given | Not given | Not given |
| C08 | Attach mode, step 2 | No git call runs. | Not given | Not given | Not given |
| (no cell) | The order of the entries in step 1 | Not sorted. Measured against both references. | Same rule | Linux ext4, macOS APFS | Not given |
| (no cell) | Steps 1 to 3 and their texts | `f6010b97` and `90fca6e6`. | Same wordings | Windows | Not given |
| (no cell) | Step 4 | It follows `89cb6289`. | Same rule | Not given | Not given |
| (no cell) | The measured causes of a failed delete on Windows | An open handle, a process with its working directory in the leaf, and a running executable. | Not given | Windows | Not given |
| (no cell) | More causes of a failed delete on Windows | An ACL that denies the delete and a file name with a trailing dot were causes too. | Not given | Windows | Not given |
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
| D16 | A nested repository, which ends in `/` | `f6010b97` sends the same paths. The text names a Linux VM for D16. | Same rule | Linux (D16) | Not given |
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
- claustrum uses Go's `unicode.IsPrint`, which fits every measured payload. That is an inference, not a proof.
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
- An earlier version of the protocol reference said the opposite and called it a reference limitation reproduced for parity. It was neither.
