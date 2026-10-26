# Settings copy of the git methods

In the state `shadow`, when the start test passes, the daemon keeps its own copy of the git settings of a repository. The state `fallback-git` and `"shadowDirOff":true` make no copy. This page gives its states, folders and git calls.

An untagged rule on this page was measured against build `5fd08069` on Linux. A rule that names a system was measured on that system.

## The settings copy

The daemon keeps its own copy of the git settings of a repository. The copy is in a
folder `git-shadow` beside the socket. The git calls of the five git methods name
that copy, so git reads the copy and not the config of the repository.

### The states and `settingsControl`

`git.info` reports the state in its last member, `settingsControl`. No other method
sends it, and an error reply has none (Linux, macOS and Windows).

| `settingsControl` | When | What it changes |
|---|---|---|
| no member | No start test of the daemon gave a state yet | Nothing. A folder with no repository, or a `HEAD` that is no ref, starts no test. A `git.info` on a `.git` file to a missing repository starts it |
| `"shadow"` | The start test passed | The git calls name the copy. Before them the daemon asks git about the repository |
| `"fallback-git"` | The git of the host does not work on such a copy, for example git 2.11.4 and 2.12.5 | The git calls name the git directory. The daemon reads no repository config, gives no config refusal, makes no copy and does not ask git about it |
| `"off-switch"` | A request with `"shadowDirOff":true` runs (the hold) when the reply is made | A request that starts then names the git directory, with no test, folder or config read |

- After the test the "no repository" reply has the member too:
  `{"isRepo":false,"repoSlug":"","defaultBranch":"","settingsControl":"shadow"}`.
  That reply waits for a running test and has its state. After a failed test it has
  no member. A path that does not exist gets the same, but starts no test itself. On
  a folder with no repository the other methods answer at once.
- As the first git request of a daemon, a `git.info` on a folder whose `.git` file
  names a missing path runs the test and gives that reply. A missing entry under
  `worktrees` of an existing git directory runs no test, and the reply has no member
  (Linux, macOS and Windows). A missing entry in a missing repository runs the test.
- With no git in the daemon's `PATH`, a `git.info` on a repository answers "no
  repository" with no test. The daemon still makes its folder, with the lock file
  only. The member follows the daemon's state (not measured).
