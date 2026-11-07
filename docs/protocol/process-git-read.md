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

claustrum's own: a `launcher` array gets the combine text.

## The allowed commands

The list is the union of the measured rows. Each argv ran on at least one system.

- `status --porcelain=v2 --branch -z`
- `rev-parse --show-toplevel`, `rev-parse --abbrev-ref HEAD`, `rev-parse --is-shallow-repository`, `rev-parse --verify HEAD`
- `symbolic-ref --short HEAD`
- `ls-files --others --exclude-standard --full-name :/`
- `show --no-textconv --no-ext-diff HEAD:README --`
- `merge-base HEAD HEAD`, `rev-list --left-right --count HEAD...HEAD`, `diff --name-status -M HEAD`

claustrum's own: any other argv gets the shape refusal, also one that differs from a row only in a ref or a path.

## The run

The daemon takes the steps of the git methods first: the start test, the settings copy and one `config -z --list` in the `cwd` (measured on Linux). The git then gets 37 `-c` items, the hook pins and `GIT_OPTIONAL_LOCKS=0`. Most commands get `GIT_INDEX_FILE=<copy>/no-index`. Some commands get extra arguments, and `diff` runs as `diff-index` (measured on Linux, and `status` on Windows).

A folder with no repository is not refused. The git exits 128 with `fatal: not a git repository: '/dev/null'` (`'NUL'` on Windows, measured on Linux and Windows).

claustrum's own: a refusal of the git steps fails the spawn with `-32603`, its text and the reason `other`.
