# The pane's git

`process.spawn` with `"gitRead":true` runs one read-only git command of the Changes pane. The git runs under the daemon's own copy of the repository's settings.

An untagged rule on this page is measured against build `c9125eb8` on Linux, macOS and Windows.

```json
{"id": "g1", "command": "git", "args": ["rev-parse", "--show-toplevel"], "cwd": "/repo", "gitRead": true}
```

The result is `{"success":true}`, as for a plain spawn. The output comes in stream frames.

## The member

`gitRead` is a bool. `false` gives a plain spawn (measured on Linux). `null` counts as absent (claustrum's own, not measured). An object gets `-32602` `Invalid params`, and so do a text (Linux and Windows), a number and an array (Windows).

## The refusals

The combine test comes before the shape test (measured on Linux). The `cwd` test comes before the shape test too (measured on Linux, macOS and Windows). The place of the `cwd` test after the combine test is claustrum's own, not measured. Each member below is measured on one system at least.

1. The combine test gives `-32602` `gitRead needs the command "git" and a cwd, and cannot be combined with env, launcher, pty, linkBound, cliTempRoot or sessionFoldersOff` with `"data":{"reason":"other"}`. It refuses a command other than the bare name `git`, no `cwd` and a non-empty `env`. It also refuses `"sessionFoldersOff":true`, a `linkBound` object, `"cliTempRoot":true` and a `pty` object with a key. An empty `env` object passes (measured on Linux).
2. The `cwd` test of every spawn: a `cwd` that does not exist gets `-32603` `chdir <cwd>: ...` with `cwd_missing`, also with a refused shape.
3. The shape test gives `-32602` `not one of the Changes pane's git commands (<N> arguments)` with `"data":{"reason":"other"}`, where `<N>` is the count of `args`. The daemon logs `[process.Manager] Failed to start process <id>: <text>` (measured on Linux).

### Other members

With `gitRead`, `"pty":true` gets `Invalid params` (measured on Linux and Windows). So does a `cliTempRoot` text or object (measured on Linux). A `pty` object with no valid key gets `A terminal needs a key of 32 bytes in hex`, with or without `gitRead` (measured on Linux and macOS).

claustrum's own: a `launcher` array gets the combine text. The [linkBound](process-link-bound.md#the-refusals) tests come before these tests. So `gitRead` with a `linkBound` object and a `launcher` gets the `linkBound` text. A spent `id` gets its refusal before the shape test.

## The allowed commands

Each fixed word has a fixed place. A slot takes a ref or a path from the request. The slots are measured on Linux.

- `status --porcelain=v2 --branch -z`
- `rev-parse --show-toplevel`, `rev-parse --abbrev-ref HEAD`, `rev-parse --is-shallow-repository`, `rev-parse --verify <ref>`
- `symbolic-ref --short HEAD`
- `ls-files --others --exclude-standard --full-name :/`
- `show --no-textconv --no-ext-diff <ref> --`, where `<ref>` is for example `HEAD` or `<rev>:<path>`
- `merge-base <ref> <ref>`, `rev-list --left-right --count <rev>`
- `log --no-show-signature --format=%H%x00%h%x00%s%x00%an%x00%ae%x00%ct%x1e <rev>`
- `hash-object -t tree /dev/null`
- `diff <base>`, `diff --numstat <base>`, `diff -z --no-color --name-status -M <base>`
- `diff --inter-hunk-context=3 <base>`, `diff --src-prefix=a/ --dst-prefix=b/ <base>`
- `diff --name-status -M <base>`, the same with `-- :(top,exclude)<path>` or `-- :(top,exclude,literal)<path>`, and `diff --name-status -M <ref> <ref>`

A `<rev>` is a ref, `<A>..<B>` or `<A>...<B>`. A `<base>` is a ref or `<A>..<B>`. In `diff --name-status -M` a `<base>` with three dots gets the shape refusal, and so does `--inter-hunk-context=10` (measured on Linux, macOS and Windows).

## The ref test

A `<ref>` gets the shape refusal when it is empty or starts with `-` (measured on Linux), or has more than 4096 bytes. So does a ref with a TAB, LF, SOH or DEL byte (measured on Linux, SOH not on macOS and Windows). No git runs. A ref of 4096 bytes runs. In `diff --name-status -M` each side of `<A>..<B>` passes the ref test, so a range of more than 4096 bytes runs when no side has more (measured on Linux, macOS and Windows). Every other argv tried gets the shape refusal too, for example one with an added option, a missing or extra `--` or a second path (measured on Linux). claustrum's own: also an argv no row tried.

claustrum's own, not measured: the other bytes below `0x20` get the refusal. A ref with a space or `..` inside goes to git. A range in the other slots takes the same test of each side. A pathspec needs a path after its prefix. Only `sub`, `a b`, `..` and `*.txt` are measured as the path. Also not measured: the ref test in the other slots and on a pathspec.

## The run

The daemon takes the steps of the git methods first: the start test, the settings copy and one `config -z --list` in the `cwd` (measured on Linux). The git then gets 37 `-c` items, the hook pins and `GIT_OPTIONAL_LOCKS=0`. Most commands get `GIT_INDEX_FILE=<copy>/no-index`. After a good [server.adoptEnv](server-adopt-env.md) the git starts from the environment of the daemon, with no adopted name (measured on Linux, macOS and Windows). Some commands get extra arguments (measured on Linux, macOS and Windows):

- `diff` with one ref runs as `diff-index` with the real index. A patch shape adds `-p`, and a trailing `--` follows the ref.
- `diff --name-status -M <A>..<B>` runs as `diff-tree -r --ignore-submodules=dirty --submodule=short --no-ext-diff --no-textconv --name-status -M <A> <B> --`. The other `diff` shapes with `<A>..<B>` run as `diff-tree -r` too.
- `log` with `<A>...<B>` runs, with the range before the trailing `--`.

claustrum's own, not measured: `diff` with two refs runs as `diff-tree -r` too.

A folder with no repository is not refused. The git exits 128 with `fatal: not a git repository: '/dev/null'` (`'NUL'` on Windows, measured on Linux and Windows).

The git holds its copy of the settings until it exits, as each git call of the git methods does (claustrum's own).

claustrum's own: any other refusal of the git steps fails the spawn with `-32603`, its text and the reason `other`. So does a copy that cannot be written (measured on Linux and Windows).

## The state fallback-git

In the git state `fallback-git` a repository gets `{"code":-32008,"message":"the helper cannot give git its own copy of a repository's settings on this host (this git does not support it, or the app switched it off)","data":{"reason":"other"}}` (measured on Linux, macOS and Windows). The daemon takes the steps up to the `config -z --list` call first, and the git does not run (measured on Linux). In the state `shadow`, `"shadowDirOff":true` on the spawn changes nothing (measured on Linux, macOS and Windows).

## The time limit

The limit is 120 s from the start of the git. No member sets the limit. A plain child is not ended at it. At the limit the daemon logs `[process.Manager] the pane's git has run for 2m0s; ending it`.

- Linux and macOS: `SIGTERM` goes to the git alone. If the git still runs 7 s later, its process group gets `SIGKILL`. A git that ends on the `SIGTERM` gets no `SIGKILL`, and a process in its group lives on.
- Windows: the git ends at the limit with exit code 1. claustrum ends it as it ends a child for every signal (claustrum's own).

The `exit` frame of a git that the limit signalled first carries `"killedBy":"limit"`. A Linux frame:

```json
{"type":"stream","processId":"gr-none","stream":"exit","seq":1,"exitCode":-1,"signal":"SIGKILL","killedBy":"limit","end":{"at":75,"len":114}}
```

Its line is `[process.Manager] Process gr-none exited with code -1, terminated by SIGKILL, asked to end by this daemon (limit)`. A git that exits on the `SIGTERM` has its own exit code and no `signal` member (measured on Linux and macOS). On Windows the frame has `"exitCode":1` and no `signal` member.

claustrum's own, not measured: a git that ended before the limit gets no line and no signal, also while its exit drain is open. A refused `SIGTERM` still goes on to the 7 s step.
