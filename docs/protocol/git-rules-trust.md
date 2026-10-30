# Trust check of the git directory

An untagged rule on this page matches build `5fd08069` or an earlier build. A section that names its systems or its build keeps that scope.

## Git-directory trust check

The folder of the check is `path` for `git.info` and `git.list_branches`, and
`baseRepo` for `git.status`, `git.worktree_create` and `git.worktree_remove`. The check has three outcomes. "Trusted" lets git run. "No
repository" answers as if the folder held no repository. "Refused" answers with
one of the texts M2 to M6 and S1 to S3, and git does not run.

### How the daemon finds the git directory

When the daemon's own environment sets neither `GIT_DIR` nor `GIT_COMMON_DIR`,
the daemon walks up from the request folder:

- The walk starts at the request folder with symlinks resolved. A refusal names
  the resolved path, never the alias.
- On Windows the walk resolves no junction before the last component of the
  path. It starts at the path as spelled, and it takes a `..` by text. Take a
  repository `<P>` with a junction `<P>\J` into a repository `<R>`. For
  `<P>\J\sub` each git call carries `GIT_COMMON_DIR=<P>\.git`. Through a second
  junction `<KP>` to `<P>`, the pin is `<KP>\.git`. A junction as the last
  component gives the pin of `<P>` too. A folder symlink in the place of the
  junction resolves, and the pin is `<R>\.git`. A junction outside any repository
  gives "no repository", and each git call carries `GIT_DIR=NUL`.
- On Windows the resolve of a folder path can fail, for example at a missing name
  before `..`. The walk then starts at the spelled path too (from
  the code). Only `git.info` reaches that start (from the code).
  `git.list_branches` and `git.status` resolve the path first, and so do
  `git.worktree_create` and `git.worktree_remove` for a `baseRepo` that exists
  (from the code). If that resolve fails, they answer there (from the code).
