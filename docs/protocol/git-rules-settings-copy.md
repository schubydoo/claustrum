# Settings copy of the git methods

In the state `shadow`, when the start test passes, the daemon keeps its own copy of a repository's git settings. The state `fallback-git` and `"shadowDirOff":true` make no copy.

An untagged rule on this page was measured against build `5fd08069` on Linux. A rule that names a system was measured on that system.

## The settings copy

The copy is in a folder `git-shadow` beside the socket. The calls of the five git
methods name it, so git reads it and not the repository config.

### The states and `settingsControl`

`git.info` reports the state in its last member, `settingsControl`. No other method
sends it, and an error reply has none (Linux, macOS and Windows).

| `settingsControl` | When | What it changes |
|---|---|---|
| no member | No start test of the daemon gave a kept state yet | Nothing. A folder with no repository, or a `HEAD` that is no ref, starts no test. A `git.info`, or a create (`4c111e86`, measured on Linux and Windows), on a `.git` file to a missing repository starts it |
| `"shadow"` | A start test passed and was kept | The git calls name the copy. Before them the daemon asks git about the repository |
| `"fallback-git"` | The git of the host does not work on such a copy, for example git 2.11.4 and 2.12.5 | The git calls name the git directory. The daemon reads no repository config, gives no config refusal, makes no copy and does not ask git about it |
| `"off-switch"` | A request with `"shadowDirOff":true` runs (the hold) when the reply is made | A request that starts then names the git directory, with no test, folder or config read |

- After a kept test the "no repository" reply has the member too:
  `{"isRepo":false,"repoSlug":"","defaultBranch":"","settingsControl":"shadow"}`.
  That reply waits for a running test and has its state. After a failed test it has
  no member. A path that does not exist gets the same, but starts no test itself. On
  a folder with no repository the other methods answer at once, except a create on a `.git` file to a missing path (`4c111e86`, measured on Linux and Windows).
- As the first git request of a daemon, a `git.info` on a folder whose `.git` file
  names a missing path runs the test and gives that reply. A missing entry under
  `worktrees` of an existing git directory runs no test, and the reply has no member
  (Linux, macOS and Windows). A missing entry in a missing repository runs the test.
- With no git in the daemon's `PATH`, `git.info` and (`4c111e86`, Linux) `git.list_branches`
  on a repository answer "no repository" with no test. The daemon makes its
  folder, with the lock file only. The member follows the daemon's state (not measured).
- A `git.info` on a regular file gets the gate of the repository of its folder.
  Outside any repository it starts no test. As the first git request of a daemon its
  reply then has no member on Linux and macOS. On Windows it is the hooks refusal,
  with `fork/exec <git.exe>: The directory name is invalid.` On a FIFO or
  `/dev/null` outside any repository the first `git.info` or `git.status` starts no test, and the `git.info` reply has no member, also after
  such a `git.status` (`b3081b51`, measured on Linux). macOS is not measured. For
  `git.status` a regular file there is claustrum's own, not measured.
  On Linux and macOS every file that is not a folder takes the steps of a regular file.
  These parts are claustrum's own, not measured: a FIFO or a device file inside a
  repository on `git.info`, the gate of `git.status` on a file inside a repository,
  and a kind other than a FIFO and `/dev/null`.
