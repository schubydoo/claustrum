# Config refusals of the git methods

In the state `shadow`, the daemon reads the git config of a repository itself. This page gives the refusals that follow.

An untagged rule on this page follows build `5fd08069`, measured on Linux.

## Repository config refusals

The daemon reads the git config of a repository itself, and it tests some files of
the git directory. If it does not accept one of them, the git method answers with a
refusal. A mark `(also <system>)`
means the rule was measured there too. A rule that names its systems
was measured there. Most rules were measured on
`git.info` and `git.status`.

The read runs only in the state `shadow` of [the settings copy](git-rules-settings-copy.md#the-settings-copy).
In the state `fallback-git` the daemon makes no read and gives no refusal. Git 2.13.7 is the
oldest git that ran with the refusal. The daemon
follows each `include.path` setting itself, and for a linked worktree it reads the
config of the repository of that worktree. A refusal is not kept (also macOS and
Windows), and an accepted config is read once and again after one of its files
changes (the change test and the bound of 256 git directories are claustrum's own,
see [claustrum's own choices](#claustrums-own-choices)). With a config that is a link
to a pty slave, the daemon stays up after the pty master closes.

A refusal is `<class>: <detail>`, shown as `<text>` in these frames.

| Method | Answer |
|---|---|
| `git.info`, `git.status`, `git.list_branches` | `{"jsonrpc":"2.0","id":N,"error":{"code":-32603,"message":"<text>"}}` |
| `git.worktree_create` | `{"result":{"success":false,"error":"<text>","errorCode":"worktree_add_failed"}}`. Nothing is made |
| `git.worktree_remove` | `{"result":{"success":false,"error":"failed to remove worktree: could not check whether <worktreePath> is locked: <text>"}}`. The worktree stays, and no branch is deleted |
| `git.worktree_remove` with `worktreeRoot` | `{"result":{"success":false,"error":"failed to remove worktree: cannot determine the repository's work tree: <text>"}}`. Nothing is removed |

On `git.worktree_remove` the refusal also comes for a worktree folder that does not
exist, and the branch `branchName` stays (also macOS and Windows). That holds in a
bare repository too (also Windows). With `worktreeRoot` the refusal comes for a
worktree that exists and for a missing one (also macOS).

### Paths in the texts

A path in quotes uses the separators of the system, and Go's `%q` quotes it. A path
of more than 300 characters is cut to its first 300 characters, with no mark of the
cut. On Windows each backslash is doubled inside the quotes, and the JSON encoding
doubles it again. The path of the `open <file>:` text is not in quotes, and it is not
cut. The `<worktreePath>` of the remove text is as sent, and it is not cut.

On Linux and macOS the path of the config is the resolved path, so a request through
a symbolic link gets the real path. On Windows a request through a junction keeps the
spelling of the junction, and a request in another letter case gets the real spelling.

The path of an included file is the folder of the file that holds the setting, one
separator, and the value as written. The Windows `is outside` text is the exception:
it names the cleaned path. On Windows `path = <dir>/inc.cfg` gives
`<git dir>\<dir>/inc.cfg`. The folder of the holding file stays as written too: with
`include.path = a/../b/inc.cfg`, a `path = x.cfg` in that file names
`<git dir>/a/../b/x.cfg` (also macOS).

### Turning the read off

`"shadowDirOff": true` in the request turns the read off for that request, on the
five methods (also macOS and Windows). A git request that starts while it runs has the
switch too. The request then gets the answer that it gets with no read. A FIFO as the
config then gets no answer in 10 s (also macOS). The member name matches without
regard to letter case, so `"SHADOWDIROFF": true` is the switch too (also macOS and Windows). The values
`"true"`, `1`, `null` and `false` act as no member (also macOS and Windows).

### Class 1: the git config

Class 1 is `this repository's git config cannot be used: <detail>`. The format
version and the extensions of an included file are not tested.

| Detail | When |
|---|---|
| `the repository uses format version "<v>", which this version of Claude does not support` | The last value of `core.repositoryformatversion`, as git lists it, is not exactly the text `0` or `1`. A config with no such key is not refused. `<v>` is cut to its first 20 characters, with no mark (also macOS and Windows) |
| `the repository uses git extension "<name>", or a value of it, that this version of Claude does not support` | The format version is `1`, and an `extensions.<name>` setting is refused (see "Extensions" below). `<name>` is in lower case. It is cut to its first 60 characters, with no mark (also macOS and Windows) |
| `"<file>" includes "<value>", and Claude expands neither ~user nor %(prefix), nor ~ where no home directory is set; write that path out in full` | An include value of the form `~user/…` or `%(prefix)/…`, in an `include` section or in an `includeIf` section. The condition of the `includeIf` section does not have to match. `<file>` is the file that holds the setting. `<value>` is cut to its first 100 characters (also macOS) |
| `its includes go past what Claude follows (16 files, nested 3 deep)` | More than 16 included files, or an include chain of more than 3 levels below the config. A missing file counts, and so does the file of an `includeIf` section, also a missing one. A file that includes itself gets this text too |
| `"<file>" is not a regular file` | The config or an included file is a FIFO or a directory. A config that is a symbolic link to the null device gets it, and so does an included file that is a symbolic link to a FIFO. `include.path = ~` gets it with the path of the home folder. So does a `config.worktree` of the git directory with `extensions.worktreeConfig` on (also macOS). The daemon's own read does not wait on a FIFO |
| `"<file>" is larger than 8 MiB` | The config, an included file or that `config.worktree` has more than 8388608 bytes |
| `"<file>" has a setting in section "<section>" whose name is longer than 1024 bytes` | The full name of a setting, with its section and subsection, has more than 1024 bytes. The sections are `core`, `remote`, `lfs`, `url`, `filter`, `extensions`, `include`, `includeif`, `index`, `feature` and `checkout` (the last three also macOS and Windows). `<section>` is in lower case, without the subsection. The bound holds in an included file, with the path of that file |
| `"<file>" has more settings than Claude reads (8 MiB); remove repeated ones` | See "The count of settings" below |
| `open <file>: <reason>` | The daemon cannot open the config, an included file or that `config.worktree`. A file of mode 000 gives `permission denied`. A socket gives `no such device or address` on Linux. As the config, a socket gives `operation not supported on socket` on macOS |
| `<error lines of git>` | The listing of the config exits non-zero while a child holds its stderr. The lines read until then are joined by one space (also macOS and Windows) |
| `fatal: bad config line <n> in "<file>"` | The config or an included file has a syntax error. So does the `config.worktree` file of the git directory with `extensions.worktreeConfig` on. This text comes before an extension refusal. The text is that of git, with the path in quotes |
| `"<included file>" is outside "<repo>", and on Windows Claude reads a repository's settings only from inside it; if the line that includes it is yours and you trust the file, put that line into ~/.gitconfig on the remote machine instead` | Windows: a relative include that leaves the repository. For a bare repository `<repo>` is its git directory |
| `"<file>" includes "<value>", and on Windows Claude follows an include only if its path is relative and stays inside the repository; if that line is yours and you trust the file, put the line into ~/.gitconfig on the remote machine instead` | Windows: an absolute include value, also to a missing file, `~/x` and `~root/x`. An absolute value and `~/x` get it also in an `includeIf` section whose condition does not match. `<value>` is cut to its first 300 characters |
| `"<file>": is, or is reached through, a link that Claude does not follow here; replace the link with what it points to` | Windows: the config is a file link. Also Windows, on every git method that reads the config: the git directory is a junction or a directory symbolic link, and `<file>` is `<git dir>\config` |

No refusal comes from a config that is missing, empty, a dangling symbolic link or a
symbolic link to a regular file. A NUL byte in a value, an empty `include.path` and
one with no value give none either. On Linux the daemon follows a relative include,
also one that leaves the git directory. It also follows an absolute include, and
`~/x` with a home folder.

Two more kinds of file get the kind test and the size test, with their own path in
the text. A format version `2` in such a file is not refused.

- The file of an `includeIf` section, whatever its condition is (also macOS and
  Windows). Its filter settings count for the filter rule of the checkout.
- The `config.worktree` file of the linked worktree that the trust check of the
  request finds. On `git.status` it is the worktree that `baseRepo` names. If the
  config turns `extensions.worktreeConfig` on, the file gets the tests. `true`, `yes`,
  `on` and `1` turn it on, and so do `TRUE`, `ON`, `Yes`, `On`, `tRuE` and the key
  with no value. `false`, `no` and `0` do not (also macOS and Windows, with
  `git.worktree_remove` on Linux only).

### Extensions

Under format version `1` the daemon refuses an extension other than
`worktreeConfig` in two cases. [The copied settings file](git-rules-settings-copy.md#the-copied-settings-file)
has no rule for its key, or the rule does not accept its value. Refused are
`preciousObjects = 1234567890`, `objectFormat = sha512`, `refStorage` with no value and
`partialClone = a/-b`. Not refused are `worktreeConfig = maybe`, `objectFormat = SHA256`, `refStorage = FILES`,
`preciousObjects = yes` and `preciousObjects = 1` (also macOS and Windows). An
extension in a config with no format version key is not refused. Under version `1`
the rules accept `objectFormat = sha256` and `refStorage = reftable`, so a sha256 or
a reftable repository is not refused. A value that git itself refuses but the copy
leaves out, such as `extensions.objectformat = md5` under version `0`, gets no
refusal of the daemon. The method answers as git does. With `objectFormat = SHA256`
under version `1`, `git.status` of `5fd08069` answers `exit status 128`.

### The count of settings

Each setting of a counted section costs the bytes of its full name, the bytes of its
value, and 96. The config has its own sum, and the included files have one sum
between them. The file in which a sum passes 8388608 is refused. The counted sections
are `core`, `remote`, `extensions`, `include`, `branch`, `lfs`, `index`, `url`,
`filter`, `feature`, `checkout` and `includeif`. The sections `x` and `user` do not
count. This arithmetic is fitted to the measured bounds (also macOS and Windows).

### Class 2: files of the git directory

Class 2 is `a file in this repository's .git directory cannot be used: <detail>`.
The files are `shallow`, `info/attributes` and `info/exclude`, and the git directory
itself. The daemon tests them in each request.

| Detail | When |
|---|---|
| `"<file>" is not a regular file` | `info/attributes` or `info/exclude` is a FIFO or a directory. A symbolic link there passes, also one to the null device. `shallow` is a FIFO, a directory or a symbolic link to the null device. The daemon does not wait on a FIFO |
| `"<file>": it is larger than 4 MiB` | `info/attributes` or `info/exclude` has more than 4194304 bytes |
| `"<file>" is larger than 4 MiB` | `shallow` has more than 4194304 bytes |
| `open <git dir>: not a directory` | Windows: a daemon `GIT_DIR=.git` names the `.git` file of a linked worktree, on the five methods |
| `open <file>: <reason>` | `shallow` has mode 000 (`permission denied`) or is a socket (`no such device or address`). The path is not in quotes. An `info/attributes` or `info/exclude` of mode 000 is not refused |
| `"<file>" holds something other than object ids` | A line of `shallow` is not 40 or 64 hex characters in lower case. That rule is fitted to the measured states. A blank line, an id with a space after it and a file of one line end are refused. A line end of CRLF, a last line with no newline and an empty file pass |

### Which answer comes first

A refused config comes first. A refused `config.worktree` file of a linked worktree
comes next (from the code), and then a refused file of the git directory. In class 2, `shallow` comes before `info/attributes` (also macOS and Windows),
and `info/attributes` comes before `info/exclude`. From the code: a refusal of the
trust check or of the daemon's own `GIT_CONFIG_COUNT` comes before them all, and on
`git.worktree_remove` with `worktreeRoot` the read comes before the configuration
listing.

### A folder that is not a repository

A folder with no repository keeps its "not a repository" answers, and the daemon reads
no config there. That holds for a `HEAD` that is no ref and no object id, also with
format version `2`. It also holds for a repository of another owner with no
`safe.directory` (also macOS and Windows). Such an answer is not kept, so a new
`safe.directory` line counts at the next request (also macOS). After a change of the
owner inside the 5 seconds of a kept good answer, a request answers normally. The
first request after that time answers "not a repository".
[The call about the repository](git-rules-settings-copy.md#the-call-about-the-repository) decides that state
and how long an answer is kept.

### claustrum's own choices

These rules are from the code and are not measured on `5fd08069`:

- With a daemon `GIT_COMMON_DIR`, `git.info` and `git.list_branches` read nothing.
- `extensions.worktreeConfig` starts the `config.worktree` test with values that no
  run had, such as `oN` or `007`. The last such setting of the config counts.
- Under format version `1` the value rule refuses extension values that no run had,
  such as `preciousObjects = -1`.
- The cuts of the format version value and of the extension name count characters.
- The `config.worktree` open error and size bound hold on `git.status`,
  `git.list_branches` and `git.worktree_remove` too, and every kind on Windows.
- The name bound and the count of settings are tested on a whole file before an
  include of that file is followed. So 200000 include lines of a missing file get the
  "more settings" text, as measured. The two include limits come before the open of an included
  file.
- A read with a failed open or read of the daemon is not kept, also when it accepts
  the config. So the next request reads again.
- The change test misses a change that keeps the size, modification time, mode and
  identity, where the system gives one, of each file of the read.
- An open error of the file of an `includeIf` section gives no refusal. An open
  error other than the measured ones gets the same `open <file>: <reason>` form.
- On Linux and macOS a `~` include takes the value of `HOME` in place of the `~`,
  when `HOME` is set and not relative (claustrum's own, not measured). An empty
  `HOME` counts as set, so `~/x.inc` becomes `/x.inc`. An unset or relative `HOME`
  gets the "expands neither" refusal. On Windows a `%(prefix)/…` value gets the
  "expands neither" text, with the cut at 100 characters.
- The listing that the read takes from git for one config file has a bound of 64 MiB.
  The read tests the name bound and the count on the first 64 MiB of a longer
  listing. If neither refuses, the read gives no verdict.
- If the read gives no verdict, the method goes on as with no read. A failed git call
  gives none, except for a syntax error, the error lines above and the stop at 30
  seconds ([Time limits of git calls](git-rules-calls.md#time-limits-of-git-calls)). So a git that does
  not start gives none, and so does a failed call with no holder of stderr. A config
  file that the daemon opens and cannot read gives none too.
- A bare repository whose config the daemon refuses, on a host where git refuses bare
  repositories (`safe.bareRepository = explicit`), gets the refusal.
- An `includeIf` line in a file three levels below the config gets the "includes go
  past" text.
- On Windows the daemon refuses a link at a config file and at an included file, and a
  junction or a directory symbolic link at the git directory itself. It tests no other
  folder above the file. It also refuses an include value that starts with a slash or
  a drive.
