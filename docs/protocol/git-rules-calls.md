# Hardened git calls and time limits

The git methods run most git steps as hardened calls. This page gives those calls, their environment, their time limits and the branch step.

An untagged rule on this page matches build `5fd08069` or an earlier build. A section that names its systems or its build keeps that scope.

## Hardened git calls

The git methods run most git steps as hardened calls. A hardened call is a git call
with a fixed list of `-c` options and a fixed set of environment variables. The lists
and the variables follow `5fd08069`. The hook pins were measured against `f6010b97`,
and their limits against `89cb6289`. A rule that names no system was measured on
Linux, macOS and Windows. `githarden.go` holds the lists, and
`githarden_shape_test.go` tests them.

Each hardened call passes no `-C`. Before each one, the daemon runs a config
listing (`git config -z --list`) with the profile of that call. Before its first
listing, a request with no cached value runs the excludes read, `git config
--includes --path core.excludesFile`, in the temporary directory. It adds only
`GIT_DIR=<null>`. The probe `git --attr-source=<empty tree> version` adds no
variable. There are two profiles:

- The heavy profile forbids every transport protocol
  (`GIT_ALLOW_PROTOCOL=denied_by_claude_ssh`). `git.info`, `git.list_branches` and
  `git.status` use it.
- The light profile allows `https:ssh`. Most calls of `git.worktree_create` use it.
  In the states `shadow` and `fallback-git` without `"shadowDirOff":true`, the light
  calls of a create carry the heavy variables, except the checkout and its listings
  (measured on Linux and macOS in the state `shadow`, and on Windows in the state
  `fallback-git`).

