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

The combine test comes before the shape test (measured on Linux). The place of the `cwd` test after the shape test is claustrum's own, not measured. Each refusal is `-32602` with `"data":{"reason":"other"}`. Each member below is measured on one system at least.

1. The combine test gives `gitRead needs the command "git" and a cwd, and cannot be combined with env, launcher, pty, linkBound, cliTempRoot or sessionFoldersOff`. It refuses a command other than the bare name `git`, no `cwd` and a non-empty `env`. It also refuses `"sessionFoldersOff":true`, a `linkBound` object, `"cliTempRoot":true` and a `pty` object with a key. An empty `env` object passes (measured on Linux).
2. The shape test gives `not one of the Changes pane's git commands (<N> arguments)`, where `<N>` is the count of `args`.
3. The `cwd` test of every spawn comes last: a `cwd` that does not exist gets `cwd_missing` (measured on Linux).

With `gitRead`, `"pty":true` gets `Invalid params` (measured on Linux and Windows). So does a `cliTempRoot` text or object (measured on Linux). A `pty` object with no valid key gets `A terminal needs a key of 32 bytes in hex`, with or without `gitRead` (measured on Linux and macOS).

claustrum's own: a `launcher` array gets the combine text. The [linkBound](process-link-bound.md#the-refusals) tests come before these tests. So `gitRead` with a `linkBound` object and a `launcher` gets the `linkBound` text.

## The allowed commands

Each fixed word has a fixed place. A slot takes a ref or a path from the request. The slots are measured on Linux.

- `status --porcelain=v2 --branch -z`
- `rev-parse --show-toplevel`, `rev-parse --abbrev-ref HEAD`, `rev-parse --is-shallow-repository`, `rev-parse --verify <ref>`
- `symbolic-ref --short HEAD`
- `ls-files --others --exclude-standard --full-name :/`
- `show --no-textconv --no-ext-diff <rev>:<path> --`
- `merge-base <ref> <ref>`, `rev-list --left-right --count <A>...<B>` and `<A>..<B>`
- `diff --name-status -M <ref>` and `diff --name-status -M <ref> <ref>`

A `<ref>` that is empty or starts with `-` gets the shape refusal (measured on Linux). So does every other argv tried, for example one with an added option, a missing or extra `--` or a second path (measured on Linux). claustrum's own: also an argv no row tried.

claustrum's own, not measured: a ref with a space or `..` inside goes to git. Each side of a range passes the ref test. A `<rev>:<path>` value needs a `:`.

## The run

The daemon takes the steps of the git methods first: the start test, the settings copy and one `config -z --list` in the `cwd` (measured on Linux). The git then gets 37 `-c` items, the hook pins and `GIT_OPTIONAL_LOCKS=0`. Most commands get `GIT_INDEX_FILE=<copy>/no-index`. Some commands get extra arguments, and `diff` with one ref runs as `diff-index` (measured on Linux, and `status` on Windows). claustrum's own, not measured: `diff` with two refs runs as `diff-tree -r` with the same options.

A folder with no repository is not refused. The git exits 128 with `fatal: not a git repository: '/dev/null'` (`'NUL'` on Windows, measured on Linux and Windows).

The git holds its copy of the settings until it exits, as each git call of the git methods does (claustrum's own).

claustrum's own: a refusal of the git steps fails the spawn with `-32603`, its text and the reason `other`.

## The time limit

The git gets `SIGTERM` 120 s after its start. A git that ignores it runs about 7 s more. No member sets the limit. A plain child is not ended at it. At the limit the daemon logs `[process.Manager] the pane's git has run for 2m0s; ending it`.

- Linux and macOS: `SIGTERM` goes to the git alone. If the git still runs 7 s later, its process group gets `SIGKILL`. A git that ends on the `SIGTERM` gets no `SIGKILL`, and a process in its group lives on.
- Windows: the git ends at the limit with exit code 1. claustrum ends it as it ends a child for every signal (claustrum's own).

The `exit` frame of a git that the limit signalled first carries `"killedBy":"limit"`. A Linux frame:

```json
{"type":"stream","processId":"gr-none","stream":"exit","seq":1,"exitCode":-1,"signal":"SIGKILL","killedBy":"limit","end":{"at":75,"len":114}}
```

Its line is `[process.Manager] Process gr-none exited with code -1, terminated by SIGKILL, asked to end by this daemon (limit)`. A git that exits on the `SIGTERM` has its own exit code and no `signal` member (measured on Linux and macOS). On Windows the frame has `"exitCode":1` and no `signal` member.

claustrum's own, not measured: a git that ended before the limit gets no line and no signal, also while its exit drain is open. A refused `SIGTERM` still goes on to the 7 s step.