- A request with `"shadowDirOff":true` gets `"off-switch"` also as the first request
  of a daemon, in a "no repository" reply and while a failed test is kept.
  [Turning the read off](git-rules-config.md#turning-the-read-off) has the values that count as the
  switch.
- During a hold, each later command that starts its own config listing names the
  git directory (Linux, macOS and Windows). The config check and the call after it
  keep the copy, as does a command that already runs. On Linux, after the hold a
  later command runs the test and takes the copy. A create in a hold makes no copy.
- During a hold, `git.status`, `git.list_branches` and `git.worktree_remove` get no
  config refusal. On a repository of format version 3 they answer
  `{"isRepo":false,"clean":false}`, `{"isRepo":false,"branches":[]}` and the
  lock-check text that ends `(its registrations could not be examined); retry`, and
  the remove deletes nothing. On a config that only the daemon refuses, the remove
  deletes. macOS and Windows are not measured for these replies and removes.
- A held request keeps no hold for later requests. A `worktree add` that started in
  the hold is not stopped at 2 minutes during the hold. Nor is it after the holder answers, and a held call that starts
  after that answer has the 2 minute limit (claustrum's own, not measured).
- In the state `fallback-git` the test runs again on the first request more than 10
  minutes after the last test. In the state `shadow` no second test ran up to 720 s.
- The daemon logs one `[GitHandler]` line per test that gives a state, until a
  state is kept (`4c111e86`, measured on Linux). After that it logs one per change
  (`5fd08069`, measured). A failed test logs none (`5fd08069`, measured). On macOS and Windows, and for a kept
  `fallback-git` due for its retest, it is claustrum's own.

### The start test

The first request without the member that reaches a repository runs the test. The
test makes a small repository and a copy of it in a folder `.check-<n>` of the
daemon, and runs git calls there. One of them is the read of the test repository's
config. It runs in the folder of [calls outside a repository](git-rules-calls.md#calls-outside-a-repository) (claustrum's own, not measured).
The test folder is gone at the reply. If the test fails, the request gets this
text:

`Claude could not test git on the remote machine (<cause>). Check free disk space and quota there, then try again.`

`git.info`, `git.status` and `git.list_branches` answer it as a `-32603` error.
`git.worktree_create` answers it in `error`, with the `errorCode`
`worktree_add_failed`. `git.worktree_remove` answers it after `failed to remove
worktree: could not check whether <worktreePath> is locked: `, with no `errorCode`.

One `git version` call follows a failed read of the test repository's config. If it fails
too, the text is its `git cannot run on this host; git not run: <exec error>: <git text>`
alone, in the same frames. A remove deletes nothing there (Linux, macOS and Windows).
The same holds for a failed first `git version` call after a failed call that makes
the test repository, and the second does not run (`b3081b51`, measured on Linux,
claustrum's own on macOS and Windows).

The test's `commit-tree` call names the tree that `mktree` printed. A `mktree` that
prints `garbage` and exits 0 fails the test at `commit-tree`, and a remove deletes
nothing (`b3081b51`, measured on Linux). Other output goes to `commit-tree` with its line ending cut (claustrum's own, not measured).

| `<cause>` | When |
|---|---|
| `git <verb> failed under "<folder of the daemon>": <error lines of git, space-joined>` | A test call other than the read of the test repository's config failed. Go's `%q` quotes the folder. Each line end is one space. An empty line and the blanks at line ends stay, each blank as one space, except at both ends of the text (`b3081b51`, measured on Linux) |
| `git <verb> failed under "<folder>": signal: killed` | The daemon ended such a test call after 30 seconds (Linux and macOS), not the first `config --get`. The reply comes near 31 s (Linux). On Windows the end is `exit status 1` |
| the same with ` (<version stdout>)` at its end | The failed or ended call was one of the calls that make the test repository (measured for `init` and `mktree`, and for `config`, `commit-tree` and `update-ref` on `b3081b51` on Linux). The brackets hold the second `version` call's stdout, even if it fails (`4c111e86`, measured on Linux for `init`) |
| `this repository's git config cannot be used: <error lines of git>` | The read of the test repository's config failed, and the plain `git version` after it works |
| `this repository's git config cannot be used: git did not finish reading "<config of the test repository>" within 30 seconds; try again` | The read of the test repository's config ran, or held its stdout, past 30 seconds. The reply comes near 31 s. With stderr alone held, a read that exits 1 gives the lines read before the cut (not measured) |
| `mkdir <path>: <reason>` or `open <path>: <reason>`, with no quotes | A folder or the daemon's lock file cannot be made. On Windows a lock file that cannot be made gives the reason alone. With a deny entry for writes on `git-shadow` and the backup and restore privileges on, that is `Access is denied.` (`b3081b51`, measured on Windows) |

If git refuses the copy and the same call works without it, the state is
`fallback-git` and not a failure. A test call that works and answers wrongly gives
it too. At the last call, which is a `worktree add`, a `No space left on device` error or an
end by a signal fails the test. If two calls fail, the cause names the first.
A first `config --get helper.check` on the copy that the daemon ended after 30 seconds
does not fail the test (`b3081b51`, measured on Linux).

The daemon keeps a failure 10 seconds after the test, also with its cause
gone. In that time a request gets the failure with no git call, but works with
`"shadowDirOff":true`. A failed test under `"sessionFoldersOff":true` is not kept
(`4c111e86`, measured on Linux). A test that passed is not kept either
([rule](git-info.md#settingscontrol)).

### The folders

The folder of one daemon is `<socket folder>/git-shadow/<host form>-<pid>-<16 hex>`.
`<host form>` is the host name with each character outside `A-Z`, `a-z`, `0-9` and `.`
written as `_` (measured for `-` and `.` on Linux and macOS). On Linux and macOS that
folder and each folder of a copy have mode `0700`, and their files
`0600`. The daemon makes the folder at the first request that runs the test. It holds
an exclusive lock on its file `lock` for life. On Windows the hold is
an unshared open handle. On a mount whose `flock` answers `ENOLCK`, `c9125eb8`
goes on with no lock, and each git request succeeds (measured on Linux). claustrum
follows. Any other `flock` error fails the request (claustrum's own, not measured).

- At `-stop` and `server.shutdown` the daemon removes its folder. The empty
  `git-shadow` stays (Linux, macOS and Windows).
- If the folder is removed while the daemon runs, the next request makes it again
  with another hex part (Linux and macOS).
- An empty file, FIFO or link there goes, never its target, and requests
  succeed in a new folder (`4c111e86` on Linux and macOS). `shadow`,
  `fallback-git`, a missing git (`git.info`, and `git.status` on Linux and macOS)
  and a start test heal it. A test after a kept failure is measured on Linux. Other
  start tests are claustrum's own. On Linux
  and macOS one file `unlinkat` after the home guard removes it, never a folder or
  tree (from the code). Other non-folder kinds, a file with content and Windows (a
  regular file, or an empty folder in its place) are claustrum's own.
- The folder of one repository is `<folder of the daemon>/<32 hex>`. The name is the
  first 32 hex digits of the sha256 of the git directory's path. A linked
  worktree uses the folder of its main repository.
- That folder holds `HEAD`, `config`, the empty folders `objects` and `refs`, and
  `info/exclude` where the repository has it. On Linux and macOS it holds a link
  `worktrees` to the git directory's `worktrees` folder, and none on Windows.
- More copies follow the repository state: `shallow`, `info/attributes`,
  `rebase-merge/head-name`, `rebase-apply/head-name` and `BISECT_START`.
- The daemon makes the folder at the first request that accepts the repository
  config. After a file changes, a later request writes its copy again.
- A create writes the copy of `HEAD` again when it has mode 000 or no bytes, and
  succeeds (`4c111e86`, measured on Linux). A copy that is gone or has another
  text gets the same rule (claustrum's own). `4c111e86` removes an empty folder there,
  writes the copy and succeeds (measured on Linux). On Linux and macOS claustrum
  removes it by one rmdir (`removeEmptyHeadFolder`). On Windows it stays, and the
  create fails (claustrum's own). A folder with content stays, and the create fails
  (not measured). The other methods get the same rule (claustrum's own).
- `.probe` is the folder of [the repository
  call](#the-call-about-the-repository).
- A request also keeps short-lived entries in the daemon's folder:
  `.gitdir-<n>` for a `git.status`, and `.checkout-<n>` and `.worktreeinclude-<n>` for a create. `.config-<n>` for a
  create in a repository with a `config.worktree` file, and `.s<n>`. In `fallback-git` the first three are there
  too (`.gitdir-<n>` on all three systems and `.checkout-<n>` on Windows, measured, and the rest
  from the code). With `"shadowDirOff":true` the first three
  are in the temporary folder, as `claude-ssh-gitdir-<n>`, `claude-ssh-checkout-<n>` and
  `claude-ssh-worktreeinclude-<n>`. That temporary index name is as on `4c111e86` (measured on
  all three systems).
- `4c111e86` names a create's index folder
  `.checkout-<n>` (measured on all three systems). It is there at the reply and goes
  soon after, as does `claude-ssh-checkout-<n>` (measured on Linux). After an LFS space stop it goes before the reply ([the private LFS store](git-worktree-create.md#the-private-lfs-store)). claustrum
  removes either right after the write of the reply, or at the shutdown if that write
  has not ended (claustrum's own). A claustrum killed before
  that remove leaves it (claustrum's own). The next daemon removes it at its first request using the
  copy (measured on all three systems).
  claustrum removes it with the old daemon's folder in [the
  sweep](#the-sweep-of-git-shadow) (from the code). On Windows the `.checkout-<n>` folder of `4c111e86` holds no
  LFS object (measured).
- In `shadow` a create keeps `.readback-objects-<n>` there for its
  content check (`b3081b51`, measured on Linux), and `.readback-scratch-<n>` (`4c111e86`, measured on all three systems).
- On Windows, if the daemon's folder denies new folders, a create still works
  with the backup and restore privileges on, and with both off fails at `.s<n>`.
- On Linux and macOS a daemon folder with mode `0300` still takes a create. An empty request folder goes by one rmdir, and a folder with content stays (`.s<n>`,
  `.checkout-<n>` and `.readback-objects-<n>`, while an emptied `.readback-scratch-<n>` goes, `4c111e86`, measured on Linux, claustrum's own on macOS). The other methods get the same steps (claustrum's own,
  not measured). Modes `0100` and `0000` (`4c111e86`, measured on Linux and, in `shadow`,
  macOS): `0100` uses its entries and a create fails at `.s<n>`, `0000` gives the
  `mkdir` error of the folder or `.probe`.

??? note "A FIFO at a folder (Linux and macOS)"
    A FIFO at `.probe`, `.s<n>` or a repository folder gives `open <path>: not a directory` at once. One put there during the open, and the remove steps, still wait (claustrum's own, not measured).

If a repository's folder cannot be made, the request gets this text in the
three shapes of the start test:

`Claude could not prepare its own files on the remote machine: cannot write its copy of the repository's git config: <cause>`

The cause is the `mkdir` error of the folder. The same request again gets the same
line with no git call. On Windows a held copy gets 2 s of retries, then `rename
<temp file> <copy>: Access is denied.` with full paths.

#### The sweep of git-shadow

The request that makes the daemon's folder also sweeps `git-shadow` (measured
on Linux and macOS, and in part on Windows). If an entry's lock file can be
taken and its name has this host's `<host form>`, the entry goes. An entry with
another name must also be more than 7 days old (the exact bound is not measured). A
held lock keeps the folder of a live daemon.

- On Linux and macOS the age is read after the lock step, so a folder with no lock
  file gets one and stays. On Windows the age is read before, and such a folder of 8
  days goes.
- On Windows that lock also keeps it from a remove by hand, so no later request
  sweeps again.
- `c9125eb8` goes on after `ENOLCK` on Linux and a FIFO lock on macOS, and claustrum
  follows. After such an error a folder of either name goes past 7 days. The 7 days
  are claustrum's own, not measured.
- The rows show 8 days removed and 1 hour kept. Linux measured another
  host's name, and macOS this host's name form.
- Another lock error keeps the entry, and so does a FIFO lock on Linux. A link whose
  target lock gives an error stays. All three are claustrum's own, not measured.
- A folder with no lock file under 1 minute old stays and gets none
  (claustrum's own, not measured).
- A planted link, or a junction on Windows, goes as a link, and claustrum makes no
  lock file in its target ([D8](../DIVERGENCES.md#d8)).

The folder of the daemon keeps 128 repository folders. A new repository removes the
folder with the oldest use that no git call holds. A git call holds its folder until
the wait for its pipes ends (claustrum's own, not measured). With all 128 held, a
request gets this text and nothing goes:

`Claude could not prepare its own files on the remote machine: git is running in 128 repositories at once; try again`

A folder goes between git calls, never during one naming it. The next call
makes it again with no new config read, except a call right after its own config
listing (claustrum's own, not measured). A failed remake gets the `cannot write its
copy` text, or the 128 text when all 128 folders are held, and a call whose folder went and is not made
again, or whose daemon shut down or replaced it, fails alone (from the code). It gets
this text, and a listing puts `listing the configuration in force: ` before it (Linux
and Windows):

`Claude could not prepare its own files on the remote machine: its copy of this repository's settings was cleared away before git started; try again`

### The copied settings file

The `config` of the copy is the daemon's own text. Each carried setting is one block
of two lines, `[<section>]` and `<key> = "<value>"`, in the order of the repository's
file. The last `core.bare` of `config.worktree` follows, then the `[url]`
blocks of rewritten URLs. The file ends with
`[lfs]\n\trepositoryformatversion = 0\n`.

- 41 keys are carried, each with one rule of accepted values (`internal/git/gitshadowconfig.go`).
  Only some of those rules are measured.
- `remote.<name>.url`, `.lfsurl`, `.promisor` and `.partialclonefilter`,
  `lfs.<url>.access` and `branch.<name>.remote` are carried too. The length of a
  rewritten URL is not measured.
- Every other key is dropped, such as `core.hooksPath` and `user.*`.
- Outside the version and extension refusals, a value that its rule does not accept
  is left out with no refusal, for example `core.filemode = maybe`.
- Where the copy carries a value that git refuses, `git.info` answers "no
  repository" with the member (Linux, macOS and Windows), for example
  `extensions.objectformat = sha256` in a sha1 repository.
- A setting of an `[include]` file is carried at the place of the include line. Its
  format version, `extensions.*`, `core.bare` and `filter.*` (not measured) are
  dropped. A setting of an `includeIf` file is not carried.

### The call about the repository

In `shadow` the daemon runs `rev-parse --git-dir` before the method's
calls, in the work tree root. On Linux and macOS a create asks again after a good add ([git.worktree_create](git-worktree-create.md#add-and-checkout-failures)). After the add of a commit from `sourceBranch` it asks before its [content check](git-worktree-create.md#the-content-check). On Windows only a create of a branch asks, right after the add (`4c111e86`, measured), and any answer goes on (claustrum's own). It passes `-c core.hooksPath=<null device>`
and `-c core.fsmonitor=` (`c8e2f083`, measured on Windows). The call names
the folder `.probe` of the daemon. The daemon removes leftovers like
`shallow` and keeps a planted `keepme` (measured on Linux), and other names and
entries are claustrum's own. It rewrites an emptied `config`, keeps an
unchanged one and replaces a folder at `HEAD` (measured on Linux). If `.probe` cannot be prepared, no git runs. The request gets [the
`cannot write "<.probe path>"` text](errors.md#error-string-catalogue)
(Linux). The call drops the daemon's `GIT_NAMESPACE`,
`GIT_INDEX_FILE`, `GIT_DISCOVERY_ACROSS_FILESYSTEM` and
`GIT_ALTERNATE_OBJECT_DIRECTORIES`, and keeps its `GIT_CONFIG_COUNT` pairs (Linux,
macOS and Windows). A good answer is kept 5 seconds after the call ends. A change of the root
folder or its `.git` entry makes the daemon ask again.

An exit 128 with any text gets a second run of the call plus `GIT_DIR=<.probe>`, in
`.probe` (`52d82f0a` on Linux and macOS, `c8e2f083` on Windows). Exit 0 there gives
"no repository" on Windows (`c8e2f083`). The daemon kills a second run 30
seconds after its start, and the reply is the error of the rows below, with the text
of the first call or its exit status (`c8e2f083`, Linux and Windows). So does a non-zero
exit (claustrum's own).

A third run follows that exit 0, in the root, without user or
system config or `GIT_DIR` (`52d82f0a`; `4c111e86` on a Windows share, statuses not logged). On Linux and macOS
its exit 0 gives that error plus `; if that names a git setting or file, fix it, then try
again`, a stop at that bound gives the last row (`52d82f0a`,
Linux), and a failure "no repository" (claustrum's own). On Windows each gives "no
repository" (seen on that share, else claustrum's own). A bare repository gets no third
run (Linux). A `refs` made
[unsearchable](git-rules-trust.md#how-the-daemon-finds-the-git-directory) right after a good
answer gives "no repository" (`52d82f0a` on Linux, claustrum's own on macOS).

| Answer of git | Reply |
|---|---|
| exit 1, a signal (`signal: terminated`) or a git that cannot start (Windows) | `git could not check this repository on the remote machine: at "<root>": exit status <n>` |
| exit 1 or exit 2 with a text | The same text with git's text in place of `exit status 1`. Two lines stand there, one space apart. It holds on all five methods |
| either of the two rows above | One `git version` call follows. A failed one gives `git cannot run on this host; git not run: <exec error>: <git text>` in the frame of each method (not a create without git: `4c111e86`, Linux and macOS, and Windows for a missing `baseRepo`). A remove makes two such calls and deletes nothing. Not measured: a git that cannot start, `git.info` and `git.worktree_create` after a signal, a remove with `worktreeRoot` |
| another path than the git directory, in two calls | `git and Claude disagree about which repository holds this folder: from "<root>" git finds "<path>", Claude finds "<git directory>"; try again`. `<path>` is the path as git printed it: with forward slashes on Windows. On Windows the answers `<R>/../OTHER/.git` and `<R>/.git/..` keep their `..` (`b3081b51`, measured on Windows). On Linux and macOS a path that is not clean is shown cleaned, and on every system a path that is not absolute is shown cleaned from `<root>` (claustrum's own, not measured). No output gives `<root>` |
| no answer in 30 seconds | `git could not check this repository on the remote machine: it did not finish within 30 seconds in "<root>"; try again` (Linux, macOS and Windows) |

The three texts have the shapes of the start test. A bad answer is not kept, and a
remove asks twice. After a second "disagree" answer that names another path, a remove
without `worktreeRoot` asks twice more, 8 calls in all. `c9125eb8` does so on macOS
in a remove right after a failed create (measured). Linux, Windows, an answer with no
output, a later good answer and the other bad answers are claustrum's own, not measured. For a repository of another owner with no `safe.directory`,
`git.worktree_remove` answers the lock-check refusal that ends `(its registrations
could not be examined); retry`. With a child that holds the pipes, an exit 0 counts
by its output so far (measured on `git.status` only).

### The git calls

In the state `shadow` each hardened call carries `GIT_COMMON_DIR=<copy>` and
`GIT_OBJECT_DIRECTORY=<git directory>/objects`. A call with no `--git-dir=` in its
arguments also carries `GIT_DIR` and `GIT_WORK_TREE` (Linux, macOS and Windows). The
four come right after the profile values, in that order (order measured on Linux and
macOS). The staged `worktree add` moves its `GIT_COMMON_DIR` to the end ([staged
add](git-worktree-create.md#the-staged-add)).
[Hardened git calls](git-rules-calls.md#hardened-git-calls) has the other points of a call.

### `config.worktree`

The copy has no `extensions.worktreeConfig` setting, so git reads no
`config.worktree` file in the state `shadow`. The daemon does two things in its
place, in a repository with that extension and a `config.worktree` file. The
`worktreeConfig` values `2` and `10` turn that extension on.

- Each key of the copied settings file but the format version, `extensions.*` and
  `core.bare` goes before the option list of the git calls as one `-c` item, with the copy's value,
  sorted by key. Six keys are measured. Other keys and a repeated key are not
  measured. A call on a linked worktree gets the items of its own file.
- A new worktree of [git.worktree_create](git-worktree-create.md) gets its own
  `config.worktree` file.