[The daemon's own git environment](#the-daemons-own-git-environment) gives the
variables of the daemon that change these values. A variable that the daemon adds
wins over the same variable of the daemon's own environment (measured on Linux and
macOS). On Windows claustrum gives a git child its environment in name order. That
is [D23](../DIVERGENCES.md#d23).

A client sees the hardening in these places:

- `git.status` reads the real `HEAD` commit, also where `refs/replace` names another
  (see [git.status](git-status.md#what-the-status-reads)).
- The failure texts of `git.worktree_create` hold no graft-file `hint:` lines: the
  light list turns them off. No `git.status` reply holds one either, because its
  calls give only stdout to the reply.
- On Windows a create with a tree path of 270 characters succeeds.
- The records of a new worktree hold absolute paths, also where the user config sets
  `worktree.useRelativePaths` (measured on macOS and Windows).
- Each call passes the user's global excludes file as `core.excludesFile`. When
  the user has none, the value is the null device. On Windows the `status`,
  `ls-files` and `diff-index` calls of `git.status` pass `/dev/null` there. That is claustrum's
  own, see [D16](../DIVERGENCES.md#d16).
- The config listings run with `LC_ALL=C` and `LANGUAGE=C`. git then writes its
  listing errors in English (measured on Linux). The one exception is the second
  listing before the checkout of a create in the state `shadow`, which adds
  `LANGUAGE=C` alone. The call after a listing keeps the daemon's `LC_ALL`,
  `LANGUAGE` and `LANG`. The checkout in the state `shadow` is an exception too: it
  ends with `LANGUAGE=C`.
- On Linux and macOS, with `"shadowDirOff":true`, a create can answer the text
  `open <tmp>/claude-ssh-index-<n>/index: no such file or directory`. `<n>` is
  decimal digits.

### Refusal texts

The first config listing of a method is the hooks refusal check. It is not an extra
call. Each method answers these texts in its own error frame (see the
[Error-string catalogue](errors.md#error-string-catalogue)):

| Cause | Text |
|---|---|
| The listing fails | `config-defined hooks could not be pinned off; git not run: listing the configuration in force: <exec error>: <git text>`. When git wrote no text on standard error, the text ends after `<exec error>`. [Git-directory trust check](git-rules-trust.md#git-directory-trust-check) gives the classes of the failure |
| The listing fails, and `git version` fails too | `git cannot run on this host; git not run: <exec error>: <git text>`, with the detail of `git version` |
| A config key over 1048576 bytes | `config-defined hooks could not be pinned off; git not run: listing the configuration in force: a configuration key is longer than the listing reads` |
| More than 1024 hook names | `config-defined hooks could not be pinned off; git not run: too many configured hooks to pin` |
| Hook names over 65536 bytes in sum | `config-defined hooks could not be pinned off; git not run: configured hook names exceed the aggregate pin byte bound` |

A config value has no cap: 2 MiB passes. In some sections a key over 1024 bytes is
refused before the listing (see [Repository config refusals](git-rules-config.md#repository-config-refusals)).
The limits refuse at the first listing only. In a folder that holds no repository, the
first listing follows [The daemon's own git environment](#the-daemons-own-git-environment).
There a limit refuses each method except `git.status`. That rule is from the code.

### Hook pins

Each hardened call pins off the hooks that the listing right before it names (from
the code). A key
`hook.<name>.<var>` names a hook. The name is the text between the first and the last
dot, and it can be empty or hold dots. `hook.<var>` names no hook, and a hook key
inside a value pins nothing. The call gets `hook.enabled=false` and an empty
`hook.event`, and for each name `hook.<name>.enabled=false` and an empty
`hook.<name>.event`. The pins go in through `GIT_CONFIG_COUNT` (see
[The daemon's own git environment](#the-daemons-own-git-environment)). Only
`command` and `event` were measured as `<var>`.

- A later listing that exceeds a limit does not refuse its call. The call then gets
  only `hook.enabled=false` and an empty `hook.event` (from the code).
- A later listing that fails refuses in three places:
    - the two later listings of `git.status` (see
      [git.status](git-status.md#rare-states)). For the listing in the git
      directory, this is measured on Linux. For the listing in the temporary
      folder, it is from the code.
    - the two listings before the checkout of a create in the state `shadow`
      (measured on Linux and macOS, see
      [git.worktree_create → Error texts](git-worktree-create.md#error-texts))
    - any listing that its time limit stopped (measured on Linux for the listing
      of the checkout)
- Two names that differ only in letter case get two pins. A name under two keys
  counts once toward the limits. The count is tested before the byte sum. These
  three rules are from the code.

## The daemon's own git environment

This section names the variables of the daemon's own environment that reach the
calls of the git methods. It also gives the refusals of that environment. A rule
here holds on Linux, macOS and Windows, unless it names fewer systems.

### Variables that the git calls keep or drop

The repository calls are the configuration listings, the hardened calls,
`worktree add`, the checkout and `git status`. The excludes read and the
`--attr-source` probe get the daemon's environment as it is, plus their own
additions.

- The daemon's environment is cleaned for the repository calls and the calls of
  the start test, entry by entry, in this order (build
  `c8e2f083`, not measured). The calls of the start test also get no `GIT_`
  variable of the daemon.
    - An entry with no `=` and the four `GIT_*_PATHSPECS` variables go. On
      Windows a name matches in any letter case and keeps its spelling.
    - A relative value drops 23 names, such as `HOME` and `GIT_INDEX_FILE`. On
      Windows `/dev/null` keeps `GIT_CONFIG_GLOBAL` and `GIT_CONFIG_SYSTEM`. An
      empty value is not relative. On Windows `NUL` in any case is not.
    - The Kerberos names `KRB5CCNAME`, `KRB5_KTNAME` and `KRB5_CLIENT_KTNAME` go
      when their file part is relative. `KEYRING`, `KCM`, `API`, `MEMORY`,
      `XCACHE` and `XCTEMP` have none.
    - On Windows an empty `HOME`, `USERPROFILE`, `PROGRAMDATA` or
      `ALLUSERSPROFILE` goes. So does a `HOMEDRIVE` or `HOMEPATH` of the wrong
      shape. A relative `SSH_AUTH_SOCK` goes, but not one that starts with a slash
      on Windows.
    - 19 list names, such as `PATH`, `LOCPATH` and `SSL_CERT_DIR`, keep their
      absolute entries. Five of them keep an empty value. The rest go when no
      entry is left. On Windows a UNC path is absolute.
    - `LD_PRELOAD` and `LD_AUDIT` keep an absolute piece or one with no `/`.
      `LD_PRELOAD` splits on white space too.
    - Six program names, such as `GIT_SSH` and `SSH_ASKPASS`, go when they are a
      relative path. `SSH_SK_PROVIDER` also needs `internal` or an absolute path on
      macOS and Windows.
    - Every other entry stays.
- `GIT_CONFIG` and `GIT_CONFIG_PARAMETERS` do not reach a repository call. The
  names match exactly, in upper case. So on Windows `git_config` and
  `Git_Config_Parameters` reach git.
- A listing gets the daemon's `GIT_CONFIG_COUNT`, `GIT_CONFIG_KEY_<n>` and
  `GIT_CONFIG_VALUE_<n>` as they are. It keeps a name with a leading-zero index,
  such as `GIT_CONFIG_KEY_01`, in its place (measured on Linux and macOS).
- A hardened call gets `GIT_CONFIG_COUNT` first, with the number of all the pairs.
  The daemon's pairs `0` to `count-1` follow it, in index order. The
  [hook pins](#hook-pins) follow them, from the pair `count` on. The
  `GIT_INDEX_FILE` of the checkout and the `GIT_OPTIONAL_LOCKS` of the `status`,
  `ls-files` and `diff-index` calls follow the pins. The order is measured on Linux
  and macOS.
- A hardened call gets no other `GIT_CONFIG_KEY_<digits>` or
  `GIT_CONFIG_VALUE_<digits>`. A leading-zero index such as `GIT_CONFIG_KEY_01`
  is removed too. Other names, such as `GIT_CONFIGX` or `GIT_CONFIG_S4`, reach
  git.
- The daemon's `GIT_ALLOW_PROTOCOL` sets the light value. Unset, it is
  `https:ssh`. Else the daemon splits it on `:` and keeps each entry that is
  exactly `https` or `ssh`, once, in its order. The match is case-sensitive, with
  no trim. With no entry left, it is `denied_by_claude_ssh`. The heavy value stays
  `denied_by_claude_ssh`. On Windows a lower-case `git_allow_protocol` sets it
  too.

The daemon reads each value that it parses from the standard output of git (from
the code). The reference does so for the branch of `git.info` and the repository
test of a create (measured on Linux).

[Git-directory trust check](git-rules-trust.md#git-directory-trust-check) gives the `GIT_DIR` of the
listing in a folder with no repository, and its frames.

### The check of `GIT_CONFIG_COUNT`

The git methods check the daemon's `GIT_CONFIG_COUNT`, with no git call except
the excludes read. The check reads the count and its pairs by their exact
upper-case names. On Windows it does not see a lower-case `git_config_count` or
`git_config_key_0`.

- If the count is not set, there is no check.
- An empty value counts as `0`. Else the check skips leading space, tab, `\n`,
  `\v`, `\f` and `\r` bytes. One optional `+` and one or more digits follow, and
  then the end. So `\t1`, ` +1` and `01` count as `1`, and `00` counts as `0`.
  Other values are refused. Examples are `-1`, `1\n`, `+ 1`, `++1`, a lone tab and
  a no-break space before the digit. So are `0x1`, `x` and `99999999999999999999`.
  The text is `inherited GIT_CONFIG_COUNT "<value>" is not a count`, with the value
  in Go `%q` quotes.
- For each index from `0` to `count-1`, `GIT_CONFIG_KEY_<n>` and
  `GIT_CONFIG_VALUE_<n>` must both be set. `<n>` is the plain decimal index, so
  `GIT_CONFIG_KEY_01` is not pair `1`. An empty value counts as set. The first
  index where one is not set is refused with
  `inherited GIT_CONFIG pair <n> is incomplete`.
- An empty key is not refused here. git then fails the listing, also in a folder
  that holds no repository (see the next section).

`GIT_CONFIG_PARAMETERS='x` changes no frame in a plain folder.

### Configuration listings that git cannot parse

With a key that git cannot read, git fails each configuration listing with exit
status 128. `<text>` is then `config-defined hooks could not be pinned off; git not
run: listing the configuration in force: exit status 128: <git text>`. A good pair
before the broken pair changes nothing. `<git text>` comes from git, for example:

| Key | `<git text>` |
|---|---|
| empty | `error: empty config key fatal: unable to parse command-line config` |
| `nodot` | `error: key does not contain a section: nodot fatal: unable to parse command-line config` |
| `a.b c` | `error: invalid key: a.b c fatal: unable to parse command-line config` |

A `GIT_CONFIG_GLOBAL` that names a file git cannot parse fails each listing too.
With the content `[broken`, `<git text>` is `fatal: bad config line 1 in file
<file>` (measured on Linux and macOS). The `git version` call after a failed
listing gets no `GIT_CONFIG_GLOBAL` and passes, so the text stays the hooks
refusal (measured for `git.info` and `git.worktree_create`).

This refusal needs a folder where git starts. A path that does not exist answers
as it does with nothing set (measured on Linux and macOS, and for `git.info` on
Windows). On Linux and macOS a plain folder of mode 0000 answers so too. A plain
folder gets the refusal. Nothing changes on disk.

### Where each method answers the refusal

Both refusals start with `config-defined hooks could not be pinned off; git not
run: `, and each method answers them in one frame. A refusal of the git-directory
trust check comes first, with the trust text in the frame. A remove of a present
worktree gets the lock-check text instead. For the count, the trust-check order
is measured on Linux and macOS, in a remove only for a gone worktree without
`worktreeRoot` or a `baseRepo` that exists with it. On Windows it is measured in
`git.info`, `git.status` and the remove of a gone worktree. For a listing that
git cannot parse it is from the code. "The count" below is the check of
`GIT_CONFIG_COUNT`, and "the listing" is a listing that git cannot parse.

| Method | Frame | Where |
|---|---|---|
| `git.info` | `{"error":{"code":-32603,"message":<text>}}` | Also a plain folder. The listing also refuses a folder with an empty `.git` folder, and from the code the count does too. The count also refuses a missing folder and a file |
| `git.list_branches` | the same | Also a plain folder. The count also refuses a file and an empty or absent `path`, with a repository or a plain folder as the daemon's working directory. A `path` that is not empty and does not resolve, or that is inside a managed worktrees directory, gets `{"isRepo":false,"branches":[]}` before the count, with no git call |
| `git.status` | the same | Also a plain-folder `baseRepo`. The count also refuses a `path` that is a plain folder, the repository, missing or a file. A `baseRepo` that does not resolve gets `{"isRepo":false,"clean":false}` before the count, with no git call. See [git.status](git-status.md#baserepo). From the code: a `baseRepo` in a managed worktrees tree answers the same way. That case is not measured with a refused count |
| `git.worktree_create` | `{"success":false,"error":<text>,"errorCode":"worktree_add_failed"}`, and no folder is made | Also a plain-folder `baseRepo`. The listing refuses it without and with `worktreeRoot`, and from the code the count does too. Without the refusal, a create with `worktreeRoot` there on Windows answers `not_a_repo`. The count also refuses a missing `baseRepo`. It comes before the refusals of a relative `worktreePath`, a `..` component, a path outside the repository and a path that exists. Three frames come before it: the `-32602` frame of a missing `branchName`, the `nested_base_repo` frame of a `baseRepo` inside a managed worktrees directory, and on Windows the frame of claustrum's own trust-root test. On Windows the count also comes before the `worktreeRoot` refusal |
| `git.worktree_remove` | Without `worktreeRoot`, for a gone worktree: `{"success":false,"error":"failed to remove worktree: could not check whether <worktreePath> is locked (<text>); retry"}`. With `worktreeRoot`: `{"success":false,"error":"failed to remove worktree: cannot determine the repository's work tree: <text>"}` (measured on Linux and macOS) | A plain-folder `baseRepo`. The list below has the other cases |

The other cases of `git.worktree_remove`:

- These refusals come first, with their usual frames: a relative path, a path
  outside the repository, the repository itself and a `..` component. With
  `worktreeRoot` they are also a path outside the root, a `..` `baseRepo` and a
  root in `baseRepo` (measured on Linux and macOS). On Windows the `worktreeRoot`
  refusal comes first. For a listing that git cannot parse, that is from the
  code.
- On Windows a `baseRepo` that fails claustrum's own trust-root test gets the
  managed-worktrees refusal first.
- Without `worktreeRoot`, `open <baseRepo>: not a directory` comes first for a
  regular-file `baseRepo`. So does `statat .claude: permission denied` for a
  `baseRepo` with mode 0600 and a path under `.claude` (measured on Linux and
  macOS).
- Without `worktreeRoot`, the count also refuses a missing `baseRepo` in the
  gone-worktree frame. For a worktree directory that is present, the count puts
  `its registrations could not be examined` in place of `<text>`. On Linux and
  macOS a `baseRepo` of `<T>/missing/..`, which does not resolve, gets that frame
  too. On Windows that `baseRepo` gets the managed-worktrees refusal first.
- With `worktreeRoot`, the count also refuses a `baseRepo` that is a repository in
  the work-tree frame. A trust refusal puts its own text there instead. For a
  missing absolute `baseRepo` without a `..` component, the count answers in the
  gone-worktree frame. A worktree directory that is present then gets `the
  repository at <baseRepo> could not be read` in place of `<text>`. If the root is
  missing too, the "not reachable" refusal comes first. Each rule of this bullet
  is measured on Linux and macOS.

These states are not measured. The rules are from the code.

- A listing in a folder that holds no repository passes, but it exceeds a limit
  of the hook pins (see [Hardened git calls](#hardened-git-calls)).
  `git.info`, `git.list_branches` and `git.worktree_create` answer the limit
  refusal in their "refused" frames. `git.worktree_remove` answers it as `<text>`
  of its two frames in the table above. `git.status` answers
  `{"isRepo":false,"clean":false}`. It refuses in that folder only for a listing
  that fails.
- A `.git` file with no `gitdir:` line. Its listing carries `GIT_DIR=<null>`, so
  the folder answers as a plain folder does.

## Time limits of git calls

On `git.list_branches`, `git.info`, `git.status` and `git.worktree_create` most
calls end at 2 minutes. Config listings before a git call have a 30 second limit (the one before the
checkout: measured on Linux and macOS).
So do the repository config read (`git.status`, measured on Linux) and each
`config.worktree` listing (measured on Linux). A create with the filter rule and no
settings copy runs claustrum's own `--show-scope` listing, with the 2 minute
limit and the `timeoutMs`. A stopped call of the top folder pair stops the create
with the stop text (claustrum's own). The scan of ignored files ends at 10
minutes (measured on Linux and macOS). Read-tree checkouts have no such limit.

- The stop is SIGTERM, then SIGKILL 2 s later. On Windows the process ends at
  once. Config file listings get SIGKILL alone, not the listing before the
  read-tree call (measured on Linux and macOS).
- Before a good add no call follows a stopped call. A stopped add removes an
  empty leaf. A stopped repository test of a create answers the stop text alone,
  with `worktree_add_failed` (measured on Linux). After a good add the rollback runs its
  own calls (measured on Linux and macOS). A stopped scan copies nothing, and the create succeeds.
- While a `"shadowDirOff":true` request runs, no call of that request stops at
  2 minutes. No call of another request stops at 2 minutes then either (measured on Linux). claustrum's own limit is then 30 minutes.
- `timeoutMs` does not end the add. With `"shadowDirOff":true` it does not end
  the listing before the checkout either (measured on Linux).
- With no `timeoutMs` a reply waits 2 s after git exits for a child that holds
  its output and gets no signal. Listings before a call wait until stdout
  closes, also past the limit. The `status` scan waits too, within its limit
  (measured on Linux). Held past 30 s, the
  config read on standard input ends near 31 s (measured on Linux), and `config.worktree`
  reads too (not measured). A holder of stderr alone delays a config listing
  about 2 s and that read 1 to 1.5 s (measured on Linux and macOS).
- `git.worktree_remove` gets none of these limits. Its first call has the 30 s
  bound of the call about the repository (measured on Linux).

## The branch step

The branch step deletes a branch only when another local branch or a
remote-tracking ref reaches its tip. Else it keeps the branch.
`git.worktree_remove` runs it after the worktree directory and its registration
go. The `git.worktree_create` rollbacks run it on the branch that the call made.
`<b>` is `branchName` as sent. `<tip>` is the object name in the listed record
of `refs/heads/<b>`. A rule of this section with no other mark is measured
against `89cb6289` on Linux.

The step runs `for-each-ref`, `rev-parse --verify`, `rev-list` and
`update-ref`. Each runs in `baseRepo` after its own configuration listing. It
carries the light `-c` list and the six values of the heavy profile, in an order
of their own (see [Hardened git calls](#hardened-git-calls)) (measured on Linux, macOS and
Windows).

### Which branch the step deletes

- The step runs no git call for an empty `branchName`, or for one that starts
  with `-` or `+`. The branch stays.
- Another local branch at or past the tip reaches it, and so does a
  remote-tracking ref. Then the step deletes `refs/heads/<b>`. A tag, a detached
  HEAD, a symbolic ref and a branch at a parent commit do not reach it.
- A branch that is checked out in another worktree gets no guard of its own.
- If `branchName` names a symbolic ref, the step deletes the symbolic ref only.
- The delete passes the tip that the check saw as the old value. So git refuses
  the delete if the branch moved after the check.
- If the branch list is read and `rev-parse --verify` reports the branch absent,
  the step deletes nothing and keeps nothing (measured on Linux, macOS and Windows).
- The step keeps the branch in each of these cases (measured on Linux, macOS and Windows where not marked):
    - The branch holds a commit that no other ref reaches.
    - The branch list cannot be read, or its read is stopped (measured on Linux, and the
      stop on macOS and Windows). A configuration listing that answers
      `fatal: not a git repository` keeps the branch too (measured on Linux and macOS).
    - The repository has 10001 local branches or more. With 10000 the full check
      runs.
    - Another local branch has the same name in another letter case. claustrum
      folds case with Go's `strings.EqualFold`, also outside ASCII (claustrum's
      own).
    - The check of the commits fails or is stopped (measured on Linux).
    - The delete fails or is stopped.
    - A list that does not parse, and a branch that is not listed but that
      `rev-parse --verify` does not report as absent (claustrum's own).
    - In a create rollback after a failed `config.worktree` step, the daemon
      reads the config of the repository again. A refusal there keeps the
      branch (`5fd08069`, measured on Linux and macOS).

### Time limits of the branch step

These bounds are always on. They are not a divergence.

- One bound of 60 s covers the branch list and the commit check together. At the
  bound the running call is killed, with no SIGTERM first (measured on Linux, macOS and
  Windows). The bound starts before the configuration listing of
  `for-each-ref` and also covers `rev-parse` (claustrum's own). A stopped
  `rev-parse` is a failure and keeps the branch, also on Windows, where the kill
  gives exit code 1 (claustrum's own).
- `update-ref` gets SIGTERM 5 s after it starts, on Linux and macOS. A call that
  ignores it is killed 5 s later. On Windows it is killed at 5 s. That holds for
  a remove and for a create rollback (measured on Linux and macOS). The kill after
  SIGTERM in a rollback is claustrum's own.
- Only the git process gets the SIGTERM or the kill, not its children
  (claustrum's own).
- The `timeoutMs` of the caller does not stop the step.

### Two branch steps at once

- Within one daemon, the steps of one repository run one at a time, also when
  one goes through a linked worktree. If two removes have branches that share a
  unique commit, one branch stays, and the second reply has
  `"branchKept":true` (measured on Linux and macOS). claustrum keys this lock by the
  common git dir, also in a rollback (claustrum's own).
- The steps of different repositories run at the same time.
- claustrum runs two removes of one `baseRepo` one after the other (claustrum's
  own).
- A remove deletes its worktree directory while the `worktree add` of a create
  in the same git directory runs. Its registration, branch and reply wait for
  the add (`5fd08069`, measured on Linux and macOS). They do not wait for the checkout
  (measured on Linux).

### The branch step in a remove reply

The remove frame never gains text from this step. Each case of the keep list
that a remove can reach adds `"branchKept":true`. A skipped name adds no member.

### The branch step in an undo text

A create rollback that keeps the branch adds a part after `; and the undo could
not finish for <leaf>: `. If the branch holds a commit that no other ref
reaches, it also sets `"branchKept":true` after `errorCode`:

| outcome | part | member |
|---|---|---|
| the branch holds a commit that no other ref reaches | `the worktree itself was removed, but branch <b> was left in place: it now has commits no other branch or remote-tracking ref reaches. To keep that work, push the branch or rename it. To discard it, run git branch -D <b>. This branch name cannot be reused until the branch is renamed or deleted` (measured on Linux, macOS and Windows) | `"branchKept":true` |
| the same, after a failed rmdir of the leaf | `<rmdir text>; branch <b> was left in place: it now has commits …` with no `the worktree itself was removed, but ` (measured on Linux and Windows) | `"branchKept":true` |
| 10001 branches, a case twin, a list that does not parse, or a `rev-parse` that finds the branch (exit 0) | `branch <b> still exists; delete it by hand (for example: git branch -d <b>, which git refuses if that would lose commits) before retrying this branch name: could not check whether another branch or remote-tracking ref reaches its commits` (measured on Linux, macOS and Windows, but the list and `rev-parse` cases are claustrum's own) | none |
| a call of the check failed or was stopped | the same, then a space and the process error in parentheses, for example ` (exit status 128)` or ` (signal: killed)` (a failed `rev-parse` is claustrum's own) | none |
| after a failed `config.worktree` step, the config is refused at a second read | the same, with that refusal in parentheses (`5fd08069`, measured on Linux and macOS) | none |
| the delete failed or was stopped | `branch <b> still exists; delete it by hand (for example: git branch -D <b>) before retrying this branch name: <process error>`, for example `exit status 128` or `signal: terminated` (measured on Linux and macOS) | none |
| the same, with `refs/heads/<b>.lock` present | `… before retrying this branch name. A lock refs/heads/<b>.lock is also present: if another git process is running against this repository, the lock may be live and clears on its own; if none is, it is stale debris of the interrupted delete — remove the lock file by hand too: <process error>` | none |
| the name starts with `+`, so no call runs | `branch <b> left in place (its name is unsafe to pass to update-ref); delete it by hand` | none |
| the step deleted it (measured on Linux, macOS and Windows), or the branch is absent at the check | none | none |

- The dash in the lock text is the raw UTF-8 em dash U+2014.
- claustrum adds the lock part when the file `<common git dir>/refs/heads/<b>.lock`
  exists after the failure, also after a stop (claustrum's own).
- A name that starts with `-` gets the text of a `+` name (claustrum's own). A
  create does not reach it, because git refuses `-b -<name>` (host git, not
  measured).
- When claustrum's home or identity guard skips the leaf, the leaf stays. A kept
  branch then gets its text without `the worktree itself was removed, but `
  (claustrum's own).
- On Windows a stopped call ends with exit code 1. A Windows rollback text then
  ends `exit status 1` or ` (exit status 1)` where Linux and macOS show a signal
  (the Windows texts are not measured).
- A rollback that fails in two places, other than after a failed rmdir, joins
  the two parts with `; ` in the same order (claustrum's own).
