# The settings copy of the git methods: measurement record

This page is a record, not reading material. It holds the cells behind the folder `git-shadow` of `5fd08069` and behind the member `settingsControl` of `git.info`. The rules for a reader are in [The settings copy](../PROTOCOL.md#the-settings-copy).

Names on this page:

- `<run>` is the folder of the socket.
- `<S>` is the folder of one daemon, `<run>/git-shadow/<host form>-<pid>-<16 hex>`.
- `<R>` is the folder of one repository, `<S>/<32 hex>`. It holds the copy.
- `<gitdir>` is the git directory of the repository: `<repo>/.git`, or `<repo>` for a bare one.
- "The member" is `"shadowDirOff":true` in the params of a request.
- "The call about the repository" is the `rev-parse --git-dir` call of the section with that name.

The cells ran on three VMs. The Linux VM has git 2.43.0, the macOS VM has git 2.50.1, and the Windows VM has Git for Windows 2.55.0. A cell name is the name of its round, so one name can stand in two tables. The rounds:

| Round | Systems | What ran |
|---|---|---|
| `s3`, `s3b`, `s3p2`, `s3p2-oldgit` | Linux, macOS and Windows VMs. `s3b` and `s3p2-oldgit` on a Linux VM | `89cb6289`, `5fd08069` and the claustrum of that day |
| `s3p2b-edges`, `s3p2b-edges2`, `s3p2b-edges3` | Linux, macOS and Windows VMs. The third round on Linux and Windows VMs | the same three |
| `val-s3p2b1` | Linux, macOS and Windows VMs | `89cb6289`, `5fd08069` and two claustrum builds |
| `val-s3p2b2-1`, `val-s3p2b2-3`, `val-s3p2b2-5` | Linux, macOS and Windows VMs | `5fd08069` and a build of this change. Some cells also ran `89cb6289` |

The three `val-s3p2b2` rounds compared a build of this change with `5fd08069`. The last edits of this change came after round `val-s3p2b2-5`. They have tests and ran on no VM. "claustrum and `5fd08069`" below names what that round left open.

## The member and the states

| Cell | System | Request | `5fd08069` |
|---|---|---|---|
| A1 of round `s3` | Linux VM, macOS VM, Windows VM | `git.info` on a repository, without the member | `{"jsonrpc":"2.0","id":2,"result":{"isRepo":true,"repo":"repo","branch":"main","root":"<repo>","repoSlug":"","defaultBranch":"","settingsControl":"shadow"}}` (the Linux line) |
| A1 of round `s3` | Linux VM, macOS VM, Windows VM | `git.info` on a folder with no repository, as the first request | `{"isRepo":false,"repoSlug":"","defaultBranch":""}`, no member |
| A1 of round `s3`, D1 of round `s3p2` | Linux VM, macOS VM, Windows VM | the same request after a request that ran the start test | `{"isRepo":false,"repoSlug":"","defaultBranch":"","settingsControl":"shadow"}` |
| A1 and B1 of round `s3`, X1 of round `s3p2a-edges` | Linux VM. B1 also on a macOS VM | `git.info` with the member | `"settingsControl":"off-switch"`. On a folder with no repository: `{"result":{"isRepo":false,"repoSlug":"","defaultBranch":"","settingsControl":"off-switch"}}` |
| O1 of round `s3p2-oldgit` | Linux VM, git 2.11.4 and 2.12.5 | `git.info` without the member | `"settingsControl":"fallback-git"` |
| 29 of round `s3p2b-edges` | Linux VM, git 2.43.0 | the first test `worktree add` fails and the second one works | `"settingsControl":"fallback-git"`, and the first `git.info` has 20 calls |
| B3 of round `s3` | Linux VM | the member as `"true"`, `1` or `null` | `"settingsControl":"shadow"`, as with no member |
| S1-seq of round `val-s3p2b2-1` | macOS VM | the member as `"true"`, `1`, `null` or `false` | `"settingsControl":"shadow"` in all four |
| E5 of round `s3p2` | Linux VM | a `HEAD` that is no ref and no object id, as the first request | no member, no `git-shadow` folder, and the git calls carry `GIT_DIR=/dev/null` |
| D1 of round `s3p2`, 39 and 40 of round `s3p2b-edges` | Linux VM, macOS VM, Windows VM | a repository of another owner, without the member | the test runs, and the reply is "no repository" with `"settingsControl":"shadow"` |
| S1 of round `val-s3p2b2-1` | macOS VM, Windows VM | a bare repository as the first request | the test runs. The reply is `{"isRepo":false,"repoSlug":"","defaultBranch":"","settingsControl":"shadow"}` |

- No other method sends the member `settingsControl`. The S1 cells of round `val-s3p2b2-1` show `git.status`, `git.list_branches`, a create and a remove with no such member (macOS and Windows VMs).
- An error reply has no `settingsControl`. The Linux cells are C2 of round `s3p2` and O4 of round `s3p2-oldgit`. Round `s3p2b-edges` has cells 26, 30, 33, 37, 39 and 40.
- A request with the member runs no test and names `<S>` in none of its 8 calls. It leaves an `<S>` that exists (cell S1 of round `s3b`, Linux VM).
- With the member on each request of a daemon, no `git-shadow` folder is made (cell B1 of round `s3`, Linux, macOS and Windows VMs).
- `89cb6289` sends no `settingsControl` in these cells.

The two log lines. `5fd08069` writes one line at the first decision of the state and one more at a change. It writes none for a repeat test with the same result, and none for a failed test. The cells are O5 of round `s3p2-oldgit`, C1a and C2 of round `s3p2`, and cells 28 and 29 of round `s3p2b-edges` (Linux VM). On a Windows VM the line comes at request 7, the first good test (cell 33 of round `s3p2b-edges`).

- `[GitHandler] this host's git works on Claude's own copy of a repository's settings (a shadow directory): a repository's own git config and hooks are not read`
- `[GitHandler] this host's git does not work on Claude's own copy of a repository's settings (fallback-git; older than about 2.13?): a repository's own git config is read, and only the -c overrides limit it`

The state `fallback-git`, on `5fd08069`:

| Cell | System | Fact |
|---|---|---|
| section 4 of round `s3p2-oldgit` | Linux VM | `fallback-git` on git 2.11.4 and 2.12.5. `shadow` on git 2.13.7, 2.17.6 and 2.43.0 |
| section 5 of round `s3p2-oldgit` | Linux VM | Each call on a repository has `GIT_COMMON_DIR=<gitdir>`, no `GIT_DIR`, the heavy list and the six heavy values |
| O4 of round `s3p2-oldgit` | Linux VM | No `config --no-includes --file -` read and no refusal of the config. Format version `2` gets `{"isRepo":false,"repoSlug":"","defaultBranch":"","settingsControl":"fallback-git"}` |
| O5 of round `s3p2-oldgit`, 28 of round `s3p2b-edges` | Linux VM | The test runs again on the first request more than 10 minutes after the last test. 590.0 s and 599.8 s give no test. 605.5 s, 610.0 s and 659.8 s give one |
| O5 of round `s3p2-oldgit` | Linux VM, one run | After the git of the host changed to 2.43.0, the state stayed `fallback-git` until the next test. Then it was `shadow` |
| 28 of round `s3p2b-edges` | Linux VM | In the state `shadow` no second test ran at 590 s, 610 s and 720 s |
| Q-S4c of round `val-s3p2b2-3` | Windows VM | `git.status` names `<S>\.gitdir-<n>`, and the read-tree names `<S>\.index-<n>\index`. The light calls of a create carry the six values. The `worktree add` carries `GIT_COMMON_DIR=<gitdir>` |
| S4-wt1-first-status of round `val-s3p2b2-1` | macOS VM | `.gitdir-<n>` and `.index-<n>` in `<S>`, and six values on the light calls and on the three listings of the branch step |

## The folder of one daemon

| Cell | System | Fact on `5fd08069` |
|---|---|---|
| C6 of round `s3` | Linux VM | The name is `<host form>-<pid>-<16 hex>`. A host name of the form `aa-bb` gives `aa_bb-17044-82053e1e2c228715` |
| 33 of round `s3p2b-edges`, S2 of round `val-s3p2b2-1` | Windows VM | `WIN-<host>` gives `WIN_<host>`. The name has 37 characters there |
| the macOS rounds | macOS VM | A `.` in the host name stays |
| section 5 of round `s3p2-oldgit`, 32 of round `s3p2b-edges` | Linux VM | The daemon makes `<run>/git-shadow` (0700), `<S>` (0700), `<S>/lock` (0600, 0 bytes) and `<S>/.check-<n>`, in this order |
| A3 of round `s3`, 24 of round `s3p2b-edges`, V5 of round `val-s3p2b2-3` | Linux VM, macOS VM. V5 on a Linux VM | In the folder of a repository and in `.probe`, each folder has 0700 and each file 0600. A file that git writes, such as `.index-<n>/index`, has 0644 |
| 44 of round `s3p2b-edges` | Linux VM | `/proc/locks` has `FLOCK  ADVISORY  WRITE <daemon pid> 08:01:<inode of lock> 0 EOF` for the life of the daemon |
| S2 of round `val-s3p2b2-1` | macOS VM | A shared `flock`, an exclusive `flock` and a `lockf` on `<S>/lock` are refused with `Resource temporarily unavailable` |
| Q-S2a and Q-S2b of round `val-s3p2b2-1` | Windows VM | Each open of `<S>\lock` with data access fails with a sharing error, with each share mode. An open for attributes works. A remove of `lock` and of `<S>` fails |
| B6 of round `s3` | Linux VM, macOS VM | After the driver removed `<S>`, the next `git.status` answers normally. It makes `<S>` again with the same pid and another hex part. That folder has `lock` and `<R>`, and no `.probe` |
| B6 of round `s3` | Windows VM | The `lock` file of a live daemon cannot be removed |
| section 5 of round `s3p2-oldgit`, 29, 31 and 33 of round `s3p2b-edges` | Linux VM, macOS VM, Windows VM | In the state `fallback-git` and after a failed test, `<S>` holds `lock` only |
| 44 of round `s3p2b-edges` | Linux VM, Windows VM | Two live daemons with two sockets in one run folder each have their own `<S>`. `-stop` of the second removes its own folder only |
| S7 of round `val-s3p2b2-1`, 41b of round `s3p2b-edges2` | macOS VM | The same with a second run folder whose `git-shadow` is a link to the first one |
| section 1 of round `s3p2`, A3 of round `s3` | Linux VM (93 cells), macOS VM, Windows VM | At `-stop` the daemon removes `<S>`. The empty `<run>/git-shadow` (0700) stays |
| F3 of round `s3p2`, 48 of round `s3p2b-edges` | Linux VM (6 runs), macOS VM and Windows VM (3 runs each form) | At `server.shutdown` or `-stop` during a slow `git.status` the daemon empties `<run>/git-shadow`, also `<S>/.gitdir-<n>`. The slow git child gets no signal, and the slow request gets no reply line. The Linux runs have `server.shutdown` only |
| B4 of round `s3` | Linux VM, macOS VM | A daemon that the driver ended with `SIGKILL` leaves `<S>` |
| G1 of round `val-s3p2b2-7` | Linux VM, 2 of 2 runs for each build and form | At a stop both builds remove the folder of the daemon through a symbolic link at `git-shadow`. The link and the other entries of its target stay |
| R1 of round `val-s3p2b2-7` | Linux VM | With `-socket s.sock` and with `-socket rel/rpc.sock` the folder `git-shadow` is under the working directory of the daemon. Each path of the settings copy has the absolute form: the argv, the working directory and the variables of the test calls, and the variables of the later calls. The five replies and the call lists equal those of the absolute socket path |
| F1 of round `val-s3p2b2-7` | Linux VM | With `<S>` of mode 0500 after the first request, `git.status` answers `{"isRepo":false,"clean":false}` after 4 calls. A create answers `git worktree add failed (checkout): mkdir <S>/.index-<n>: permission denied` with `worktree_add_failed` |

Two daemons with two sockets in one run folder have no result on a macOS VM. The start of the second daemon ends the first one there, on `5fd08069` and on `89cb6289` (cell 41 of round `s3p2b-edges2`).

The entries of `<S>`, on `5fd08069`:

| Entry | When | Cell and system |
|---|---|---|
| `lock` | the life of the daemon | above |
| `.check-<n>/` | during the start test | section "The start test" |
| `.probe/` | from the first call about the repository to the end of the daemon | section "The call about the repository" |
| `<32 hex>/` | one for each repository | section "The folder of one repository" |
| `.gitdir-<n>/` with `index`, `commondir` and `HEAD` | one `git.status`. It is there before the one-time `--attr-source=<empty tree> version` call and goes at the reply | T1 of round `s3b` (Linux VM). 22 of round `s3p2b-edges` (Windows VM). V5 of round `val-s3p2b2-3` (Linux VM, 6 of 6 runs) |
| `.index-<n>/` (0700) with `index` | one create. It goes at the reply, also after a failed read-tree | V5 of round `val-s3p2b2-3` (Linux VM, 15 of 15 creates). 44a and 44c of round `s3p2b-edges2` (Windows VM) |
| `.worktreeinclude-<n>/` (0700) with `patterns` | one create with a `.worktreeinclude` file. It comes after the read-tree. On the Linux VM it goes right after its `ls-files` call starts. On the Windows VM it is there to the reply. `patterns` has 11 bytes, `secret.env\n`, and mode 0644 on Linux and macOS VMs | A3 of round `val-s3p2b1`. V5 of round `val-s3p2b2-3` (Linux VM, 5 of 5 creates. macOS VM, row D1. Windows VM, cell V5i) |
| `.config-<n>/` (0700) with `config.worktree` (0600) | one create in a repository with a `config.worktree` file. It goes before the next git call | section "`config.worktree`" |
| `.s<n>\` | Windows, one `worktree add` of a create | section "The create on Windows" |

For a request on a linked worktree `ws`, `.gitdir-<n>` holds three files. `HEAD` has 19 bytes, `ref: refs/heads/ws\n`. `commondir` has 17 bytes, `C:\o\w\repo\.git\n`. `index` has 137 bytes. The `commondir` file names the git directory of the repository, not `<R>` (cell 22 of round `s3p2b-edges`, Windows VM). `89cb6289` has the same three sizes in its temp folder there.

With the member the short-lived entries of `5fd08069` are in the temp folder: `claude-ssh-gitdir-<n>`, `claude-ssh-index-<n>` and `claude-ssh-worktreeinclude-<n>/patterns` (cell B1 of round `s3`, cell T1 of round `s3b`, cells A1 and A3 of round `val-s3p2b1`). That `patterns` file has mode 0644 in 9 of 9 creates with the member (group P3 of round `val-s3p2b2-6`, Linux VM).

## The start test

The test runs inside the first request that reaches a repository without the member. That request first runs `config --includes --path core.excludesFile`. The test is 10 git calls in `<S>/.check-<n>`. `<n>` is a decimal number that differs for each attempt. The folder is gone at the reply, also after a failed test and after a call that the daemon ended.

The calls of `5fd08069`. The Linux rows are cell B3a of round `s3p2`. Cell S3 of round `val-s3p2b2-1` has the same ten calls on a macOS VM, with exit codes and the order of the variables. Cell S3 of that round has them on a Windows VM, with no order.

- The argv starts with `-c core.hooksPath=/dev/null -c gc.auto=0 -c maintenance.auto=false -c commit.gpgsign=false`. On the Windows VM the first item is `core.hooksPath=NUL`.
- The working directory is `<S>/.check-<n>`.
- The variables, in the order of the Linux and macOS logs: `GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null LC_ALL=C LANGUAGE=C GIT_TERMINAL_PROMPT=0 GIT_AUTHOR_NAME=c GIT_AUTHOR_EMAIL=c@c GIT_COMMITTER_NAME=c GIT_COMMITTER_EMAIL=c@c`. On the Windows VM `GIT_CONFIG_GLOBAL` is `/dev/null` too.
- "+real" adds `GIT_DIR=<S>/.check-<n>/real/.git GIT_WORK_TREE=<S>/.check-<n>/real`. "+shadow" adds `GIT_COMMON_DIR=<S>/.check-<n>/shadow GIT_OBJECT_DIRECTORY=<S>/.check-<n>/real/.git/objects` after those two.

| # | argv after the prefix | More variables |
|---|---|---|
| 1 | `init -q --template= <S>/.check-<n>/real` | none |
| 2 | `config helper.check real` | +real |
| 3 | `mktree` | +real |
| 4 | `commit-tree -m c 4b825dc642cb6eb9a060e54bf8d69288fbee4904` | +real |
| 5 | `update-ref HEAD <the new commit id>` | +real |
| 6 | `config --get helper.check` | +real +shadow |
| 7 | `config --no-includes --file - --list -z`, with no prefix, in the root folder, with `GIT_DIR=/dev/null LC_ALL=C LANGUAGE=C` only | |
| 8 | `config --get helper.check` | +real +shadow |
| 9 | `rev-parse --verify -q HEAD^{commit}` | +real +shadow |
| 10 | `worktree add --no-checkout --detach <S>/.check-<n>/wt <the commit id>` | +real +shadow |

- Call 7 gets the config of `real` on standard input. It has 115 bytes on the Linux VM (cell 1 of round `s3p2b-edges2`). It has 160 bytes on the macOS VM and 153 bytes on the Windows VM (cell S3 of round `val-s3p2b2-1`). On the Windows VM its working directory is that of the daemon, and it has `GIT_DIR=NUL`. From the code: claustrum ends that call at the bound of the nine other calls and waits 1 s for its pipes. A call 7 that fails or that is ended is not measured.
- Pass: both `config --get` calls print `shadow`, call 9 prints the commit id, and call 10 exits 0. The state is `shadow` (section 4 of round `s3p2-oldgit`, Linux VM).
- If call 9 exits 1 with no output, the same call runs again with +real only. An exit 0 of it gives `fallback-git`, and no `worktree add` runs. The test then has 9 calls (section 4 of round `s3p2-oldgit` and cell 28 of round `s3p2b-edges`, Linux VM).
- If call 10 fails, `worktree add --no-checkout --detach <S>/.check-<n>/wt2 <id>` runs with +real only. An exit 0 of it gives `fallback-git`. A second failure fails the request (cells 29 and 26 of round `s3p2b-edges`, Linux VM. Cell 33 of that round and section S4 of round `val-s3p2b2-1`, macOS and Windows VMs).

The folder `shadow` of the test (cell `v8-slow-info` of round `val-s3p2b2-3`, Linux VM, each test call held 0.25 s. Rows D1 and D2 of round `val-s3p2b2-1`, macOS VM. Row D3 of that round, Windows VM):

- After call 5 and before call 6: `shadow/` (0700), `real/.git/worktrees` (0700), the link `shadow/worktrees` to `real/.git/worktrees`, and the folders `shadow/info`, `shadow/objects` and `shadow/refs` (0700). `shadow/config` has 61 bytes, `[core]\n\trepositoryformatversion = 0\n[helper]\n\tcheck = shadow\n`. `shadow/HEAD` has 25 bytes, `ref: refs/heads/.invalid\n`.
- After call 7 and before call 8: `shadow/config` is the copied form of the config of `real`, with `[helper]\n\tcheck = shadow\n` after it. It has 181 bytes on the Linux VM and 237 bytes on the Windows VM. `shadow/HEAD` is `ref: refs/heads/master\n`.
- The second `config` and `HEAD` have a new inode number, in 14 of 14 cells of round `val-s3p2b2-3` (Linux VM). A file `shadow/.tmp-<digits>` (0600) was seen in 7 of 55 tests on the macOS VM.
- On the Windows VM the same texts follow each other, and `real\.git\worktrees` is there from the moment `shadow` is there. No reparse point was seen in `.check-<n>` there.
- The whole `.check-<n>` is gone within 10 ms after call 10 ends (Linux VM).

The failure text. Each cause has one frame: `Claude could not test git on the remote machine (<cause>). Check free disk space and quota there, then try again.`

| Cell | System | Fixture | `<cause>` on `5fd08069` |
|---|---|---|---|
| 26 of round `s3p2b-edges` | Linux VM | both `worktree add` calls fail | `git worktree failed under "<S>": fatal: forced failure by the test wrapper`. The last part is the text of the wrapper |
| 19 of round `s3p2b-edges2` | macOS VM, Windows VM | the same | the same form with the text of the wrapper of that VM |
| 31 of round `s3p2b-edges` | Linux VM | an `init` held 32 s or 40 s | `git init failed under "<S>": signal: killed (git version 2.43.0)` |
| 31 of round `s3p2b-edges` | Linux VM | a `rev-parse --verify` held 32 s or 40 s | `git rev-parse failed under "<S>": signal: killed` |
| W8a of round `s3b` | Linux VM | two `worktree add` calls of 31 s each | `git worktree failed under "<S>": signal: killed`, reply at 62.5 s |
| C2 of round `val-s3p2b2-7` | Linux VM, 2 of 2 runs | an `init` that exits 1 after 3 s | `git init failed under "<S>": fatal: forced failure by the test wrapper (git version 2.43.0)`. The request makes 4 calls: the excludes read, the `init`, `version` in the root folder and `version` in `.check-<n>`. Three requests that wait get that one text with one `init` call |
| S4 of round `val-s3p2b2-1` | macOS VM | `init`, `rev-parse --verify` or `worktree add` held 32 s | the three Linux forms, with `(git version 2.50.1 (Apple Git-155))` after the `init` |
| Q-S4h of round `val-s3p2b2-1` | Windows VM | the same three holds | `exit status 1` in place of `signal: killed`, and `(git version 2.55.0.windows.3)` after the `init` |
| C1 of round `s3p2`, 30 of round `s3p2b-edges` | Linux VM, macOS VM | `<run>` with mode 0500 | `mkdir <run>/git-shadow: permission denied` |
| C1 of round `s3p2` | Windows VM | a file named `git-shadow` | `mkdir C:\o\w\run\git-shadow: The system cannot find the path specified.` |
| S4 of round `val-s3p2b2-1` | macOS VM | a file named `git-shadow` | `mkdir <run>/git-shadow: not a directory` |
| 32 of round `s3p2b-edges` | Linux VM | `git-shadow` with mode 0500 | `mkdir <S>: permission denied` |
| 32 of round `s3p2b-edges` | Linux VM | a tmpfs with 1, 2 or 3 inodes on `git-shadow` | `mkdir <S>: no space left on device`, `open <S>/lock: no space left on device`, `mkdir <S>/.check-<n>: no space left on device` |

- `<S>` is in double quotes in the cause of a failed call, and it has no quotes in the cause of a failed folder step.
- On the Windows VM a path in double quotes has each `\` doubled before the JSON encoding. In the JSON line it reads `\"C:\\\\o\\\\w\\\\run\\\\git-shadow\\\\WIN_<host>-7284-89c47ca2e0ac7e0e\"` (cell 33 of round `s3p2b-edges`).
- After the lock step failed, the request leaves an empty `git-shadow`. After the `.check-<n>` step failed, it leaves `<S>` with `lock` (the tmpfs rows of cell 32).

The shape of the failure for each method. In cell 30 of round `s3p2b-edges` each method is the first request of its own daemon (Linux VM). Section S4 of round `val-s3p2b2-1` has the macOS and Windows VMs.

| Method | `5fd08069` |
|---|---|
| `git.info`, `git.status`, `git.list_branches` | `{"jsonrpc":"2.0","id":N,"error":{"code":-32603,"message":"<text>"}}` |
| `git.worktree_create` | `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"<text>","errorCode":"worktree_add_failed"}}` |
| `git.worktree_remove` | `{"jsonrpc":"2.0","id":1,"result":{"success":false,"error":"failed to remove worktree: could not check whether <worktreePath> is locked: <text>"}}`, with no `errorCode` member |

`89cb6289` answers each of these requests normally. Its remove answers `{"success":true}` and removes the worktree.

The times of the test, on `5fd08069`:

| Cell | System | Fact |
|---|---|---|
| 26 of round `s3p2b-edges` | Linux VM | A failure is kept from the END of the failed test. A request 5.00, 8.00, 9.00, 9.80 or 9.90 s after reply 1 makes 0 calls and gets the kept text. One at 10.00 s or later runs the test again |
| 19 of round `s3p2b-edges2` | macOS VM | 9.904 s makes 0 calls, and 10.007 s runs the test |
| 19 of round `s3p2b-edges2` | Windows VM | 9.910 s makes 0 calls, and 10.006 s runs the test |
| 27 of round `s3p2b-edges` | Linux VM | With the `init` held 4 s, a request 9.33 s after the reply makes 0 calls. One 10.45 s after it runs the test |
| 26 of round `s3p2b-edges`, 19 of round `s3p2b-edges2` | Linux VM, macOS VM, Windows VM | A repeat test that fails starts a new 10 s |
| C1b of round `s3p2`, 33 of round `s3p2b-edges` | Linux VM, macOS VM, Windows VM | The kept failure holds after its cause is gone |
| S2b of round `s3b`, 32 of round `s3p2b-edges` | Linux VM | A request with the member works while a failure is kept. It gets `"settingsControl":"off-switch"` |
| S2a of round `s3b` | Linux VM | A daemon that passed the test before the run folder got mode 0500 answers three more requests normally |
| 31 of round `s3p2b-edges` | Linux VM | A hold of 25 s or 29 s on a test call passes. A hold of 32 s or 40 s gives the error reply at 31.05 to 31.14 s |
| S4 of round `val-s3p2b2-1` | macOS VM | A hold of 5 s passes. A hold of 32 s on `init` gives the reply at 30.07 s. The 29 s side did not run |
| Q-S4h of round `val-s3p2b2-1` | Windows VM | A hold of 29 s on the `worktree add` passes. A hold of 32 s is ended about 30.0 s after its start |
| 31 of round `s3p2b-edges`, S4 of round `val-s3p2b2-1` | Linux VM, macOS VM | After an ended `init`, `git version` runs two times before the reply. After an ended `rev-parse --verify`, the second `rev-parse` starts |
| Q-S4h of round `val-s3p2b2-1` | Windows VM, 4 of 4 runs | After a first `worktree add` that the daemon ended, the `wt2` call runs and exits 0. The reply is the failure text all the same, and the next request gets the kept text |

The held wrapper logs no signal and is gone at the reply. On the Linux VM its `sleep` child lives on.

## The folder of one repository

The name is the first 32 hex digits of the sha256 of the path of `<gitdir>`, with no separator at its end.

| Cell | System | Fact on `5fd08069` |
|---|---|---|
| B1 of round `s3p2` | Linux VM | `e7bb3832a81fc9681c92258a9851069f` for `<repo>/.git`, the resolved path |
| B1 and B2 of round `s3p2` | macOS VM | The resolved path. `sha256("/private/tmp/o/repo/.git")` starts with `237f056b`. Another letter case gives another folder |
| B1, B2 and D6j of round `s3p2` | Windows VM | The path with backslashes: `sha256("C:\o\w\repo\.git")` starts with `0a83e704d30073e7cc965a58a66dc000`. A junction is not resolved. Another letter case adds no folder |
| B3c of round `s3p2`, 24 c of round `s3p2b-edges` | Linux VM, macOS VM, Windows VM | A bare repository uses the path of the repository itself |
| B3b of round `s3p2`, 22 of round `s3p2b-edges` | Linux VM, macOS VM, Windows VM | A request on a linked worktree uses the folder of the main repository |
| 20j and 23a of round `s3p2b-edges` | Linux VM | A `.git` file with a `gitdir:` line uses the git directory that the file names. A submodule uses its folder under `modules` |
| D6j-junction of round `val-s3p2b2-3` | Windows VM | `git.status` through a junction: `rev-parse --absolute-git-dir` prints the real path. The daemon then reads that config and makes a second folder with the name of the real path. The reply is `{"isRepo":true,"clean":false,"changes":["?? u.txt"]}` |

The entries. The modes are those of the Linux and macOS VMs.

| Entry | Mode | Content | Cells and systems |
|---|---|---|---|
| `HEAD` | 0600 | The first 8192 bytes of the `HEAD` of the repository. For a linked worktree, the `HEAD` of the main repository | B3a, B3b and B3e of round `s3p2` (Linux, macOS and Windows VMs). The bound: E5 of that round and cell 17 of round `s3p2b-edges` (Linux VM) |
| `config` | 0600 | The text of the next section | the next section |
| `info/`, `objects/`, `refs/` | 0700 | `objects` and `refs` are empty | A3 of round `s3`, B3a of round `s3p2` (Linux, macOS and Windows VMs) |
| `info/exclude` | 0600 | A copy of the file of the repository | the same cells |
| `info/attributes` | 0600 | A copy, where the repository has the file | B3 of round `s3p2` (Linux VM). 24 j of round `s3p2b-edges` (macOS and Windows VMs) |
| `worktrees` | a symbolic link | It points to `<gitdir>/worktrees`, also where that target does not exist. For a bare repository it points to `<repo>/worktrees` | A3 of round `s3` (Linux VM). B3a of round `s3p2` (macOS VM) |
| `worktrees` | none | No such entry, and no reparse point in `<R>` | 22 of round `s3p2b-edges` (Windows VM, 1159 listings) |
| `shallow` | 0600 | A copy, 41 bytes | B3 of round `s3p2` (Linux VM). 24 d of round `s3p2b-edges` (macOS and Windows VMs) |
| `rebase-merge/` with `head-name` | 0700 and 0600 | A copy | B3 of round `s3p2` and 20k of round `s3p2b-edges` (Linux VM). B3g of round `s3p2` (macOS VM). V2c of round `val-s3p2b2-3` (Windows VM) |
| `rebase-apply/` with `head-name` | 0700 and 0600 | A copy, 16 bytes | 20a of round `s3p2b-edges` (Linux VM) |
| `BISECT_START` | 0600 | A copy | B3 of round `s3p2` (Linux VM). 24 i of round `s3p2b-edges` (macOS and Windows VMs) |
| `BISECT_LOG` | 0600 | 0 bytes, where the file of the repository has 340 or 348 bytes | the same cells |
| `reftable` | a link | It points to `<gitdir>/reftable`. `HEAD` is `ref: refs/heads/.invalid\n`, the text of the repository | 25 of round `s3p2b-edges` (macOS VM, git 2.50.1) |

- The 8192 bytes of `HEAD`: a file of 8191 or 8192 bytes is copied whole. A file of 8193 bytes or of 1 MiB gives a copy of 8192 bytes with no newline at its end. The `git.info` reply of the four size rows has no `branch` member.
- A `HEAD` that is a symbolic link to `refs/heads/main` gives a plain file `HEAD` (0600, 21 bytes, `ref: refs/heads/main\n`). The five methods answer normally (cells N4 of round `val-s3p2b2-3`, Linux and macOS VMs).
- A `HEAD` that is a symbolic link to `refs/remotes/origin/main` gives the copy `ref: refs/remotes/origin/main\n` (0600, 30 bytes). So does a link to `refs/remotes/origin/nope`, a ref that does not exist. `git.info` then has no `branch` member, and a create answers the text of git for its `worktree add` (cell H1 of round `val-s3p2b2-7`, Linux VM). From the code: a link whose target does not start with `refs/` gets the "no repository" answers of the trust check.
- With a `HEAD` file of mode 000 the five methods give their "no repository" answers, and no folder of the repository is made. `git.list_branches` makes 2 calls: the listing and the heavy `rev-parse --git-dir`. A create makes 2 calls: the listing and the light `rev-parse --is-inside-work-tree`, both with six profile values. Each call has `GIT_DIR=/dev/null`, and the second one exits 128 (cell H1 of that round, Linux VM).
- No entry is made for `packed-refs` and `MERGE_HEAD`. The cells are B3 of round `s3p2` (Linux VM) and 24 f and 24 h of round `s3p2b-edges` (macOS and Windows VMs).
- Cells 20b to 20i of round `s3p2b-edges` show eight more names with no entry (Linux VM). Five are `CHERRY_PICK_HEAD`, `REVERT_HEAD`, `sequencer/`, `ORIG_HEAD` and `FETCH_HEAD`. Three are `info/grafts`, `info/sparse-checkout` and `objects/info/alternates`.
- `git.list_branches` answers `{"isRepo":true,"branches":["main","w0"]}` while `<R>/refs` is empty (cell B0 of round `s3`, Linux VM).

When a copy is made, written again and removed, on `5fd08069`:

| Cell | System | Fact |
|---|---|---|
| section 1 of round `s3` (macOS VM), W1 of round `s3b` (Linux VM) | Linux VM, macOS VM | `<R>` is made at the first request that accepts the repository, by `git.info`, `git.status` and `git.worktree_create` |
| section 4 of round `s3p2` | Linux VM | With a refused config no `<R>` is made |
| B4 of round `s3p2`, 18k and 21a of round `s3p2b-edges`, 24 B4 of that round | Linux VM, macOS VM, Windows VM | A second request with nothing changed changes no entry and makes no config read |
| B4 of round `s3p2`, 18k of round `s3p2b-edges`, 15k of round `s3p2b-edges2` | Linux VM, macOS VM, Windows VM | After a change of the config, `config` is a new file. Each other entry stays |
| 21a of round `s3p2b-edges` | Linux VM | A change of an included file alone gives a new `config`, also with the same size. Each such step reads the config and the included file again |
| V2e of round `val-s3p2b2-3` | Windows VM | A change of an included file alone gives a `config` with a new file id |
| 18a of round `s3p2b-edges`, 15 of round `s3p2b-edges2` | Linux VM, macOS VM, Windows VM | After a change of `HEAD`, `<R>/HEAD` is a new file at the first request |
| 19 of round `s3p2b-edges`, 15 of round `s3p2b-edges2` | Linux VM, macOS VM, Windows VM | `shallow`, `info/attributes`, `info/exclude`, `rebase-merge/head-name` and `BISECT_START`: the copy follows at the first `git.info` after the file came, changed or went. The files of the cells are hand-written |
| the same cells | the same systems | After `info/exclude` went, `<R>/info/` is empty. After `rebase-merge/head-name` went, the folder and the file are gone |
| 19a to 19c of round `val-s3p2b2-3`, E15b of that round | Linux VM, Windows VM | A change of `shallow`, `info/attributes` or `info/exclude` gives no new `config` and no config read |
| 25 of round `s3p2b-edges2` | macOS VM, Windows VM | A `touch` of `.git/config` with the same text gives one config read |
| D7 of round `val-s3p2b2-3` | macOS VM | A new copy is written as `.tmp-<digits>` (0600) in the folder of the file. The poll saw that name in `<R>`, in `<R>/info`, in `.check-<n>/shadow` and in `.probe` |

If the folder `<R>` cannot be made, `5fd08069` answers one text in the three shapes of the start test:

| Cell | System | Fixture | Text |
|---|---|---|---|
| N4 of round `val-s3p2b2-3` | Linux VM | `<S>` with mode 0500 | `Claude could not prepare its own files on the remote machine: cannot write its copy of the repository's git config: mkdir <S>/<32 hex>: permission denied` |
| N4 of that round | Linux VM | a plain file named `<32 hex>` in `<S>` | the same text with `mkdir <S>/<32 hex>: not a directory` |
| N4 of that round | Windows VM | the same plain file | the same text with `mkdir C:\\o\\w\\run\\git-shadow\\<name of S>\\0a83e704d30073e7cc965a58a66dc000: The system cannot find the path specified.` |

In those cells request 1 has 2 git calls: the call about the repository and one config read. The same request again gets the same line with 0 git calls. A request on another repository answers normally, and so does a `git.info` with the member. After the driver repaired the fixture, the next `git.info` is normal at once, with 8 calls and no new config read. The planted file is unchanged.

## The copied settings file

The form, on `5fd08069`:

- The file is a list of blocks. Each carried setting is one block of two lines: `[<section>]\n\t<key> = "<value>"\n`, or `[<section> "<subsection>"]\n\t<key> = "<value>"\n`. A header comes before each key, also for a section that repeats (A3 of round `s3`, A1 of round `s3p2`).
- The blocks are in the order of the file of the repository (A1 of round `s3p2`, Linux, macOS and Windows VMs).
- The section name and the key name are in lower case. The subsection keeps its letters (A2 of round `s3p2` on the three systems, cell 10 of round `s3p2b-edges` on Linux and Windows VMs). One measured exception is the key `insteadOf` of a `[url]` block.
- Each carried value is in double quotes, also a number and a boolean.
- A key that the repository sets two times gives two blocks, in file order (A2 of round `s3p2`, the three systems).
- The file ends with `[lfs]\n\trepositoryformatversion = 0\n`. This value has no quotes. The block is there on the three systems and for a bare repository.
- A quote in a value is written `\"`, and a backslash is written `\\`. No other escaped form was seen. `$`, a backtick and `'` are written as they are. The Linux cells are 1, 2 and F1 to F4 of round `s3p2b-edges`. Cell 2 of that round ran on macOS and Windows VMs.
- The text is equal after `git.info` and after `git.status` (A3 and A7 of round `s3p2`, cell 16 of round `s3p2b-edges`).

The text for a fresh repository:

| System | Bytes | Text |
|---|---|---|
| Linux VM | 156 | `[core]\n\trepositoryformatversion = "0"\n[core]\n\tfilemode = "true"\n[core]\n\tbare = "false"\n[core]\n\tlogallrefupdates = "true"\n[lfs]\n\trepositoryformatversion = 0\n` |
| macOS VM | 219 | the same with `[core]\n\tignorecase = "true"\n[core]\n\tprecomposeunicode = "true"\n` before the last block |
| Windows VM | 212 | `filemode = "false"`, and `[core]\n\tsymlinks = "false"\n[core]\n\tignorecase = "true"\n` before the last block |

A bare repository has 184 bytes on the macOS VM and 177 bytes on the Windows VM. It has `bare = "true"` and no `logallrefupdates` block (cell 24 c of round `s3p2b-edges`).

### The carry table

Each row is measured on `5fd08069`. "L", "M" and "W" are the Linux, macOS and Windows VMs. A cell with a letter first is of round `s3p2`. "EL" is round `s3p2b-edges`, and "E2" is round `s3p2b-edges2`.

| Section and key | Result | Cells and systems |
|---|---|---|
| `core.repositoryformatversion` | other form: see below | E8 (L, M, W), EL 15 (L), E2 10 (L) |
| `core.filemode`, `core.bare`, `core.logallrefupdates` | carried | A1, A2, A3 (L, M, W) |
| `core.ignorecase`, `core.symlinks`, `core.autocrlf` (`input`), `core.eol` (`lf`) | carried | A1 (L, M, W), EL 4 and F6 (L) |
| `core.sharedrepository` | other form: see below | H3 (L), F6 (L), group P5 of round `val-s3p2b2-6` (L) |
| `core.precomposeunicode` | carried | A3 of round `s3` (M) |
| `core.quotepath`, `core.safecrlf` (`true`, `warn`), `core.untrackedcache`, `core.splitindex`, `core.longpaths`, `core.protectntfs`, `core.sparsecheckout` | carried, a boolean as `"true"` or `"false"` | EL 4 and F6 (L) |
| `core.worktree`, `core.attributesfile`, `core.excludesfile`, `core.fscache`, `core.abbrev` (`12`), `core.compression` (`3`), `core.commitgraph` | dropped | EL 4 (L) |
| `core.hooksPath`, `core.fsmonitor`, `core.sshCommand`, `core.pager`, `core.editor`, `core.gitProxy` | dropped | G1 (L). G1 for some of them (W) |
| `diff.external`, `gpg.program`, `commit.gpgsign`, `uploadpack.packObjectsHook` | dropped | G1 (L) |
| `index.version` (`4`, `3`) | carried | A1 (L, M, W), EL F6 (L) |
| `extensions.objectformat = sha256`, format version `0` | carried | A3 (L, M, W) |
| `extensions.objectformat = sha1`, `extensions.noop-v1 = true`, format version `0` and `1` | carried | X8 of round `val-s3p2b2-3` (L) |
| `extensions.noop`, `preciousobjects`, `partialclone`, `refstorage = files`, format version `1` | carried, the key in lower case | E9 (L, M, W), EL 13 (L) |
| `extensions.relativeworktrees`, `compatobjectformat = sha256`, `submodulepathconfig`, `refstorage = reftable`, format version `1` | carried | EL 13 (L). `refstorage = reftable` also EL 25 (M) |
| `extensions.worktreeconfig = true` | dropped | A7b and H6, EL 12 and 13 (L). EL 16 (M, W, version `0`) |
| `remote.<name>.url` | other form: the URL rules below | A5, A4, EL 1, 2, 6, 7, F1, F3 (L), E2 4 (L), E2 5 and 7 (W) |
| `remote.origin.promisor` (`true`), `remote.origin.partialclonefilter` (`blob:none`) | carried, each as its own block | A5 (L, M, W) |
| `remote.origin.fetch`, `remote.up.fetch`, `remote.origin.pushurl` | dropped | A5 (L, M, W), EL 6 (L) |
| `remote.lfsdefault`, with no subsection | carried as `[remote]\n\tlfsdefault = "origin"\n` | A6 (L, M), EL 16 (W) |
| `remote.pushdefault`, with no subsection | dropped | EL 9 (L) |
| a key of 300 bytes in `[core]` or `[remote "o"]` | dropped | E2 of round `s3p2` (L, M, W) |
| `branch.<name>.remote` | carried as `[branch "<name>"]\n\tremote = "<value>"\n`, by the name rules below | A5 (L, M, W), B3d (L), EL 9, F2, F4 (L), EL 24 d (M, W) |
| `branch.main.merge`, `branch.main.rebase`, `branch.main.pushremote` | dropped | A5 (L, M, W), EL 9 (L) |
| `url.<base>.insteadOf` | other form: see below | C6 of round `s3`, A7a, EL 8 (L), E2 3 (L) |
| `url.<base>.pushInsteadOf` | dropped, and not applied to a URL | EL 08c (L) |
| `lfs.url`, `lfs.fetchinclude` | carried, each as its own `[lfs]` block before the last block | A6 (L, M), EL 5 (L), EL 16 (W) |
| `lfs.fetchexclude` (`*.tmp`) | carried the same way | E2 6 (L) |
| `feature.manyfiles` (`false`, `yes`) | carried as `"false"` or `"true"` | EL 05b and F6 (L) |
| `user.name`, `alias.x`, `credential.helper`, `http.proxy`, `diff.d.textconv`, `merge.m.driver`, `gc.auto`, `receive.denyCurrentBranch` | dropped | A6 (L, M), EL 16 (W) |
| `submodule.s.url`, `submodule.s.active`, `status.relativePaths`, `diff.renames`, `merge.ff`, `pull.rebase`, `push.default`, `fetch.prune`, `init.defaultBranch` | dropped | EL 05a (L) |
| `advice.detachedHead`, `pack.threads`, `protocol.version`, `safe.directory`, `filter.m.required`, `sparse.expectFilesOutsideOfPatterns`, `maintenance.auto` | dropped | EL 05b (L) |
| `rerere.enabled`, `commit.verbose`, `tag.sort`, `log.date`, `color.ui` | dropped | EL 05c (L) |
| keys of `config.worktree` | other form: section "`config.worktree`" | EL 12 (L), EL 16 (M, W), E2 1 and 9 (L) |
| `x.*`, 200000 keys | dropped | E1, E4, E10 (L). E1b (W) |

For the dropped sections of the EL 05 rows one key each is measured.

`core.repositoryformatversion`: the values `0` and `1` are written, and the value `2` gets no block. `2` then `0` gives one block `"0"` (cell E8, the three systems). `2`, `1`, `0` gives two blocks (EL 15a). `0`, `1`, `1` gives three blocks (EL 15b). The key in an included file is not written (EL 15c to 15e, Linux VM). Five first values get a class 1 refusal: `3`, `-1`, `x`, `00` and an empty value (E2 10a to 10e, Linux VM). The [record of the refusals](git-config-refusals.md) has that text.

Values:

- A boolean is written `"true"` or `"false"`. `yes`, `on`, `1`, `True` and the key with no value give `"true"`. `no` and `0` give `"false"`. Those rows ran on the three systems (cell 3 of round `s3p2b-edges`). On the Linux VM `2` gives `"true"`, and `off`, `FALSE` and an empty value give `"false"`.
- A value that does not fit its key is left out, with no refusal: `core.filemode = maybe`, `core.autocrlf = sometimes`, `index.version = banana`, and `extensions.objectformat = md5` under format version `0` (A3 of round `s3p2`, the three systems). Also `core.eol = xx` and `core.eol = l f` (EL 21a and F6, Linux VM).
- Dropped on the Linux VM, one row each (EL F6): `lfs.url` with a space, `lfs.fetchexclude` with a tab, `remote.origin.partialclonefilter = blob:limit=1 k`, `remote.origin.promisor = tr ue` and `remote.lfsdefault = or g`. Carried there: `lfs.fetchinclude = "a b"`.

The URL of a remote:

- Carried as it is, with a user name and a token: `https://`, `http://`, `ssh://`, `git@host:path` and `git://` (A5 of round `s3p2` on the three systems, EL 7 and 8 on a Linux VM). `gh:o/r.git` is carried where no `insteadOf` rule matches (EL 08b to 08d).
- Dropped on the three systems: `file://<path>` and a plain local path (A5 and B3d of round `s3p2`). Dropped on the Linux VM: `ftp://`, `../x`, an empty value and `ext::` (EL 7, E2 4). Dropped on the Windows VM: `C:\x\r.git`, `C:/x/r.git`, `\\server\share\r.git` and `file:///C:/x/r.git` (E2 7a to 7d).
- A tab drops the value on the three systems (EL 02c, cell 2 of that round on macOS and Windows VMs). A quote, a backslash and `#` do not, on the three systems.
- Dropped on the Linux VM: a space, a newline, CR and the bytes 0x01, 0x08, 0x0c, 0x1f and 0x7f (EL 01a, 01i, 02e, 02f and F1). Carried there in cell F1: byte 0xff, U+00A0, U+0085, U+2028 and 27 more texts.
- Each remote with a carried URL gets its block, in file order. No `origin` is needed (EL 6, Linux VM).

Names:

- The name of a remote, and the value of `branch.<name>.remote`, on a Linux VM (EL F2 and F3). Carried: `origin`, `or-g`, `or_g`, `or.g`, `or/g`, `or+g`, `or@g`, `Or9` and `o`. Names of 64 and of 300 letters are carried too. Dropped: 28 texts. Some of them have a first `-` or `.`, a space, a quote or a backslash. Others have `#`, `:`, `,`, `~`, `%`, `;`, `=` or `ö`. The Windows VM ran 11 of the texts with the same result (E2 5a and 5b).
- A remote with a dropped name gets no block (EL 01j and 01l).
- The name of a branch is carried for 38 of the same 39 texts. `-org` is dropped (EL F4 on a Linux VM. E2 5e on a Windows VM, 10 of 11 texts). A branch that does not exist is carried (EL 9).

Includes and `insteadOf`:

- A setting from a file of a plain `[include]` is carried. Its blocks stand at the place of the include line. The cells are EL 14 (Linux VM), E10 of round `s3p2` and EL 11 and 16 (macOS VM), and EL 16 (Windows VM). `remote.<name>.url` and `branch.<name>.remote` of such a file are carried too (E2 2, Linux VM).
- A relative include path counts from the folder of the file with the include line. `~/` counts from the `HOME` of the daemon. An include inside an included file is followed. An include of an absent file is no error (E2 8, Linux VM).
- A setting from a file of an `includeIf` section is not carried. The forms are `gitdir:`, `gitdir/i:`, `onbranch:` and `hasconfig:` (EL 11 on Linux and macOS VMs).
- An `insteadOf` rule of the config itself is applied to each remote URL that it matches, and the longest match wins. The rule itself is not carried. For each remote that the rule changed there is one block `[url "<resolved URL>"]\n\tinsteadOf = "<resolved URL>"\n`. In the measured rows those blocks stand after the last remote block, in the file order of their remotes. The cells are C6 of round `s3`, EL 8 and E2 3 (Linux VM).
- Two remotes with one resolved URL give the same `[url]` block two times (E2 3e). A rule of the home `.gitconfig` is not applied (EL 08d). A `[url]` rule from an included file with no remote is dropped. The cells are A7a of round `s3p2` (Linux VM) and EL 16 (macOS and Windows VMs).

Replies that follow from the copy, on `5fd08069`:

| Cell | System | State | Reply |
|---|---|---|---|
| A3 of round `s3p2`, cell 3 of round `s3p2b-edges` | Linux VM, macOS VM, Windows VM | `core.filemode = maybe`, `core.autocrlf = sometimes`, `index.version = banana`, or `extensions.objectformat = md5` under version `0` | normal. `89cb6289` answers "no repository" or the hooks refusal |
| A3 and A3e of round `s3p2` | Linux VM, macOS VM, Windows VM | `extensions.objectformat = sha256` in a sha1 repository | `{"isRepo":false,"repoSlug":"","defaultBranch":"","settingsControl":"shadow"}`, and `<R>` holds the config |
| X8 of round `val-s3p2b2-3` | Linux VM | `extensions.objectformat = sha1` or `noop-v1 = true` under version `0` | the same "no repository" line, and `{"result":{"isRepo":false,"clean":false}}` |
| 13 of round `s3p2b-edges` | Linux VM, git 2.43.0 | `relativeWorktrees`, `compatObjectFormat`, `submodulePathConfig` or `refStorage = reftable` under version `1` | the same "no repository" line |
| V6-13d of round `val-s3p2b2-3` | Windows VM | `refStorage = reftable` under version `1` in a files repository | `git.info` has no `branch` member. Its last call, `rev-parse --short HEAD`, exits 128 |
| A3f of round `s3p2` | Linux VM, macOS VM, Windows VM | `core.bare = true` with a work tree | normal, also on `89cb6289` |

### What no cell covers

No cell has these values. claustrum drops each of them, and a test pins that choice.

- `core.eol` other than `lf`, `index.version = 2` and `core.logallrefupdates = always`.
- `core.sharedrepository` with no value, in another letter case, as `yes`, `1` or `2`, or as an octal number that git refuses.
- A boolean with a sign, with a unit letter or with more than 9 digits. `INPUT` and `WARN` in capitals.
- `partialclonefilter` other than `blob:none`.
- A URL with another scheme, also `HTTPS://` in capitals. A host of one letter before the colon on Linux and macOS. A first `-` after the scheme. The control bytes 0x02 to 0x07, 0x0b and 0x0e to 0x1e.
- A remote name that starts with a digit, `_`, `+`, `@` or `/`. A remote name that has `..`, `/.`, `./` or `//`, or that ends with `.` or `/`.
- A branch name with a control character, `)`, `]`, `}` or `>`.
- `lfs.url` with a scheme other than `https://`. An lfs pattern with a character beyond letters, digits, `*`, `.` and a space. So `*.bin,*.zip` is dropped.
- An extension in an included file.
- An `insteadOf` rule of an included file with a remote that it matches. The URL is carried as written.
- A key with no row in the carry table.

Three more points have no cell. The first is the place of a `[url]` block among blocks of another section after the remotes. claustrum puts the `[url]` blocks right after the last remote block. The second is two rules of one length. claustrum takes the first of them. The third is a `touch` of the config with the same text on a Linux VM. claustrum writes a new `config` file after it.

## The config reads

`5fd08069` reads each config file with one call, `config --no-includes --file - --list -z`, with the file text on standard input. The variables are `GIT_DIR=/dev/null LC_ALL=C LANGUAGE=C`, with `GIT_DIR=NUL` on the Windows VM. The working directory is the root folder on Linux and macOS VMs. On the Windows VM it is the folder of the daemon (B3a of round `s3p2`).

The reads of a first `git.info`, after the call about the repository (cell 1 of round `s3p2b-edges2`, Linux VM. The wrapper logged the bytes of each read):

| Cell | Fixture | Reads | Calls of the request |
|---|---|---|---|
| 1k | a plain repository | the config | 21 |
| 1m | one `[include]` | the config, the included file | 22 |
| 1a, 1b, 9a, 9k | `extensions.worktreeConfig = true` and a `.git/config.worktree` file | the config, then `.git/config.worktree` two times | 23 |
| `p2-K-none-empty-file` of round `val-s3p2b2-6` | the extension and a `.git/config.worktree` file of 0 bytes | the config | 21 |
| 1d | the extension is set, with no such file | the config | 21 |
| 1e | the file is there, and the extension is not set | the config | 21 |

- One `includeIf` section, with or without a match, gives 2 reads (round `s3p2b-edges`, Linux and macOS VMs).
- A second `git.info` makes no read (9 calls). `git.list_branches` makes none (cell 1c).
- A create with the extension and the file makes one read of `.git/config.worktree`, right after its `worktree add` (cells 1a, 1b, 9a and 9k).
- `git.status` on a linked worktree with a `config.worktree` of its own makes one read of that file. The call has the root folder as its working directory and `GIT_DIR=/dev/null LANGUAGE=C LC_ALL=C` (cell 1c of round `val-s3p2b2-5`, Linux VM).
- The file of each read is not measured on macOS and Windows VMs.

A refused config, on `5fd08069` (cells X9 of round `val-s3p2b2-5`, Linux and macOS VMs, 8 or 9 requests in one daemon):

| Cell | Reply | Calls for each request |
|---|---|---|
| format version `3`, a repository of the user of the daemon | the class 1 text of the format version | 13, then 1 each: the config read. The remove has 2 reads |
| a syntax error, the same owner | the class 1 text of the syntax error | 14, then 2 each: the read (exit 128) and `git version`. The remove has 4 |
| format version `3` or a syntax error, another owner | the five "no repository" lines | 12, then 1 each: the call about the repository. The remove has 2. No config read |

The `git version` call after a syntax error runs in the root folder. It has no option list and no variable (cell A5 of round `val-s3p2b1`). On the Windows VM its working directory is that of the daemon (cell PA5 of round `val-s3p2b2-3`).

## The call about the repository

In the state `shadow` `5fd08069` asks git about the repository before the calls of the method. The call comes after the start test and before the config read. In a first request it is call 12, for each method (cells X2 of round `val-s3p2b2-5`, Linux, macOS and Windows VMs).

- The argv is `-c core.hooksPath=/dev/null -c core.fsmonitor=false rev-parse --git-dir`. On the Windows VM the first item is `core.hooksPath=NUL`.
- The variables, in the order of the Linux and macOS logs: `GIT_COMMON_DIR=<S>/.probe GIT_OBJECT_DIRECTORY=<S>/.probe/objects GIT_CEILING_DIRECTORIES=<x> GIT_ALLOW_PROTOCOL=denied_by_claude_ssh GIT_NO_LAZY_FETCH=1 GIT_TERMINAL_PROMPT=0 LC_ALL=C LANGUAGE=C`. It has no `GIT_DIR` and no `GIT_WORK_TREE`.
- Its `GIT_CEILING_DIRECTORIES` takes the place of the value of the daemon for this call (cell D5 of round `s3p2`, Linux VM).

| Kind of path | Working directory | `GIT_CEILING_DIRECTORIES` | Exit code and output |
|---|---|---|---|
| the root of a repository | `<repo>` | the parent folder of `<repo>` | 0, `.git\n` |
| a folder inside | `<repo>` | the same | 0, `.git\n` |
| a linked worktree as `path` of `git.info` | the worktree | the parent folder of the worktree | 0, the path of the registration |
| `git.status` with a linked worktree as `path` | `<repo>` | the parent folder of `<repo>` | 0, `.git\n` |
| a bare repository | the repository | its parent folder | 0, `.\n` |

The table is cell X2 of round `val-s3p2b2-5` (Linux VM, 20 cells). The macOS and Windows VMs of that round show the same working directories and values.

The folder `<S>/.probe` holds two files with mode 0600. `HEAD` has 25 bytes, `ref: refs/heads/.invalid\n`. `config` has 36 bytes, `[core]\n\trepositoryformatversion = 0\n`. It also holds the empty folders `info`, `objects` and `refs` (0700). The texts are equal on the three systems (A3 of round `s3`, B3 of round `s3p2`, X1 of round `val-s3p2b2-5`). It is made in the poll step in which `.check-<n>` goes, 9 to 91 ms before the call starts (Linux VM, 30 first requests). It goes at `-stop`. The birth times on the macOS VM give this order: `refs`, `objects`, `info`, `config`, `HEAD`. That is row E2 of round `val-s3p2b2-5`, in 14 of 14 folders.

The answers (cell 39 of round `s3p2b-edges`, Linux VM, where the wrapper answers the call itself. Requests: `git.info`, `git.status`, `git.list_branches`, a create, a remove):

| Answer of the wrapper | `5fd08069` | Calls about the repository for each request | All calls for each request |
|---|---|---|---|
| exit 0, prints `.git` or the full path of `<gitdir>` | normal replies | 1, 0, 0, 0, 0 | 21, 21, 4, 13, 2 |
| exit 128 with `fatal: not a git repository (or any of the parent directories): .git` | the five "no repository" lines below | 1, 1, 1, 1, 2 | 12, 1, 1, 1, 2 |
| exit 1 with no text | `git could not check this repository on the remote machine: at "<repo>": exit status 1` | 1, 1, 1, 1, 2 | 13, 2, 2, 2, 4 |
| exit 128 with no text | the same with `exit status 128` | 1, 1, 1, 1, 2 | 13, 2, 2, 2, 4 |
| exit 1 with the text `fatal: text of the wrapper for p3` | the same with that text in the place of `exit status 1` (cells P3 of round `val-s3p2b2-7`, Linux VM, `git.info` and `git.status`) | 1 and 1 | 13 and 2 |
| exit 0, prints `/elsewhere/.git` | `git and Claude disagree about which repository holds this folder: from "<repo>" git finds "/elsewhere/.git", Claude finds "<repo>/.git"; try again` | 2, 2, 2, 2, 4 | 13, 2, 2, 2, 4 |
| exit 0, prints nothing | the same text with `git finds "<repo>"` | 2, 2, 2, 2, 4 | 13, 2, 2, 2, 4 |

The call counts of the last column are from cell 39 of round `val-s3p2b2-5` (Linux VM). After an exit 1 or an exit 128 with no text, one `git version` call follows, in the root folder with no variable.

The five "no repository" lines. `5fd08069` sends them for git's own "dubious ownership" answer too. The cells are D1 and D3 of round `s3p2` and X3 of round `val-s3p2b2-5` (Linux, macOS and Windows VMs).

- `{"isRepo":false,"repoSlug":"","defaultBranch":"","settingsControl":"shadow"}`
- `{"isRepo":false,"clean":false}`
- `{"isRepo":false,"branches":[]}`
- `{"success":false,"error":"not a git repository","errorCode":"not_a_repo"}`
- `{"success":false,"error":"failed to remove worktree: could not check whether <worktreePath> is locked (its registrations could not be examined); retry"}`

With `safe.directory = *` in the home config the answer is good. `safe.directory` in the config of the repository does not help (cells D2 and D3 of round `s3p2`, the three systems). After a "no repository" answer `<S>` holds `.probe` and `lock`, and no `<R>`.

The error texts have the three shapes of the start test. The remove text is `failed to remove worktree: could not check whether <worktreePath> is locked: <text>`.

The times, on `5fd08069`:

| Cell | System | Fact |
|---|---|---|
| 34 of round `s3p2b-edges` | Linux VM | A good answer is kept 5.0 s from the END of the call. A request 4.00, 4.60 or 4.90 s after it makes no call. One at 5.05 s or later makes one |
| X4 of round `val-s3p2b2-5` | Linux VM, macOS VM | A request that still runs 5.0 s after the end makes the call in its middle. The earliest such call came 5.015 s after the end |
| X4 step cells of round `val-s3p2b2-5` | macOS VM, 32 of 32 rows | The time is tested before each pair of a listing and its command. It is not tested before the second call of a pair |
| D4 of round `s3p2`, 25 of round `s3p2b-edges2` | Windows VM | No call inside 5 s, and a call 5.4 to 5.6 s after the one before |
| D4 of round `s3p2` | macOS VM | No call 2.84 s after the end, and a call at 8.9 s |
| 35 of round `s3p2b-edges`, 25 of round `s3p2b-edges2` | Linux VM, macOS VM, Windows VM | Inside the 5 s a new call follows `touch <repo>`, `mkdir <repo>/x`, `touch <repo>/.git` and a new file in `.git` |
| 35 of round `s3p2b-edges` | Linux VM | A new call follows a `.git` that a copy replaced. No new call follows `chmod 700 .git`, `chown nobody:nogroup .git`, a `touch` of `.git/HEAD` or of `.git/objects`, or a plain `git worktree add` |
| 35 of round `s3p2b-edges`, 25 of round `s3p2b-edges2` | Linux VM, macOS VM, Windows VM | No new call follows a new config text, a `touch` of the config, or a `HEAD` rewritten in place |
| B3b and D7b of round `s3p2` | Linux VM, macOS VM | The kept answer is for one work tree root. A second folder inside the same work tree makes no new call |
| 31 of round `s3p2b-edges2` | Linux VM | After `chown nobody:nogroup .git` inside the 5 s, requests 2 to 5 answer normally with no call. Request 6, 7 s later, answers "no repository" |
| 37 and 39 of round `s3p2b-edges` | Linux VM | A bad answer is not kept. Each request makes the call again, and a remove makes it two times |
| 37 of round `s3p2b-edges` | Linux VM | A call held 25 s or 29 s passes. A call held 31 s or 40 s gives the error at 31.15 s |
| 40 of round `s3p2b-edges` | macOS VM, Windows VM | A call held 40 s gives the error at 30.21 s and at 30.76 s |
| 38 of round `s3p2b-edges` | Linux VM | An ended call on `git.status`, `git.list_branches` and a create answers at 31.0 s with 1 call. A remove answers at 62.0 s with 2 calls. No worktree is made or removed |

The text of an ended call: `git could not check this repository on the remote machine: it did not finish within 30 seconds in "<repo>"; try again`. On the Windows VM the path in the JSON line is `\"C:\\\\o\\\\w\\\\repo\"`. The answer is not kept, and `server.ping` answers after it. `89cb6289` does not end its own `rev-parse --git-dir` in those cells.

In the state `fallback-git` and with the member, `5fd08069` does not make this call. The cells are in round `s3p2-oldgit`, cell B1 of round `s3` and cell 29 of round `s3p2b-edges`.

Two states get one more call on `git.info` of `5fd08069`: a folder with no repository, and a `HEAD` that is no ref. After its listing a heavy `rev-parse --git-dir` runs with `GIT_DIR=<null device>`, and it exits 128. Round `val-s3p2b2-3` has the rows: D4 (macOS VM), the Q-S1 cells (Windows VM) and E5-HEAD-garbage (Linux VM).

## The git calls in the state `shadow`

`{D}` is `GIT_COMMON_DIR=<R> GIT_OBJECT_DIRECTORY=<gitdir>/objects`. `{W}` is `GIT_DIR=<gitdir> GIT_WORK_TREE=<work tree root>`. [Hardened git calls](../PROTOCOL.md#hardened-git-calls) has the option lists and the profile values.

| Cell | System | Fact on `5fd08069`, without the member |
|---|---|---|
| B0 of round `s3`, B3a of round `s3p2`, 50 of round `s3p2b-edges` | Linux VM, macOS VM, Windows VM | A call with no `--git-dir=` in its argv carries `{D} {W}`. A call with `--git-dir=` carries `{D}` only |
| A1, A3 and A4 of round `val-s3p2b1` | Linux VM, macOS VM | The order is `GIT_COMMON_DIR`, `GIT_OBJECT_DIRECTORY`, `GIT_DIR`, `GIT_WORK_TREE`, right after the profile values. The Windows log has no order |
| B3b of round `s3p2` | Linux VM, macOS VM | A linked worktree as `path`: `GIT_DIR=<gitdir>/worktrees/<name>`, `GIT_WORK_TREE=<the worktree>`, and `{D}` of the main repository |
| B3c of round `s3p2` | Linux VM | A bare repository: `GIT_DIR=<repo>`, `GIT_OBJECT_DIRECTORY=<repo>/objects`, and no `GIT_WORK_TREE` |
| D7b of round `s3p2` | Linux VM | A `path` inside the work tree: the working directory is the `path`, and `{W}` is that of the repository |
| D5 and D7b of round `s3p2`, X2 of that round | Linux VM, macOS VM | A variable of the daemon's own environment goes to each call unchanged. Measured: `GIT_CEILING_DIRECTORIES`, `GIT_DISCOVERY_ACROSS_FILESYSTEM`, `DYLD_LIBRARY_PATH` |
| 50 of round `s3p2b-edges`, the call list of git 2.13.7 in round `s3p2-oldgit` | Linux VM, macOS VM, Windows VM | The listing `config -z --list` before a call prints the carried blocks and `lfs.repositoryformatversion\n0` |
| A3 and A4 of round `val-s3p2b1` | Linux VM, macOS VM, Windows VM | The light calls of a create and their listings carry six values: `GIT_ALLOW_PROTOCOL=denied_by_claude_ssh GIT_TERMINAL_PROMPT=0 GIT_NO_REPLACE_OBJECTS=1 GIT_GRAFT_FILE=<null device> GIT_NO_LAZY_FETCH=1 GIT_ASKPASS=`. So do the three listings of the branch step of a remove. The order is from the Linux and macOS VMs |
| A3 of round `val-s3p2b1` | Linux VM, macOS VM, Windows VM | Two listings run before the read-tree. The second carries the hook pins, `GIT_INDEX_FILE=<S>/.index-<n>/index` and `LANGUAGE=C`, with no `LC_ALL`. The read-tree has `lfs.storage=<gitdir>/lfs` as its first item and the variables of that listing |
| A3 of round `val-s3p2b1`, the include cell | Linux VM, macOS VM, Windows VM | The include step names `--exclude-from=<S>/.worktreeinclude-<n>/patterns` |
| N3 of round `val-s3p2b2-3` | Linux VM | A remove with `worktreeRoot` has 8 calls, and 14 with `branchName`. Each carries six values, `{D}` and `{W}` |
| `v5-create` of round `val-s3p2b2-5` | Linux VM, 4 of 4 runs | Of two creates with a `.worktreeinclude` file in one daemon, the first one runs the `version` call of the include step |
| `p3m-member` and `p3-include` of round `val-s3p2b2-6` | Linux VM, 2 of 2 and 3 of 3 runs | That call runs in the first create of the daemon only, with or without the member. With the member on creates 1, 2 and 5 of five the counts are 1, 0, 0, 0 and 0. With the member on create 3 of four they are 1, 0, 0 and 0 |

The next table is a plain create of `5fd08069` on a Linux VM. The repository has a linked worktree, and a first request came before. The cells are 50a of round `s3p2b-edges` and A3 of round `val-s3p2b1`. "light" is the light list, and "heavy" the heavy list.

| # | List | argv | Working directory | Values |
|---|---|---|---|---|
| 1, 3, 9 | none | `config -z --list` | `<repo>` | the six light values, `{D} {W}` |
| 2 | light | `rev-parse --is-inside-work-tree` | `<repo>` | the same, with the hook pins |
| 4 | light | `rev-parse --abbrev-ref HEAD` | `<repo>` | the same |
| 5, 6 | none, heavy | `config -z --list`, then `rev-parse --absolute-git-dir` | `<repo>` | the six heavy values, `{D} {W}` |
| 7, 8 | none, heavy | `--git-dir=<gitdir> config -z --list`, then `--git-dir=<gitdir> --work-tree=<repo> rev-parse --show-toplevel` | `<gitdir>` | the six heavy values, `{D}` |
| 10 | light | `worktree add --no-track --no-checkout -b w1 <repo>/.claude/worktrees/w1` | `<repo>` | the six light values, `{D} {W}` |
| 11 | none | `--git-dir=<gitdir> config -z --list` | the new worktree | the four light values, `{D}` |
| 12 | none | `--git-dir=<gitdir> config -z --list` | the new worktree | the four light values, `{D}`, the hook pins, `GIT_INDEX_FILE`, `LANGUAGE=C` |
| 13 | `lfs.storage=<gitdir>/lfs`, then the read-tree list | `--git-dir=<gitdir> --work-tree=<the new worktree> read-tree -u --reset --no-recurse-submodules refs/heads/w1` | the new worktree | as call 12 |

A bare `baseRepo`, on `5fd08069` (cell `x2-bare-create` of round `val-s3p2b2-5`, Linux VM, 3 of 3 runs): the create answers `{"result":{"success":false,"error":"not a git repository","errorCode":"not_a_repo"}}` after 15 calls. The last call is `rev-parse --is-inside-work-tree`, with exit 0 and the output `true`. Which test of `5fd08069` refuses is not measured.

A daemon with `GIT_DIR` of another repository in its environment (cells O1 of round `val-s3p2b2-7`, Linux VM, 20 requests with equal replies on both builds):

| Cell | Fact on `5fd08069` |
|---|---|
| `GIT_DIR` alone | Each of the 27 calls after the test has `GIT_DIR=<other>/.git`, then the profile values, then `GIT_COMMON_DIR=<R>` and `GIT_OBJECT_DIRECTORY=<other>/.git/objects`. No call has `GIT_WORK_TREE`. `<R>` is the copy of the other repository. The five requests make 20, 2, 4, 11 and 2 calls |
| `GIT_DIR` and `GIT_COMMON_DIR` | The same 27 calls with the same variables, and the folder `<R>` is made. The start test calls and the config reads keep the `GIT_COMMON_DIR` of the daemon |
| both cells | A create makes the branch `w1` in the other repository |

From the code: the pair of calls of a remove that finds the registration keeps the `GIT_COMMON_DIR` of the daemon. No cell has that pair with both variables.

## The create on Linux and macOS

| Cell | System | Fact on `5fd08069` |
|---|---|---|
| N2 of round `val-s3p2b2-3` | Linux VM (9 of 9 runs), macOS VM | `<gitdir>/worktrees` is there before the `worktree add` call starts, empty and with mode 0755. At that moment no git process runs. On `89cb6289` git makes it |
| N2 of that round | Linux VM (6 of 6 runs), macOS VM | After a `worktree add` that the wrapper fails, the empty folder stays. `89cb6289` leaves no such folder |
| H3 of round `val-s3p2b2-5` | Linux VM, 2 of 2 runs, umask 022 | With `core.sharedRepository = group` the folder is `d 2775`, and `worktrees/w1` is `d 2755`. `89cb6289` has the same modes |
| group P5 of round `val-s3p2b2-6` | Linux VM, 18 rows | The folder is made with mode 0777 before the umask, and the daemon then sets its mode by the value of `core.sharedRepository`. The table below has each row. A trace has `mkdirat(15, "worktrees", 0777)` and `fchmod(11, 02770)` for `0660` |
| the three `p2u` cells and the ten rows of group P5 with umask 002, round `val-s3p2b2-6` | Linux VM, 13 of 13 creates | With a daemon umask of 002 the new `.git` file of the worktree is `f 0664` |
| N1 of round `val-s3p2b2-3` | Linux VM, 6 of 6 runs | Git writes `gitdir: <R>/worktrees/w1\n` into the `.git` file of the new worktree (128 bytes). 4 to 25 ms after the call ended, `.git` is a new file of 55 bytes, `gitdir: <gitdir>/worktrees/w1\n`. That is before the next call. The first file then has 0 links |
| N1 of that round | macOS VM | Git writes `gitdir: <gitdir>/worktrees/w1\n` (46 bytes, 0644). The file has one text and one inode from its first sighting to the reply |
| N1 of that round | Linux VM | `worktrees/w1/gitdir` is `<repo>/.claude/worktrees/w1/.git\n` and `commondir` is `../..\n`. Both stay as git wrote them |

The modes of `<gitdir>/worktrees` and of `worktrees/w1` after a create, by the value of `core.sharedRepository` (group P5 of round `val-s3p2b2-6`, Linux VM). `89cb6289` has the same modes in 18 of 18 rows.

| Value | Line of `<R>/config` | umask 022 | umask 002 |
|---|---|---|---|
| `group`, `true` | `sharedrepository = "group"` | 2775 and 2755 | 2775 and 2775 |
| `all`, `world`, `everybody` | `sharedrepository = "everybody"` | 2775 and 2755 | 2775 and 2775 |
| `0660` | `sharedrepository = "0660"` | 2770 and 2755 | 2770 and 2775 |
| `0640` | `sharedrepository = "0640"` | 2750 and 2755 | 2750 and 2775 |
| `umask` | `sharedrepository = "umask"` | 755 and 755 | 775 and 775 |
| no key | no line | 755 and 755 | 775 and 775 |

From the code: `false` gets the line `"umask"`. Another octal number `0xxx` gets its own line and its mode, if it gives the owner the read and the write bit. That is the rule of the git documentation for the key. No cell has those values.
| H1 of round `s3p2` | Linux VM (7 runs), macOS VM | No daemon makes a `.claude-new-worktree*` entry or a `.s<n>` folder |

On `89cb6289` the `.git` file has one text, `gitdir: <gitdir>/worktrees/w1\n`, and stays one file (6 of 6 runs, Linux VM).

## The create on Windows

The folder `<S>\.s<n>`, on `5fd08069`. `<n>` is a decimal number of 9 or 10 digits that is new for each create.

| Cell | Fact |
|---|---|
| 44a and 44f of round `s3p2b-edges2`, 49 of round `s3p2b-edges3` | The folder is made 16 to 28 ms before the `worktree add` call starts. It is gone 3 to 90 ms after the call ended, also after a failed call |
| 49b of round `s3p2b-edges3` | In a repository with no `worktrees` folder: the listing ended at 588.0 ms, `.git\worktrees` was there at 589.8 ms, `.s<n>` at 592.9 ms, and the call started at 612.4 ms |
| 50 of round `s3p2b-edges`, 49b of round `s3p2b-edges3` | The `worktree add` carries `GIT_COMMON_DIR=<S>\.s<n>` and keeps `GIT_DIR`, `GIT_OBJECT_DIRECTORY` and `GIT_WORK_TREE`. The listing before it carries `GIT_COMMON_DIR=<R>`, and so does the read-tree |
| 44a of round `s3p2b-edges2` | Git writes the branch and its log into the real `.git`. It writes the registration of the new worktree into `.s<n>\worktrees\w1` |
| Q-S4c of round `val-s3p2b2-3` | In the state `fallback-git` the `worktree add` carries `GIT_COMMON_DIR=<gitdir>`, and there is no `.s<n>` |

What `.s<n>` holds before git runs, for six states of the repository (cell 49 of round `s3p2b-edges3`, one run for each state). Each entry is a plain file or folder:

| Entry | 49a, one worktree `ws` | 49b, none | 49c, `ws` and `ws2` | 49d, `ws` locked | 49e, locked with a reason | 49f, the folder of `ws` is gone |
|---|---|---|---|---|---|---|
| `HEAD`, 21 bytes | yes | yes | yes | yes | yes | yes |
| `config`, 212 bytes, the text of `<R>\config` | yes | yes | yes | yes | yes | yes |
| `info\exclude`, 240 bytes | yes | yes | yes | yes | yes | yes |
| `objects\` and `refs\`, empty | yes | yes | yes | yes | yes | yes |
| `worktrees\` | yes | no | yes | yes | yes | yes |
| `worktrees\ws\` with `HEAD` (19 bytes), `commondir` (6 bytes) and `gitdir` (38 bytes) | yes | | yes | yes | yes | yes |
| `worktrees\ws\locked` | | | | 0 bytes | 18 bytes | |
| `worktrees\ws2\` with the same three files | | | yes | | | |

No state has `index`, `ORIG_HEAD`, `logs` or `refs` below `.s<n>\worktrees\ws`. The real registration has those four. A file is seen with 0 bytes first, and with its full size 4 to 5 ms later.

The staging folder, on `5fd08069` (cell 47 of round `s3p2b-edges3`, 9 of 9 runs):

- After a good `worktree add` the daemon makes `<gitdir>\.claude-new-worktree-<16 hex>\`. It fills that folder in place with `HEAD` (19 bytes), `commondir` (6), `gitdir` (38) and `refs\`.
- Each file id in the staging folder differs from the id in `.s<n>\worktrees\w1`. So the content is a copy.
- The staging folder and the later `<gitdir>\worktrees\w1` have the same file id, and so have the four entries. So the folder goes by a rename.
- No delete inside `.git` was seen for this step. `.s<n>` goes 2 to 4 ms after `<gitdir>\worktrees\w1` appears.
- The staging folder lives 11.2 to 12.6 ms in three runs and 2.0 to 3.7 ms in six runs.
- While `.s<n>` exists, the `.git` file of the new worktree has 94 bytes. Its text was not read. After the move it has 38 bytes, `gitdir: C:/o/w/repo/.git/worktrees/w1\n` (cell 44a of round `s3p2b-edges2`, cell 49 of round `s3p2b-edges3`).
- `89cb6289` makes no staging entry. `<gitdir>\worktrees\w1` appears while git runs (3 runs).

A create that fails (cells 44b to 44e of round `s3p2b-edges2`). "Fails" means here that the wrapper answers exit 1 in place of git.

| Cell | Reply on `5fd08069` and `89cb6289` | Left |
|---|---|---|
| a failed `worktree add` (44b, 44d) | `{"success":false,"error":"git worktree add failed: wrapper: forced failure","errorCode":"worktree_add_failed"}` | The leaf is gone. No branch and no registration exist. `5fd08069` also removes `.s<n>` |
| a failed read-tree (44c, 44e) | `{"success":false,"error":"git worktree add failed (checkout): wrapper: forced failure","errorCode":"worktree_add_failed"}` | The registration, the leaf, the branch and the index folder are gone |

The index of a create with a `.worktreeinclude` file (cell V5i of round `val-s3p2b2-5`, 4 runs): the file `<S>\.index-<n>\index` (297 bytes) stays from the read-tree to the reply. `<gitdir>\worktrees\w1\index` has 297 bytes at the end.

### The cleanup of old staging entries

A create of `5fd08069` removes old entries of `<gitdir>` whose name starts with `.claude-new-worktree-`. This request did not make them. Cell 48 of round `s3p2b-edges3` has 248 cells with one planted entry each, and the controls M1 and M2 on three builds. Cell 48 of round `val-s3p2b2-5` has 170 cells and the two controls. Both ran on a Windows VM. "D" is a folder with one file, "E" an empty folder, "F" a plain file and "J" a junction.

| Row | Result on `5fd08069` |
|---|---|
| the age of the write time, at the end of the real `worktree add` | 3595.24 s (D), 3596.42 s (E), 3595.37 s (F) and 3595.71 s (J) stay. 3601.28 s to 3602.75 s go, in 8 cells |
| the planted age in minutes, a name with 16 hex | 5, 10, 30, 35, 40, 45, 50, 55, 58, 59, 59.5 and 59.9 stay. 60, 60.5, 90, 100, 110, 119, 120 and 121 go |
| the kind | D goes with its file. E, F and J go |
| a junction | It goes as a junction. Its target folder and the `marker.txt` in it stay, with the same file id, write time and text, in 30 of 30 junction cells. 15 of them have a removed junction |
| the name, at 120 minutes | `.claude-new-worktree-0123456789abcdef`, `.claude-new-worktree-old1`, `.claude-new-worktree-` and `.claude-new-worktree-0123456789ABCDEF` go |
| the name, at each age from 5 to 120 minutes | `.claude-new-worktree` with no dash stays, in the four kinds |
| the name, at 120 minutes | `.claude-new-worktreeX` and `.claude-old-worktree-0123456789abcdef` stay |
| which time counts, at 120 minutes | An old write time of the entry itself: gone (D, E, F, and J with its access time). An old access time alone, an old creation time alone, or old content alone: stays |
| the request | `git.info` (60 cells) and `git.status` (60 cells) leave each entry. The file id is the same after `-stop` in 120 of 120 |
| the moment (control M1) | The `worktree add` ended at 1252 ms. The old entry was last seen at 1257 ms. The staging folder of the create was seen at 1260 ms |
| the create | It succeeds, also where an entry was removed |
| `89cb6289` (controls M1 and M2) | It keeps each planted entry, with the same file id and write time |

On a Linux VM no build removes two planted entries of 120 and 5 minutes (cell H2 of round `s3p2`). The macOS VM did not run that cell.

Round `val-s3p2b2-5` ran a build of this change in the 170 cells and in M1 and M2. It cleared and kept the entries that `5fd08069` cleared and kept. In each of the 42 junction rows the target folder and its marker file were there after the cell, with the marker text unchanged.

## `config.worktree`

The repository has `extensions.worktreeConfig = true` and a file `<gitdir>/config.worktree`. On `5fd08069`:

| Cell | System | Fact |
|---|---|---|
| 12a, 12b, 12d and 12e of round `s3p2b-edges` | Linux VM | `core.bare = false` of that file is one more block of `<R>/config`, after the blocks of the config and before the last block |
| 9a of round `s3p2b-edges2` | Linux VM | `core.bare = true` is carried the same way |
| 12a to 12d and 12f of round `s3p2b-edges`, cell 16 of that round | Linux VM. `filemode` also on macOS and Windows VMs | `core.filemode`, `core.sparsecheckout` and `core.worktree` of that file are not in `<R>/config` |
| 1d and 1e of round `s3p2b-edges2` | Linux VM | With the extension and no file, `<R>/config` has no such block. With the file and no extension, the same holds |
| 1c of round `s3p2b-edges2` | Linux VM | A key of the `config.worktree` of a linked worktree is in no `<R>/config` |
| 12a to 12c, 12f, 1a and 1b of round `val-s3p2b2-3` | Linux VM | `-c core.filemode=false`, and `-c core.sparsecheckout=true` where the file has it, stand before the option list. The heavy list then has 29 or 30 items, and the light list 21 or 22 |
| 1c of round `val-s3p2b2-3` | Linux VM | The calls on a linked worktree with its own file start with `-c core.eol=lf` |
| N16c of round `val-s3p2b2-3` | Windows VM | `core.filemode=true` is the first item, on the calls of `git.info`, on `rev-parse --absolute-git-dir`, on both `rev-parse --show-toplevel` calls and on the four `hash-object` calls |
| A7b of round `val-s3p2b2-3`, N16c of that round | Linux VM, Windows VM | The four `git.status` calls with `--git-dir=<S>/.gitdir-<n>` keep the list of 28 items |
| the six `p2-K` cells of round `val-s3p2b2-6`, and row U1 of that round for the four keys with an item | Linux VM, macOS VM (2 of 2 runs) | `core.autocrlf = input`, `core.ignorecase = true`, `core.symlinks = false` and `core.sparsecheckoutcone = true` each give a `-c` item, as the one key of the file. `core.hookspath = /x` and `user.name = n` give none |
| `p2-K-sparsecheckoutcone-true` of that round, and row U2 | Linux VM, macOS VM (2 of 2 runs) | `git.status` on the new worktree makes 23 calls and lists the `config.worktree` file two times, with 34 bytes on standard input. The second read is call 9, after the first `hash-object` call and before the first `--git-dir=<S>/.gitdir-<n> config -z --list`. The four calls with `--git-dir=<S>/.gitdir-<n>` then start with `-c core.sparsecheckoutcone=true`, before the `--attr-source` option |
| `p2-K-none-empty-file` of that round | Linux VM | With a file of 0 bytes in the new registration, `git.status` lists it one time |
| 12a and 12c of round `val-s3p2b2-3` | Linux VM | On the read-tree the items come after the `lfs.storage` item |
| 12a, 12b, 12d, 12e, H6, 1b, 9a and 9k of round `val-s3p2b2-3` | Linux VM | After the `worktree add`: `config --file <S>/.config-<n>/config.worktree --unset-all core.bare`, in the folder of the daemon, with `GIT_DIR=/dev/null`. In 12d and H6 a second call follows with `core.worktree`. That is 10 calls in 8 cells |
| 12c, 12f and 1a of round `val-s3p2b2-3` | Linux VM | With `filemode = false` alone no such call runs |
| the cells `p2u-12a`, `p2u-12d` and `p2u-12f` of round `val-s3p2b2-6` | Linux VM | With a daemon umask of 002 that file is `f 0664`. A trace has its open with mode 0666 |
| section 0.2 item 4 of round `val-s3p2b2-6` | Linux VM, 20 cells | `<S>/.config-<n>` is `d 0700` and its file `f 0600`. A listing each 2 ms saw the folder for 24 to 43 ms in the 9 cells with an `--unset-all` call. It saw none in the 11 others. A trace of cell 12f has the mkdir and the two removes in one millisecond |
| row U3 of round `val-s3p2b2-6` | macOS VM, 2 of 2 runs | `<S>/.config-<n>` goes after the file of the new registration is written and before `.index-<n>` comes, in 7 of 7 sightings. In cell 12a the registration file is there at 1.186 s, and the folder is gone at 1.197 s |
| section 1.1 of round `val-s3p2b2-5` | Linux VM | After the create, `<gitdir>/worktrees/w1/config.worktree` (0644) holds the text of the file without `core.bare` and `core.worktree`. It has 0 bytes in 12d, 12e, 9a, 9k and H6. It has 25 bytes in 12c, 12f and 1a, and 48 bytes in 12a, 12b and 1b |
| section 2 of round `val-s3p2b2-5` | Linux VM | `git.status` on the new worktree answers `{"isRepo":false,"clean":false}` in 12a, 12b, 12c, 12f, 1a and 1b, with 5 calls. In 12d, 12e and 9k it answers `{"isRepo":true,"clean":true}` |
| 9a of round `s3p2b-edges2` | Linux VM | A create succeeds with `core.bare = true` in that file |

`89cb6289` leaves the whole text there, with `core.bare`, in cells 12a, 12b, 12d and 12e of round `s3p2b-edges` (Linux VM). With `bare = true` in cell 9a of round `s3p2b-edges2` it refuses the create with `not_a_repo`.

The way the file reaches the registration is not measured. No macOS or Windows cell has a create in such a repository.

## claustrum and `5fd08069`

Round `val-s3p2b2-5` ran a build of this change beside `5fd08069`.

| System | Compared | Result |
|---|---|---|
| Linux VM | 383 cells, 758 requests | 13 reply lines differ. 7 are in the git-lfs cells L1 to L5, L9 and L11. 6 are `git.status` on a new worktree in the `config.worktree` cells 12a, 12b, 12c, 12f, 1a and 1b |
| Linux VM | 1408 snapshots of `git-shadow` | Each entry and text is equal. The sha256 of 1796 `config` and `HEAD` copies is equal |
| Linux VM, second VM | 125 cells of the short-lived entries, the start test and the folder failures, and 118 cells of the call about the repository | Outside the `server.shutdown` replies one reply line differs: the bare `baseRepo` of cell `x2-bare-create` |
| macOS VM | 177 cells, 584 reply lines | 3 lines differ, each a `server.shutdown` reply |
| Windows VM | the cells of the round, 1363 wire lines of `5fd08069` | No reply line differs. 459 copied texts in 137 cell runs are equal |
| Windows VM | cell 48, 170 cells and two controls | No row differs |

The last edits of this change follow the rows that the round left open. They ran on no VM. Each has a test:

- The bare `baseRepo` (cell `x2-bare-create`). claustrum refuses a `baseRepo` whose git directory no folder holds as its `.git` entry. That test is claustrum's own.
- The `config.worktree` cells: the `-c` items, the `--unset-all` calls and the file of the new registration. claustrum sends `core.filemode`, `core.sparsecheckout` and `core.eol` as items. No cell has another key, and claustrum sends none. claustrum writes the file of the registration as a new file.
- The `version` call of the include step, one time for each daemon (cell `v5-create`).
- The second config read of a remove with a refused config (cells `x9-v3-own` and E1).
- The `git version` call after a failed call about the repository and after a syntax error (cell 39, cells `x9-syntax-own` and E1b).
- The mode of `<gitdir>/worktrees` with `core.sharedRepository = group` (cell H3).
- The read of the `config.worktree` of a linked worktree in `git.status` (cell 1c).
- The order of the entries of `.probe` (row E2).
- On Windows the index of a create stays in `.index-<n>` to the reply (cell V5i).

Round `val-s3p2b2-6` ran a build with those edits beside `5fd08069` on a Linux VM. No reply line differs in 364 requests of new cells and in 169 requests of a regression pass. Seven points differed in a call, a mode or the life of an entry. The edits for them follow the rows of that round and ran on no VM. Each has a test:

- Four more keys of a `config.worktree` file as `-c` items (the `p2-K` cells).
- The second read of the `config.worktree` file in `git.status` and its items (cell `p2-K-sparsecheckoutcone-true`).
- No read of a `config.worktree` file of 0 bytes in the read of the config (cell `p2-K-none-empty-file`).
- The folder `.config-<n>` goes after the file of the registration is written, before the next git call.
- Mode 0666 before the umask for the `config.worktree` file of the registration and for the new `.git` file.
- Each value of `core.sharedRepository` in the copy and in the mode of `<gitdir>/worktrees` (group P5).
- One `version` call of the include step for each daemon with the member, and mode 0644 for its `patterns` file (group P3).

Measured differences that stay. The reply lines are equal in each of them, except in the git-lfs cells.

| Difference | `5fd08069` | claustrum | Cell and system |
|---|---|---|---|
| a listing with `--show-scope` in a create | none | one `--git-dir=<gitdir> config -z --list --show-scope` call, in the new worktree, with the four light values and `GIT_COMMON_DIR=<gitdir>` | rounds `val-s3p2b2-3` and `val-s3p2b2-5`, Linux, macOS and Windows VMs |
| the pair after `rev-parse --absolute-git-dir` in a create | `--git-dir=<gitdir> config -z --list`, then `--git-dir=<gitdir> --work-tree=<repo> rev-parse --show-toplevel` | no such pair | the same rounds and systems |
| a create with `worktreeRoot` | 31 calls: three rounds of six calls | 18 calls: one round | cell `n3-root` of round `val-s3p2b2-5`, Linux VM. Cell A4 of round `val-s3p2b1`, macOS VM |
| a repeated remove with `worktreeRoot` | 12 calls, and 17 with `branchName`: two `--git-dir=` pairs | 8 and 13 calls | cell `n3-root` of round `val-s3p2b2-5`, Linux VM |
| a create with `existingBranch` | two listings in a row before the attach add, and none before the `-b` add after it failed | one listing in each place | cells A3 and B2 of round `val-s3p2b2-5`, Linux, macOS and Windows VMs |
| a config that changes 50 times each second (cell E4) | 4 config reads for each `git.info`, and 10 for each `git.status` | 1 config read for each | round `val-s3p2b2-5`, Linux VM, one run |
| a config with a key of 1100, 5000 or 70000 bytes, or one key 200000 times (cells E1a, E2a, E2b) | 15 config read calls have a start record and no end record | the same calls end with exit 0 | rounds `val-s3p2b2-3` and `val-s3p2b2-5`, Windows VM |
| the git-lfs cells L1 to L5, L9 and L11 | the text of git-lfs, with a hint about settings in L5 and L9 | `repository_only_filter` in L1, the git-lfs program refusal in L2 to L4 and L11, and no hint in L5 and L9 | round `val-s3p2b2-5`, Linux VM |
| the read-tree in L1 to L4, L7 and L11 | 22 items, and 26 in L1 | 25 items: three `filter.lfs.*` items before the list | round `val-s3p2b2-5`, Linux VM |
| the read-tree in the cells G2-smudge-both and G2-smudge-cat | 23 items | 25 and 26 items: the `filter.m.*` items before the list | round `val-s3p2b2-5`, macOS VM |
| `.git/lfs/logs/<stamp>.log` in L1 to L4 | one file | none | round `val-s3p2b2-5`, Linux VM |

claustrum has no sweep of the folders of other daemons and no limit on the count of repositories. A daemon that ended with `SIGKILL` leaves its `<S>`. `5fd08069` removes it at the first request of the next daemon. The cells are B4 of round `s3` (Linux and macOS VMs) and Q-S2d of round `val-s3p2b2-1` (Windows VM).

Round `val-s3p2b2-7` ran the same build beside `5fd08069` on a second Linux VM, in 74 cell runs. Cells P1, K1 and G1 are equal. Seven points differed. The edits for them follow the rows of that round and ran on no VM. Each has a test:

- The absolute form of the run folder with a relative socket path (cell R1).
- The copy of a `HEAD` link to a ref outside `refs/heads/` (cell H1).
- One more call in `git.list_branches` and in a create where the trust check finds no repository (cell H1, mode 000).
- The answers of `git.status` and of a create with a daemon folder that takes no new entry (cell F1).
- The text of git in the reply after a call about the repository that exits 1 (cells P3).
- The order of `GIT_DIR` and `GIT_COMMON_DIR` with a daemon `GIT_DIR`, and the copy with both variables in the daemon environment (cells O1).
- The git version at the end of the cause of a failed `init` (cell C2).

## Not measured

On `5fd08069`:

- The state `shadow` more than 720 s after the test. The first git version with the state `shadow`, between 2.12.5 and 2.13.7.
- A test between 599.8 s and 605.5 s in the state `fallback-git`.
- The 29 s side of a held test call on a macOS VM. The order of the variables on a Windows VM.
- A run folder that cannot be written on a Windows VM. The deny entry of the cell did not hold.
- Which entries of its own environment the daemon gives to a test call.
- A `touch` of the config with the same text: whether the `config` copy is a new file.
- The files of the config reads, the include path forms, and `remote` and `branch` keys of an included file, on macOS and Windows VMs. `includeIf` on a Windows VM.
- `rebase-apply`, and the entries that get no copy, on macOS and Windows VMs. The `reftable` link on Linux and Windows VMs.
- A real `git rebase --abort` or `git bisect reset`. The cells used hand-written files.
- The light values of the create calls in the state `fallback-git` on a Linux VM.
- A create that the member or the state `fallback-git` runs on Windows with an old staging entry. A create that fails with such an entry.
- An old staging entry with a folder inside it or with more than one file. A write time between 3596.42 s and 3601.28 s. A name in another letter case of the prefix.
- A `worktree add` that git itself fails on Windows. The cells used a wrapper.
- `.s<n>` for a repository with `shallow`, `info\attributes` or a rebase.
- The staging step and the cleanup on a macOS VM, beyond "no such entry is made".
- A `config.worktree` key other than the eleven of the cells: `core.bare`, `core.worktree`, `core.filemode`, `core.sparsecheckout`, `core.eol`, `core.autocrlf`, `core.ignorecase`, `core.symlinks`, `core.sparsecheckoutcone`, `core.hookspath` and `user.name`. Two of the four new keys in one file. A create in such a repository on macOS and Windows VMs.
- The second `config.worktree` read of `git.status` for a file with `core.sparsecheckout` or `index.sparse`, and with the member.
- `core.sharedRepository` as `false` or as another octal number, a `worktrees` folder that is there before the create, and macOS and Windows VMs.
- A bare `baseRepo` with the member.
- Two requests at the same time during a start test.

On claustrum, from the code and its tests:

- The last edits of this change, listed above.
- The Windows steps of the create ran in round `val-s3p2b2-5`. The edits after that round ran on no Windows VM.
- The git calls of a request with the member on a build of this change. Round `val-s3p2b2-1` ran the replies of such requests (cells S1). The two later rounds sent no member.