- On Windows a refusal of a config on `git.info` follows the spelled start too. In
  the state `shadow` without `"shadowDirOff":true`, it is the class 1 text of
  [Repository config refusals](git-rules-config.md#repository-config-refusals) (from the code). A
  config that git refuses and the daemon's read accepts, such as an `include.path`
  with no value, gets the hooks refusal (from the code). So does any refused
  config outside that state (from the code).
- `process.spawn` does not use the walk.
- A `.git` folder that passes the git-directory test is the git directory.
- A `.git` folder that fails the test ends the walk. If it holds a `commondir`
  entry of any type, it is the git directory, and the rules of a stray
  `commondir` judge it. Otherwise the answer is "no repository". Git itself walks
  on to an outer repository here.
- A regular `.git` file up to 1 MiB starts with `gitdir: `, one blank. First
  trailing CRs and LFs go. Other trailing white space stays. An empty rest means
  "no repository". Then a NUL ends the path. Blank, white-space and NUL rules:
  `c8e2f083`, Linux. A relative path (on Windows also
  `/x`) joins the holding folder. A path that the NUL makes empty is that folder.
  Else "no repository" (claustrum's own, not measured).
- A `.git` of another type, such as a FIFO, is skipped, and the walk goes on.
- With no `.git`, the folder itself is the git directory if it passes the test.
  That is a bare repository. Nothing found up to the root means "no repository".
- A request folder that does not exist is left to git.

The git-directory test has three parts. `objects` and `refs` exist, of any type on
Windows, searchable on Linux and macOS. A root daemon passes. `refs` 0600 is measured on
`git.info`, `git.list_branches`, a create and a remove (build `52d82f0a`). The rest
is claustrum's own. `HEAD`
passes. A regular `HEAD` passes when its first 255 bytes start with `ref:`,
then any spaces, tabs, CRs or LFs, then `refs/`. It also passes when it starts
with 40 hex digits in either case. A symlink `HEAD` passes when its target text
starts with `refs/`. Any other `HEAD` fails at once, a FIFO for example.

### How the daemon judges the git directory

The daemon then judges the git directory G:

- G is a linked-worktree entry when its parent is named `worktrees`, its own name
  is not `.git`, and its grandparent passes the git-directory test. The entries of
  a main repository, a bare repository and a submodule all count.
- G is not an entry and holds no `commondir`: trusted.
- G is not an entry and holds a `commondir`: the rules of a stray `commondir`
  judge it. If only the grandparent test fails, a `commondir` that names it gets
  M6 (build `52d82f0a`, Linux, `refs` moved away, the rest
  claustrum's own).
- G is an entry without `commondir`: M3.
- G is an entry whose `commondir` is not a regular file of at most 1048576 bytes:
  M4. A symlink is followed only when it is relative and stays inside the entry.
- Otherwise trailing CRs and LFs go, and a NUL ends the value (build
  `52d82f0a`, Linux and Windows). macOS and the step order are not measured. It is taken relative to G when it is not
  absolute, and cleaned lexically. It has to equal
  the grandparent as a string. No symlink in it is resolved, so naming the
  repository through an alias fails. On Linux and macOS a backslash is an
  ordinary character. On Windows the comparison ignores letter case and slash
  direction. A `\\?\` prefix or a path without a drive letter still fails there.
  Any other value gets M2. That covers another repository, a missing path and
  an empty file.
- A trusted git directory runs git with `GIT_COMMON_DIR` pinned to the repository
  git directory. For an entry that is the grandparent. For any other git
  directory it is that directory itself.
- The pin also keeps git from walking past a trusted nested `.git`. On Windows a
  nested `.git` whose `objects` is a regular file answers for itself.
- An entry folder that is gone means "no repository". On Linux and macOS an entry
  replaced by a regular file means the same. On Windows it gets M3.
- A `.git` file that names a plain folder, another repository's git directory or
  the main git directory is not refused. Git answers for itself there.
- On Windows the daemon does not expand 8.3 short names in G. A `.git` file or a
  daemon `GIT_DIR` can name an entry as `…\GIT~1\WORKTR~1\<name>`. That G is not
  an entry, so its `commondir` gets S1 for the content `../..` (the S1 text there
  is not measured).
- On Windows the daemon resolves G when a component of G is a symlink. Else it
  keeps G as spelled, also with a junction before its last component. For a
  junction `<KP>` to a repository, the pin is `<KP>\.git`.

### A stray `commondir`

A stray `commondir` is one in a git directory G that is not an entry: a main
`.git`, a bare repository or a submodule's git directory. The daemon judges it in
these steps:

1. A `commondir` symlink that leaves G is not followed. A dangling symlink whose
   target is relative and stays in G counts as none when G passes the
   git-directory test. G is then trusted, and git itself fails to read the file.
   The method answers what git's failure gives: `git.info` with no `branch`,
   `git.list_branches` `-32603 "exit status 128"`, and the failed add on
   `git.worktree_create`. With `worktreeRoot`, `git.worktree_remove` answers
   `cannot list the repository's worktrees: exit status 128` and deletes nothing
   (measured on Linux). Without `worktreeRoot` it answers
   `{"success":true,"branchKept":true}`. It deletes the worktree folder and its
   entry, and the branch stays (measured on Linux). If the folder is gone before
   the request, it deletes the entry (measured on Linux). When G fails the
   git-directory test, that symlink gets S3 (measured on Linux).
2. A `commondir` that is not a regular file of at most 1048576 bytes gets S2 with
   its reason. A relative symlink that stays in G is followed. A dangling symlink
   that is absolute, or that leaves G, gets S2 with `openat commondir: path
   escapes from parent` (measured on Linux).
3. The content with every trailing CR and LF removed, up to a NUL, is exactly `.`
   or `./` (the NUL is claustrum's own, not measured). A space or a tab is not trimmed. Any other content gets S1, which quotes it. So
   ` .`, `. `, `.<TAB>`, `./x`, `..`, an empty file, `.\` and the absolute path of
   G itself are all refused.
4. G then passes the git-directory test, or it gets S3. When both S2 and S3 apply,
   which one answers is not measured.
5. Else G is trusted.

### The daemon's `GIT_DIR` and `GIT_COMMON_DIR`

The daemon's own environment changes the check as follows:

- A `GIT_DIR` names the git directory to judge, for every request folder, even a
  plain one. A relative value is taken relative to the request folder. A
  `GIT_DIR` that names a gone entry gets M5. Any other `GIT_DIR` that does not
  exist is left to git.
- On Linux and macOS a `GIT_DIR` that names a `.git` file means "no repository".
  On Windows that `.git` file is trusted and pinned to itself. Without
  `"shadowDirOff":true` class 2 of
  [Repository config refusals](git-rules-config.md#class-2-files-of-the-git-directory) then answers.
- Git then runs with `GIT_COMMON_DIR` pinned to that `GIT_DIR`, unless the
  daemon's environment sets `GIT_COMMON_DIR` (measured on Linux and Windows). A
  relative value is first joined to the request folder (measured on Linux and
  Windows). On Windows `/dev/null` names nothing, so it becomes
  `<request dir>\dev\null`.
- A `GIT_COMMON_DIR` turns the check off for `git.info` and `git.list_branches`.
  The other three methods still run it. `git.worktree_create` then prefixes the
  refusal with `git worktree add failed: cannot locate the repository's git
  directory: `.
- `GIT_CONFIG`, `GIT_CONFIG_PARAMETERS` and `GIT_CEILING_DIRECTORIES` do not
  change the check.

### Trust refusal texts and frames

M2 to M5 are texts of `f6010b97`, S1 and S2 of `89cb6289`, M6 and S3 of `52d82f0a`.
M2 to M4, M6 and S1 to S3 start with `the repository's git directory could not be
trusted; git not run: `, all but M6 then `commondir not as git writes it: `. Each `%q` of a path is Go
quoting of the path cut to its first 300 runes. The content in S1 is cut to its
first 60 runes instead. A non-ASCII path is cut by runes, not bytes. An invalid
UTF-8 byte stays a byte and prints as `\xff`. The rest of the text is never cut.

| id | text after the prefix | operands |
|---|---|---|
| M2 | `%q does not name the entry's own repository, %q; restore the file to read ../.. or remove that worktree entry and add the worktree again` | `<entry>/commondir`, `<repo git dir>` |
| M3 | `worktree entry %q has none (git always writes one there, containing ../..); the entry is damaged or was not made by git — recreate the worktree with git worktree add, or remove the entry` | `<entry>` |
| M4 | `%q is not a small plain file (<reason>); remove it, or remove that worktree entry and add the worktree again so git rewrites it` | `<entry>/commondir` |
| M6 | `this worktree's repository, %q, has a missing or damaged HEAD, objects or refs, or your account on the remote machine cannot open them; repair that directory or its permissions` | `<repo git dir>` |
| S1 | `%q reads %q; git writes that file only inside .git/worktrees/<name>/, and Claude Code's sandbox may leave one containing just "."; anything else sends git's configuration and hooks elsewhere, so treat it as tampering: delete it, and if you did not create it, find out what did` | `<G>/commondir`, the content |
| S2 | `%q is not a small plain file (<reason>); git writes that file only inside .git/worktrees/<name>/; one here can send git's configuration and hooks elsewhere, so treat it as tampering: delete it, and if you did not create it, find out what did` | `<G>/commondir` |
| S3 | `%q sits in a git directory git would not take as a repository (its HEAD, objects or refs missing, invalid, or not open to your account on the remote machine), so git would look for one somewhere else; repair that directory (usually its HEAD file)` | `<G>/commondir` |

`<name>` in S1 and S2 is literal text, and the JSON encoder writes it as
`\u003cname\u003e`.

The M4 and S2 reasons seen include `commondir is not a regular file`,
`commondir is larger than 1048576 bytes`,
`openat commondir: path escapes from parent`,
`openat commondir: no such file or directory` (M4 only) or
`openat commondir: permission denied`. On Windows the OS text differs, for
example `openat commondir: The system cannot find the file specified.`.

M5 has no common prefix. It is `config-defined hooks could not be pinned off; git
not run: the worktree entry this folder's .git names no longer exists`.

### Frames

| method | refused | no repository |
|---|---|---|
| `git.info` | `{"error":{"code":-32603,"message":<text>}}` | `{"isRepo":false,"repoSlug":"","defaultBranch":""[,"settingsControl":"<state>"]}` |
| `git.list_branches` | `{"error":{"code":-32603,"message":<text>}}` | `{"isRepo":false,"branches":[]}` |
| `git.status` | `{"error":{"code":-32603,"message":<text>}}` | `{"isRepo":false,"clean":false}` |
| `git.worktree_create` | `{"success":false,"error":<text>,"errorCode":"worktree_add_failed"}` | `{"success":false,"error":"not a git repository","errorCode":"not_a_repo"}` |
| `git.worktree_remove` | see [The repository checks](git-worktree-remove.md#the-repository-checks) | see there |

The lock-check refusal is
`{"success":false,"error":"failed to remove worktree: could not check whether <worktreePath> is locked (its registrations could not be examined); retry"}`.
It has no `errorCode`, and the path is neither quoted nor cut. The work-tree
refusal is `{"success":false,"error":"failed to remove worktree: cannot determine the
repository's work tree: <reason>"}`, also with no `errorCode`. With `worktreeRoot`,
the listing classes below give their own reason. With `worktreeRoot` and no `git`
on `PATH`, the exec error replaces both texts of the table (class 1 below).

### A folder with no repository

Where the trust check finds no repository, the daemon still runs the configuration
listing in that folder. In a plain folder it carries `GIT_DIR=<null>` (see
[The daemon's own git environment](git-rules-calls.md#the-daemons-own-git-environment)). The frames
are then those of the "no repository" column. A listing that fails by class 3
below gives the "refused" column. A `.git` file to a gone entry under the
`worktrees` folder of a repository gets `GIT_DIR=<null>` too (measured on Linux).
For `git.worktree_remove` with `worktreeRoot`
this is measured on Linux and macOS.

A daemon `GIT_DIR` that names a regular file differs by system. On Linux and macOS
no listing runs, and the "no repository" column answers, also with an empty
`GIT_CONFIG_KEY_0` (measured for `git.info` on Linux and macOS). The other methods
are from the code. On Windows a file with no `gitdir:` line gets the refusal of a
config on the five methods: `this repository's git config cannot be used: open
<GIT_DIR>: not a directory` (measured on Windows). With a git older than 2.56, a
`.git` file to a gone git dir keeps the "no repository" frame of `git.info`
(measured on Linux and macOS).

Two states are from the code: a daemon `GIT_COMMON_DIR`, whose listing gets no
`GIT_DIR=<null>`, and on Linux and macOS a `.git` file that names a regular file.
In both, `git.worktree_create` runs no listing where the trust check finds no
repository, and it keeps the "no repository" column (from the code).
`git.worktree_remove` keeps that column unless its listing fails (from the code).
For the `.git` file, `git.info` and `git.list_branches` run no listing either
(from the code). The listing of `git.status` still carries `GIT_DIR=<null>`, and
its failure is the refusal of `git.status` (from the code).

### A failed configuration listing

When the configuration listing fails, the daemon sorts the failure into a class.
The classes come in this order:

1. No `git` on `PATH`: "no repository", and no process starts.
   `git.list_branches`, `git.status` and `git.worktree_create` answer their "no
   repository" shapes, the create with and without `worktreeRoot` (measured on
   Linux). On a repository the start test of
   [the settings copy](git-rules-settings-copy.md#the-start-test) comes first for these three methods,
   unless the request has `"shadowDirOff":true` (from the code).
   `git.worktree_remove` without `worktreeRoot` answers
   `{"success":true,"branchKept":true}` for a gone worktree with a `branchName`,
   and the lock-check refusal for a worktree directory that exists (measured on
   Linux). With `worktreeRoot` and a `baseRepo` that exists, it answers the
   work-tree refusal with the exec error alone, before the trust check: `exec:
   "git": executable file not found in $PATH` (measured on Linux). A `baseRepo`
   that does not exist gets no work-tree refusal (see
   [git.worktree_remove](git-worktree-remove.md#the-repository-checks)).
   A refused daemon `GIT_CONFIG_COUNT` answers before the exec error when `stat`
   reads `baseRepo` (not measured).
2. The listing exits 128, and its call names no git dir: no `--git-dir=` option and no
   `GIT_DIR=` entry in its environment, exact case. claustrum then searches upward
   from the folder for a git dir. It uses its own `GIT_CEILING_DIRECTORIES` and stops
   at a ceiling. It stops at a `.git` directory that fails the git-directory test
   and holds no `commondir`. On Linux and macOS it stops at a device change. A
   search that finds no git dir gives "no repository". So does a git dir that fails
   the git-directory test and has a bad `HEAD` or no `commondir`. The text is `git
   finds no repository here: <stderr head>`. In the other cases class 3 runs. The
   text of git plays no part (claustrum's own, not measured). In a folder with no
   repository the listing carries `GIT_DIR=<null>`, so an exit 128 there takes
   class 3 (`c8e2f083`, measured on Linux and Windows).
   On `5fd08069` exit 128 with a stderr that starts with `fatal: not a git
   repository` gives "no repository".
   With a `branchName`, `git.worktree_remove` without `worktreeRoot` keeps the
   branch, and a gone worktree answers `{"success":true,"branchKept":true}`. For a
   `.git` file to a gone git dir, git 2.56 says `gitfile does not point to a valid
   repository` instead (measured on Linux and Windows). A gone entry under
   `.git/worktrees` then answers "no repository" on each method but
   `git.worktree_remove` (measured on Linux and Windows). Another gone git dir
   answers the error of
   [the call about the repository](git-rules-settings-copy.md#the-call-about-the-repository) in the folder
   that holds `.git` (measured on Linux and Windows). `git.info` runs that call
   itself, and its second run gives "no repository" (`c8e2f083`, measured on
   Linux). With `"shadowDirOff":true`
   that git dir gets the hooks refusal on every method (measured on Linux).
3. Any other failure, a failed start included. The daemon runs `git version`. It
   runs in the folder of [the calls outside a repository](git-rules-calls.md#calls-outside-a-repository) (claustrum's own, not measured).
   If that call fails too, the text is `git cannot run on this
   host; git not run: <exec error>: <git text>`. Else it is the hooks refusal.

The `git version` call of class 3 has the profile of the failed listing (see
[Hardened git calls](git-rules-calls.md#hardened-git-calls)). It gets no `-c` option and no `GIT_CONFIG_COUNT`,
`GIT_CONFIG_KEY_<n>`, `GIT_CONFIG_VALUE_<n>` or `GIT_CONFIG_GLOBAL` of the daemon.
claustrum matches these names by their exact upper-case letters, so on Windows a
lower-case name stays on the call (from the code). The call drops the daemon's
`GIT_CONFIG_SYSTEM` and keeps `GIT_CONFIG_NOSYSTEM` (claustrum's own, not
measured). It does not give the call the daemon's own `LC_ALL` and `LANGUAGE` back
(claustrum's own, not measured).

### Where git cannot start

On Linux and macOS git cannot start in a folder that is a file, or that the
daemon cannot search (mode 0600 or 0000). The listing fails at the chdir, the
daemon runs `git version`, and the method goes on:

- If `git version` works, a regular file gets the "no repository" frames of
  `git.info`, `git.list_branches`, `git.status` and `git.worktree_create`, also
  with an empty `GIT_CONFIG_KEY_0`. A folder of mode 0000 gets the same frames for
  the last three methods, as a plain folder and as a repository (measured on
  Linux).
- If `git version` fails too, the method answers the "cannot run" text. That holds
  for `git.info` on a regular file and on a folder of mode 0000, and for the other
  three on a regular file (the create in `error`, with and without `worktreeRoot`).
  It holds for those three on a folder of mode 0000 too, as a plain folder and as a
  repository (measured on Linux).
- A path that does not exist keeps its frames.
- On a folder of mode 0000 `git.worktree_remove` without `worktreeRoot` answers
  `failed to remove worktree: open <baseRepo>: permission denied` (measured on
  Linux).

On Windows a `git version` that fails gives the "cannot run" text too, for
`git.info`, `git.list_branches`, `git.status` and `git.worktree_create` on a plain
folder, and for `git.info` on a repository and on a regular file. `git.info` on a
path that does not exist keeps its frame.

On Windows git cannot start in some paths that exist, and the listing fails by
class 3 with the start error `fork/exec <git.exe path>: The directory name is
invalid.`. `<git.exe path>` is the git that `PATH` resolves. The hooks refusal
with that error answers these requests in the "refused" frame of
[Trust refusal texts and frames](#trust-refusal-texts-and-frames),
and nothing changes on disk:

- `git.info` with `path` a regular file in a plain folder or in a repository, a
  file symlink, `NUL`, the name of a regular file with one more dot at its end, or
  a folder with a full path of 296 characters
- `git.list_branches` with `path` a regular file
- `git.status` with `baseRepo` a regular file, and `path` that file or a
  repository
- `git.worktree_create` with `baseRepo` a regular file

A `git version` that exits 1 and writes `boom` turns the `git.info` answer on a
regular file into `{"error":{"code":-32603,"message":"git cannot run on this host;
git not run: exit status 1: boom"}}`.

Other Windows frames stay as they are:

- `git.status` with the file as `path` and a repository as `baseRepo` answers
  `{"isRepo":false,"clean":false}`.
- `git.worktree_remove` with a regular-file `baseRepo` answers `failed to remove
  worktree: open <baseRepo>: not a directory`, also with an empty
  `GIT_CONFIG_KEY_0`.
- `git.info` answers "no repository" for a path that does not exist, a path under
  a regular file and a junction whose target is gone.
- With `GIT_CONFIG_COUNT=x` the count refusal answers the regular file.

On Windows, if the listing does not start in a path that `stat` reads as not a
folder, claustrum refuses (from the code).

A Windows folder can have an access entry that denies read data and traverse to
the user of the daemon. It keeps the `git.info` frame of a plain folder. With a
daemon token that has no privilege, the daemon refuses there with the hooks
refusal and `exit status 128: fatal: unable to access 'NUL/config': Permission
denied`. A folder with no deny entry keeps its frame under that token.

### The "cannot run" and hooks refusal texts

The "cannot run" text is `git cannot run on this host; git not run: <exec error>:
<git text>`. It carries the exec error and the stderr of `git version`, not those
of the listing. `git.worktree_remove` with `worktreeRoot` gives it too (measured
on Linux and macOS).

The hooks refusal is `config-defined hooks could not be pinned off; git not run:
listing the configuration in force: <exec error>: <git text>`.

claustrum makes `<git text>` of both texts with the text rule of
`git.worktree_create`. The rule takes git's raw stderr and keeps its first 512
bytes. It drops invalid UTF-8, turns each non-printable rune into one space, and
then trims spaces. If nothing is left, the text is the error of the git process,
for example `exit status 128`. The cap comes before the trim, so leading white
space counts toward the 512 bytes. A tab becomes one space. For the hooks refusal
this rule is measured on Windows. For the "cannot run" text it is claustrum's own.

With the `GIT_COMMON_DIR` pin, git names a corrupt config by its absolute path
(measured on Linux and macOS). On claustrum a syntax error in a config file of the
repository gets the class 1 text first: see
[Repository config refusals](git-rules-config.md#repository-config-refusals).

## A FIFO where the daemon reads a git file

On Linux and macOS the daemon opens three files of its own reads without
blocking: a `.git` file, the `commondir` file of a worktree entry and the `gitdir`
record of a worktree entry. It reads a regular file only. Anything else there, a
FIFO for example, is no usable file, and the read does not wait. Each rule below is measured on Linux and macOS, with a
git that walks past a FIFO `.git`:

- A `.git` that is a FIFO, a socket (measured on Linux only) or a symlink to a FIFO
  or to `/dev/null` is skipped by the walk. For a FIFO, `git.info`,
  `git.list_branches`, a create and a remove then answer for the repository above
  it. That remove deletes the folder, the entry and the branch. For a socket and a
  symlink to `/dev/null`, `git.info` and a create do. For a symlink to a FIFO, a
  create does. A create there succeeds. `git.status` with that folder as `path`
  and the repository above as `baseRepo` answers
  `{"isRepo":false,"clean":false}`. With no repository above, the answer is "no
  repository", and a create makes nothing.
- A `commondir` FIFO in the entry of a linked `baseRepo` gives M4 with
  `commondir is not a regular file`.
- A `commondir` FIFO in the entry of the worktree that a remove names: without
  `worktreeRoot` the remove succeeds and deletes the folder, the entry and the
  branch. A locked worktree there gets the locked refusal, and nothing is deleted.
  With `worktreeRoot` the remove gives no answer in 40 s, and nothing is deleted. It
  waits in `git worktree list --porcelain -z`.
- A remove of a worktree whose entry has a `commondir` regular file of 2 MiB
  succeeds and deletes the folder, the entry and the branch (measured on Linux and
  macOS).
- A `gitdir` record FIFO of the entry of the worktree: the remove without
  `worktreeRoot` succeeds, the folder and the branch go, and the entry stays. A
  locked worktree there gets the same success, and the entry and its `locked` file
  stay.
- A `gitdir` record FIFO of another entry: a remove of a worktree succeeds and
  deletes its folder, its own entry and its branch, and that other entry stays. A
  create in that repository waits in git's own `git worktree add`. It also waits
  with `timeoutMs` 3000 (measured on Linux only).

The [time limit](git-rules-calls.md#time-limits-of-git-calls) of the add ends that create (from the
code). A device file or a socket as a `commondir` file or a `gitdir` record is no
usable file (from the code). claustrum reads nothing from a FIFO that a writer
opens during the request (from the code). After `git worktree add`, a `commondir`
of the git directory of `baseRepo` that is not a regular file counts as none, so
that git directory is its own common directory (from the code). A FIFO as the
config is in
[Repository config refusals](git-rules-config.md#repository-config-refusals).