- A `git.info` on a regular file gets the gate of the repository of its folder.
  Outside any repository it starts no test. As the first git request of a daemon its
  reply then has no member on Linux and macOS. On Windows it is the hooks refusal,
  with `fork/exec <git.exe>: The directory name is invalid.`
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
  the hold is not stopped at 2 minutes during the hold. It is not stopped at 2
  minutes also after the holder answers (claustrum's own, not measured). A held call
  that starts after that answer has the 2 minute limit (claustrum's own, not
  measured).
- In the state `fallback-git` the test runs again on the first request more than 10
  minutes after the last test. In the state `shadow` no second test ran up to 720 s.
- The daemon logs one `[GitHandler]` line at the first decision of the state and one
  at a change.

### The start test

The first request without the member that reaches a repository, or a `git.info` on a
`.git` file to a missing repository, runs the test. On a `.git` file that names a
missing path the steps after the test differ from `5fd08069` (measured on macOS and
Windows), with equal frames (see
[UPSTREAM-TRACKING.md](../UPSTREAM-TRACKING.md#triage-gotchas-when-a-probe-result-is-misleading)). The
test makes a small repository and a copy of it in a folder `.check-<n>` of the
daemon, and runs git calls there. One of them is the read of the test repository's
config. It runs in the folder of [the calls outside a repository](git-rules-calls.md#calls-outside-a-repository) (claustrum's own, not measured).
The test folder is gone at the reply. If the test fails, the request gets this
text:

`Claude could not test git on the remote machine (<cause>). Check free disk space and quota there, then try again.`

`git.info`, `git.status` and `git.list_branches` answer it as a `-32603` error.
`git.worktree_create` answers it in `error`, with the `errorCode`
`worktree_add_failed`. `git.worktree_remove` answers it after `failed to remove
worktree: could not check whether <worktreePath> is locked: `, with no `errorCode`.

A failed read of the test repository's config is followed by one `git version` call. If it fails
too, the text is `git cannot run on this host; git not run: <exec error>: <git text>`
alone, in the same frames. The exec error and the text are those of `git version`. A
remove deletes nothing there (Linux, macOS and Windows).

| `<cause>` | When |
|---|---|
| `git <verb> failed under "<folder of the daemon>": <error lines of git, space-joined>` | A test call other than the read of the test repository's config failed. Go's `%q` quotes the folder |
| `git <verb> failed under "<folder>": signal: killed` | The daemon ended such a test call after 30 seconds (Linux and macOS). The reply comes near 31 s (Linux). On Windows the end is `exit status 1` |
| the same with ` (git version <v>)` at its end | The failed or ended call was one of the calls that make the test repository (measured for `init` and `mktree`) |
| `this repository's git config cannot be used: <error lines of git>` | The read of the test repository's config failed, and the plain `git version` after it works |
| `this repository's git config cannot be used: git did not finish reading "<config of the test repository>" within 30 seconds; try again` | The read of the test repository's config ran, or held its stdout, past 30 seconds. The reply comes near 31 s. With stderr alone held, a read that exits 1 gives the lines read before the cut (not measured) |
| `mkdir <path>: <reason>` or `open <path>: <reason>`, with no quotes | A folder or the lock file of the daemon cannot be made |

If git refuses the copy and the same call works without it, the state is
`fallback-git` and not a failure. A test call that works and answers wrongly gives
it too. At the last call, which is a `worktree add`, a `No space left on device` error or an
end by a signal fails the test. If two calls fail, the cause names the first.

The daemon keeps a failure for 10 seconds from the end of the failed test, also
after its cause is gone. A request in that time gets the kept text with no git call.
A request with `"shadowDirOff":true` works in that time.

### The folders

The folder of one daemon is `<socket folder>/git-shadow/<host form>-<pid>-<16 hex>`.
`<host form>` is the host name with each character outside `A-Z`, `a-z`, `0-9` and `.`
written as `_` (measured for `-` and `.` on Linux and macOS). On Linux and macOS that
folder and each folder of a copy have mode `0700`, and the files of a copy have
`0600`. The daemon makes the folder at the first request that runs the test. It holds
an exclusive lock on the file `lock` in it for its whole life. On Windows the hold is
an open handle with no sharing.

- At `-stop` and at `server.shutdown` the daemon removes its folder. The empty
  `git-shadow` stays (Linux, macOS and Windows).
- If the folder is removed while the daemon runs, the next request makes it again
  with another hex part (Linux and macOS).
- The folder of one repository is `<folder of the daemon>/<32 hex>`. The name is the
  first 32 hex digits of the sha256 of the path of the git directory. A linked
  worktree uses the folder of its main repository.
- That folder holds `HEAD`, `config`, the empty folders `objects` and `refs`, and
  `info/exclude` where the repository has it. On Linux and macOS it holds a link
  `worktrees` to the `worktrees` folder of the git directory. On Windows it has no
  such entry.
- More copies follow the state of the repository: `shallow`, `info/attributes`,
  `rebase-merge/head-name`, `rebase-apply/head-name` and `BISECT_START`.
- The daemon makes the folder at the first request that accepts the config of the
  repository. After a change of a file, a later request writes its copy again.
- [The call about the repository](#the-call-about-the-repository) names a folder
  `.probe` of the daemon.
- One request also keeps short-lived entries in the folder of the daemon:
  `.gitdir-<n>` for a `git.status`, and `.index-<n>` and `.worktreeinclude-<n>` for a
  create. Also `.config-<n>` for a create in a repository with a `config.worktree`
  file, and `.s<n>` on Windows. In the state `fallback-git` the first three are there
  too (`.gitdir-<n>` on Linux and macOS, `.index-<n>` on Windows, and
  `.worktreeinclude-<n>` from the code). With `"shadowDirOff":true` the first three
  are in the temporary folder, as `claude-ssh-gitdir-<n>`, `claude-ssh-index-<n>` and
  `claude-ssh-worktreeinclude-<n>`. On Linux and macOS the index name is as on
  `89cb6289`. On Windows it is claustrum's own (not measured).
- On Windows, if the folder of the daemon denies new folders, a create still works
  with the backup and restore privileges on. With both off it fails at `.s<n>`.

If the folder of a repository cannot be made, the request gets this text in the
three shapes of the start test:

`Claude could not prepare its own files on the remote machine: cannot write its copy of the repository's git config: <cause>`

The cause is the `mkdir` error of the folder. The same request again gets the same
line with no git call. On Windows a held copy gets 2 s of retries, then `rename
<temp file> <copy>: Access is denied.` with full paths.

The request that makes the folder of the daemon also sweeps `git-shadow` (measured
on Linux and macOS, and in part on Windows). If the lock file of an entry can be
taken and its name has the host form of this host, the entry goes. An entry with
another name must also be more than 7 days old (the exact bound is not measured). A
held lock keeps the folder of a live daemon.

- On Linux and macOS the age is read after the lock step, so a folder with no lock
  file gets one and stays. On Windows the age is read before, and such a folder of 8
  days goes.
- On Windows the held lock keeps the folder of a live daemon from a remove by hand,
  so no later request sweeps again.
- A lock error other than a held lock keeps the entry. A folder with no lock file
  that is less than 1 minute old stays and gets none. Both are claustrum's own, not
  measured.
- A planted link, or a junction on Windows, goes as a link, and claustrum makes no
  lock file in its target ([D8](../DIVERGENCES.md#d8)).

The folder of the daemon keeps 128 repository folders. A new repository removes the
folder with the oldest use that no git call holds. A git call holds its folder until
the wait for its pipes ends (claustrum's own, not measured). With all 128 held, a
request gets this text and nothing goes:

`Claude could not prepare its own files on the remote machine: git is running in 128 repositories at once; try again`

A folder goes between the git calls of a request, never during one. The next call
makes it again with no new config read, except a call right after its own config
listing (claustrum's own, not measured). A failed remake gets the `cannot write its
copy` text above, or the 128 text when all 128 folders are held (from the code). A call whose folder went and is not made
again, or whose daemon shut down or replaced it, fails alone (from the code). It gets
this text, and a listing puts `listing the configuration in force: ` before it (Linux
and Windows):

`Claude could not prepare its own files on the remote machine: its copy of this repository's settings was cleared away before git started; try again`

### The copied settings file

The `config` of the copy is the daemon's own text. Each carried setting is one block
of two lines, `[<section>]` and `<key> = "<value>"`, in the order of the file of the
repository. The last `core.bare` of `config.worktree` follows, then the `[url]`
blocks of rewritten URLs. The file ends with
`[lfs]\n\trepositoryformatversion = 0\n`.

- 41 keys are carried, each with one rule of accepted values (`gitshadowconfig.go`).
  Only some of those rules are measured.
- `remote.<name>.url`, `.lfsurl`, `.promisor` and `.partialclonefilter`,
  `lfs.<url>.access` and `branch.<name>.remote` are carried too. The length of a
  rewritten URL is not measured.
- Every other key is dropped, such as `core.hooksPath` and `user.*`.
- Outside the version and extension refusals, a value that its rule does not accept
  is left out with no refusal. One example is `core.filemode = maybe`.
- Where the copy carries a value that git refuses, `git.info` answers "no
  repository" with the member (Linux, macOS and Windows). One example is
  `extensions.objectformat = sha256` in a sha1 repository.
- A setting of an `[include]` file is carried at the place of the include line. Its
  format version, `extensions.*`, `core.bare` and `filter.*` (not measured) are
  dropped. A setting of an `includeIf` file is not carried.

### The call about the repository

In the state `shadow` the daemon runs `rev-parse --git-dir` before the method's
calls, in the root of the work tree. It passes `-c core.hooksPath=<null device>`
and `-c core.fsmonitor=` (build `c8e2f083`, measured on Windows). The call names
the folder `.probe` of the daemon. The daemon removes leftovers like
`shallow` and keeps a planted `keepme` (measured on Linux). Other names and
entries are claustrum's own (not measured). It rewrites an emptied `config`, keeps an
unchanged one and replaces a folder at `HEAD` (measured on Linux). If `.probe` cannot be prepared, no git runs. The request gets [the
`cannot write "<.probe path>"` text](errors.md#error-string-catalogue)
(measured on Linux: mode 0500, no `HEAD`). The call drops the daemon's `GIT_NAMESPACE`,
`GIT_INDEX_FILE`, `GIT_DISCOVERY_ACROSS_FILESYSTEM` and
`GIT_ALTERNATE_OBJECT_DIRECTORIES`, and keeps its `GIT_CONFIG_COUNT` pairs (Linux,
macOS and Windows). So a pair such as `safe.directory` changes the answers (from the
code). A good answer is kept 5 seconds from the end of the call. A change of the root
folder or of its `.git` entry makes the daemon ask again.

An exit 128 with any text gets a second run of the call plus `GIT_DIR=<.probe>`.
It runs in `.probe` (measured on Windows, claustrum's own elsewhere). Exit 0 there gives the "no
repository" answer (`c8e2f083`, measured on Linux and Windows). The daemon kills
a second run 30 seconds after its start. The reply is then the error of the rows
below, with the text of the first call (`c8e2f083`, measured on Linux and
Windows). A non-zero exit there gets the same reply (claustrum's own, not
measured). With no text of the first call, the reply ends in its exit status.

| Answer of git | Reply |
|---|---|
| exit 1, a signal (`signal: terminated`) or a git that cannot start (Windows) | `git could not check this repository on the remote machine: at "<root>": exit status <n>` |
| exit 1 or exit 2 with a text | The same text with git's text in place of `exit status 1`. Two lines stand there with one space between them. This row holds on all five methods |
| either of the two rows above | One `git version` call follows. If it fails, the reply is `git cannot run on this host; git not run: <exec error>: <git text>` in the frame of each method. A remove makes two such calls and deletes nothing. Not measured: a git that cannot start, `git.info` and `git.worktree_create` after a signal, a remove with `worktreeRoot` |
| another path than the git directory, in two calls | `git and Claude disagree about which repository holds this folder: from "<root>" git finds "<path>", Claude finds "<git directory>"; try again`. `<path>` is the path as git printed it: with forward slashes on Windows |
| no answer in 30 seconds | `git could not check this repository on the remote machine: it did not finish within 30 seconds in "<root>"; try again` (Linux, macOS and Windows) |

The three texts have the shapes of the start test. A bad answer is not kept, and a
remove asks twice. For a repository of another owner with no `safe.directory`,
`git.worktree_remove` answers the lock-check refusal that ends `(its registrations
could not be examined); retry`. With a child that holds the pipes, an exit 0 counts
by its output so far (measured on `git.status` only).

### The git calls

In the state `shadow` each hardened call carries `GIT_COMMON_DIR=<copy>` and
`GIT_OBJECT_DIRECTORY=<git directory>/objects`. A call with no `--git-dir=` in its
arguments also carries `GIT_DIR` and `GIT_WORK_TREE` (Linux, macOS and Windows). The
four come right after the profile values, in that order (order measured on Linux and
macOS).
[Hardened git calls](git-rules-calls.md#hardened-git-calls) has the other points of a call.

### `config.worktree`

The copy has no `extensions.worktreeConfig` setting, so git reads no
`config.worktree` file in the state `shadow`. The daemon does two things in its
place, in a repository with that extension and a `config.worktree` file. The
`worktreeConfig` values `2` and `10` turn that extension on.

- Each key of the copied settings file but the format version, `extensions.*` and
  `core.bare` goes before the option list of the git calls as one `-c` item, with the value of the copy,
  sorted by key. Six keys are measured. Other keys and a repeated key are not
  measured. A call on a linked worktree gets the items of its own file.
- A new worktree of `git.worktree_create` gets its own `config.worktree` file. See
  [git.worktree_create](git-worktree-create.md).
