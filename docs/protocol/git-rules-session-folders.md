# Session folders

The git methods and a bare `git` spawn skip a git on PATH in a session folder: a folder
that a Claude Code session works in. The request can turn this check off. Each rule on this page is of build `b3081b51`, and it names its systems.

## Which spawn makes a session folder

A `process.spawn` of a Claude Code shape with a `cwd` makes the `cwd` a session folder
(measured on Linux, macOS and Windows). Two shapes count:

- The CLI path: a command in a folder named `ccd-cli`, such as
  `<dir>/ccd-cli/2.0.0`. Its `--add-dir` folders before a `--` arg count too
  (measured on Linux and Windows). `--add-dir=<d>` names one folder. `--add-dir a b`
  names each arg up to the next arg that starts with `-`, and an empty arg is skipped
  (measured on Linux). A command named `ccd-cli` counts as the CLI path too (measured on
  Linux, macOS and Windows), with its `--add-dir` folders (measured on Linux and
  macOS). On Windows `ccd-cli.exe` makes none (measured). On Linux and macOS a spawn
  with a `launcher` counts too (measured). Windows refuses a `launcher` with `-32004`
  (measured).
- Args with `stream-json`. Their `--add-dir` folders do not count (measured on Linux
  and macOS). `--input-format=stream-json` and `--output-format=stream-json` count too
  (claustrum's own, not measured). A bare `git` with `stream-json` makes none, and a
  git by its full path with it makes one (measured on Linux, macOS and Windows).

### Other spawns

A spawn of `sleep` makes none (measured on Linux and macOS). A spawn with the [off
switch](#the-off-switch) makes none (measured on Windows), and another spawn makes one
also while such a request runs (measured on Linux). claustrum's own, not
measured: a spawn with no `cwd` makes none, a relative `--add-dir` folder is taken in
the `cwd`, a `launcher` spawn with `stream-json` counts its `--add-dir` folders, the
name `ccd-cli` matches in its exact letter case, and the record comes after the `cwd` and command tests and before the start.

### A create

A good `git.worktree_create` of a plain repository makes `<repo>/.git` a session
folder (measured on Linux, macOS and Windows). Its record stays after
`git.worktree_remove` (measured on Linux, macOS and Windows). A create runs no prune,
so 2048 records become 2049 (`b3081b51`, measured on Linux). A record that cannot be
saved does not fail the create, and the daemon logs the line of the failure (measured
on Linux and macOS). `git.info`, `git.status` and `git.list_branches` make no record
(`b3081b51`, measured on Linux). For a `baseRepo` that is a linked worktree, the folder
is the git directory of its main repository (claustrum's own, not measured). A failed
create and a create with the off switch make none (claustrum's own, not measured).
The record is written at the good reply, and on Linux and macOS its path has its links
resolved (claustrum's own, not measured). A create whose trust check names no git
directory records the folder that git names (claustrum's own, not measured).

## The record

The daemon keeps the folders in memory and in `<root>/session-folders`, for the socket
`<root>/run/<client>/rpc.sock`. It makes that folder at its start (measured on Linux,
macOS and Windows), with mode 0700 (measured on Linux and macOS).

- One file per folder. Its name is the first 32 hex digits of the SHA-256 of the
  folder path (measured on Linux and macOS). Its body is `<ms since epoch> <dev>
  <inode>`, a newline and the path, with no newline at the end (measured on Linux,
  macOS and Windows). `<dev> <inode>` is `0 0` on Windows. Its mode is 0600 (measured
  on Linux and macOS).
- A new daemon reads the files at its start, so the folders of an earlier daemon count
  (measured on Linux, macOS and Windows). Other files in the folder stay.
- A folder that has a record gets no new file (claustrum's own, not measured).

### A write and the prune

- A file goes to a temp file `.new-<pid>-<ns>` first, and then by one rename to its
  name. A folder at that name fails the write. The folder stays, the temp file goes,
  the spawn succeeds, and the daemon logs the first line below (measured on Linux,
  macOS and Windows). Each later spawn in that folder tries the write again and logs
  the line again (claustrum's own, not measured).
- After a spawn the folder keeps at most 2048 record files. After a good write the
  records with the lowest ms field go, each by one plain remove of its name. The file time does not
  count, and a gone folder does not make its record go first (measured on Linux, macOS
  and Windows).

With a socket of another layout the daemon writes no file (measured on Linux, macOS
and Windows). It logs the second line (measured on Linux and macOS).

```text
[sessiondirs] cannot write to the record of session folders in "<dir>": renameat .new-<pid>-<ns> <name>: file exists; they are kept in memory until this helper ends, and written at a later start
[daemon] socket "<socket>" is not <root>/run/<client>/rpc.sock: session folders are kept in memory only, so the next helper will not know them
```

### A record that cannot be saved

- A record that cannot be made or written fails the spawn when the first entry of the
  daemon PATH lies in that folder (`b3081b51`, measured on Linux). The spawn then gets
  `-32603` with the reason `other`, and no child starts (measured on Linux, macOS and
  Windows). That holds for a file at the folder name and a folder that denies the
  write. Linux and macOS also measured a full disk. The daemon logs the line of the
  failure and then the failed-start line.
- The first entry holds a git or is empty (measured on Linux). With no entry in the
  folder the spawn starts, also with a git or a repository in the folder (measured on
  Linux, and on macOS for an empty folder). claustrum's own, not measured: a later
  entry counts too, and an entry that is a link into the folder or names it in other
  letter case on macOS does not count.
- The folder stays in memory, so `git.status` and `git.info` still pass over a git in
  it (measured on Linux and macOS). The next spawn tries the write again. After the
  file at the folder name goes, the daemon makes the folder and writes the record
  (measured on Linux and macOS).

## A git in a session folder

The git calls of the git methods pass over a git in a session folder. So does a
`process.spawn` of the bare command `git`. Only that exact name counts: on Windows
`git.exe`, `GIT` and `Git.EXE` run the git in the session folder and keep their env
(measured on Windows). A PATH entry with a link to such a folder
counts too. The next git on the PATH of the daemon runs, and the daemon logs the first
line below (measured on Linux, macOS and Windows). claustrum logs it once for each
path (claustrum's own, not measured). On macOS a PATH entry that names a session folder in other letter
case is passed over too (measured on macOS). On Windows the paths match in any letter
case (measured on Windows).

```text
[sessiondirs] passing over "<path>": it lies in, or is reached through, a folder that Claude works in. Each file in "<dir>" ends in the path of one such folder; remove the file to have that folder forgotten
[sessiondirs] WARNING: starting "<name>" from "<path>", which is in or reached through a folder that a Claude Code session can write. …
```

Another bare command that PATH finds in a session folder runs, with the second line
(measured on Linux and macOS). Windows logs no such line (measured).

### The variables of a git child

These variables of a git child lose the session folders by their text. That holds for
a bare `git` spawn (measured on Linux, macOS and Windows) and for the calls of
`git.status` and `git.info` (measured on macOS and Windows):

- `PATH`, also the PATH of the spawn env (measured on Linux and macOS), and
  `GIT_EXEC_PATH` lose each such entry. So do `LD_LIBRARY_PATH` (measured on Linux)
  and `DYLD_LIBRARY_PATH`, `DYLD_FRAMEWORK_PATH` and `DYLD_INSERT_LIBRARIES` (measured
  on macOS). On macOS a PATH entry that names a session folder in other letter case
  goes too (measured on macOS). A list with no entry left goes. A link to
  such a folder stays in PATH (measured on Windows).
- A value of `GIT_SSH`, `GIT_ASKPASS` or `SSH_ASKPASS` in one of them goes.
- `PYTHONPATH`, `PERL5LIB`, `MANPATH`, `PATHEXT` and other names stay.

Eight names of the daemon environment go too: `PAGER`, `EDITOR`, `VISUAL`,
`GIT_PAGER`, `GIT_EDITOR`, `GIT_SEQUENCE_EDITOR`, `GIT_EXTERNAL_DIFF` and
`GIT_PROXY_COMMAND`. That holds for a bare `git` spawn and each call of `git.status`
and `git.info`, also when no session folder is known (measured on Linux, macOS and
Windows). A name in the `env` of the spawn stays. A spawned bare `git` also gets
`GIT_TRACE2=0`, `GIT_TRACE2_EVENT=0` and `GIT_TRACE2_PERF=0` (measured on Linux,
macOS and Windows), and none with the off switch (measured on Linux and macOS). A spawn
of `git` by a path keeps them (measured on Linux). The other git methods lose them too
(claustrum's own, not measured).

### With no other git

`git.status` answers (measured on Linux, macOS and Windows):

```json
{"jsonrpc":"2.0","id":2,"error":{"code":-32603,"message":"git cannot run on this host; git not run: For security, Claude won't use a git inside a folder it works in, and this host has no other git on its PATH. Install git somewhere else, such as /usr/bin"}}
```

A spawn of `git` answers:

```json
{"jsonrpc":"2.0","id":3,"error":{"code":-32603,"message":"For security, Claude won't use a git inside a folder it works in, and this host has no other git on its PATH. Install git somewhere else, such as /usr/bin","data":{"reason":"other"}}}
```

On Windows the text ends `such as C:\\Program Files\\Git` in both frames.

## The off switch

`"sessionFoldersOff":true` on a git method or on `process.spawn` turns the check off
while the request runs. The git in the session folder then runs (measured on Linux,
macOS and Windows). The session folders
stay in the variables of the git child (measured on macOS and Windows for `git.status`
and `process.spawn`, on Linux for `process.spawn`). The eight names still go
(claustrum's own, not measured). The first such request logs the first line below, and later ones log
nothing. The first check after the last such request logs the second line. The hold is
global: a request without the switch that runs meanwhile gets no check either
(measured on Linux).

```text
[sessiondirs] the check of session folders is OFF: the app sends its off switch. While a request that carries it runs, git is found by its bare name and its search lists are cut by text alone
[sessiondirs] the check of session folders is on again: no request that carries the app's off switch holds it any more
```

A `git.info` with the switch and no `"shadowDirOff":true` answers a repository with no
`settingsControl` member on Windows and macOS, and with it on Linux (measured).
