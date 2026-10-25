# Error texts of the daemon

This page lists the method-level error texts of the daemon in one table. The table links to the method pages for the texts that only one method sends. The [protocol overview](../PROTOCOL.md#error-codes) has the table of error codes.

An untagged row on this page matches build `5fd08069` or an earlier build, measured on Linux.

## Error-string catalogue

The method-level error texts, verbatim. A text that only one method sends is on the
page of that method, and a row of the method links there. The method pages under
`docs/protocol/` give the trigger and the result shape. A text with no code and no
"in `error`" note is a JSON-RPC error `-32602`. "in `error`" is the `error` member of
the result. A row that says "in the frame of" a method, or "The same frames", is
`-32603` on `git.info`, `git.status` and `git.list_branches`. On `git.worktree_create`
and `git.worktree_remove` it is in `error`. A remove can wrap such a text in a longer
one, as the table in [Git-directory trust check](git-rules-trust.md#git-directory-trust-check) shows. A
row with no mark holds a text of the reference daemon, measured on Linux against a
build up to `5fd08069`. A "measured on macOS" or "measured on Windows" mark covers the
sentence that it ends, and that sentence was measured on that system only. A sentence
that names systems without "measured" holds on each one. The [reference build history](../REFERENCE-BUILDS.md) has the builds.

| namespace / context | error string | code / notes |
|---|---|---|
| protocol | `Invalid JSON-RPC version` | -32600 |
| protocol | `Invalid method format: <m>` / `Unknown namespace: <ns>` / `Unknown method: <ns>.<m>` | -32601 |
| protocol | `Invalid params` | -32602 (absent/mistyped `params`) |
| protocol | `Unauthorized: invalid or missing auth token` | -32001 |
| protocol | `recovered panic: <v>` | -32603 (claustrum-only, see [Handler panic recovery](../PROTOCOL.md#handler-panic-recovery)) |
| files.stat | The texts that only this method sends | On the page: [files.stat → Errors](files-stat.md#errors) |
| files.read | The texts that only this method sends | On the page: [files.read → Errors](files-read.md#errors) |
| files.list | The texts that only this method sends | On the page: [files.list → Errors](files-list.md#errors) |
| files.validate | The texts that only this method sends | On the page: [files.validate → Response](files-validate.md#response) |
| files.extract_tar | The texts that only this method sends | On the page: [files.extract_tar → Errors](files-extract-tar.md#errors) |
| git.status | The texts that only this method sends | On the page: [git.status → Errors](git-status.md#errors) |
| git.status / git.list_branches | `<go error>`, for example `exit status 128` | -32603. The git call failed |
| git.list_branches / git.info / git.status / git.worktree_create | `git took too long: Claude stopped git <subcommand> after 2 minutes; try again` | -32603 on the other methods. Create puts it after the text of the stopped step, as `git worktree add failed: ` or the `mkdir_failed` text of the parent step. A stopped repository test gets it alone, with `worktree_add_failed`. While a request with `"shadowDirOff":true` runs, the text says `after 30 minutes` (claustrum's own). See [Time limits of git calls](git-rules-calls.md#time-limits-of-git-calls) |
| the four methods | `config-defined hooks could not be pinned off; git not run: listing the configuration in force: git took too long: Claude stopped git config after 30 seconds; try again` | -32603 on the other methods. Create: in `error`, `worktree_add_failed`, after `git worktree add failed (checkout): ` for the checkout listing |
| the four methods | `this repository's git config cannot be used: git did not finish reading "<path>" within 30 seconds; try again` | -32603 on the other methods. Create: in `error`, `worktree_add_failed` |
| git.* | `config-defined hooks could not be pinned off; git not run: inherited GIT_CONFIG_COUNT "<value>" is not a count` | in the frame of each method, when the daemon's own `GIT_CONFIG_COUNT` does not parse. See [The daemon's own git environment](git-rules-calls.md#the-daemons-own-git-environment) |
| git.* | `config-defined hooks could not be pinned off; git not run: inherited GIT_CONFIG pair <n> is incomplete` | in the frame of each method, when a pair below the daemon's `GIT_CONFIG_COUNT` is not set. See [The daemon's own git environment](git-rules-calls.md#the-daemons-own-git-environment) |
| git.* | `config-defined hooks could not be pinned off; git not run: listing the configuration in force: exit status 128: <git text>` | in the frame of each method, for a `GIT_CONFIG_KEY_<n>` of the daemon that git cannot read. Linux, macOS and Windows. See [The daemon's own git environment](git-rules-calls.md#the-daemons-own-git-environment) |
| git.* | `config-defined hooks could not be pinned off; git not run: listing the configuration in force: fork/exec <git.exe path>: The directory name is invalid.` | Windows only. In the frame of `git.info`, `git.list_branches`, `git.status` and `git.worktree_create`. The `path` of the first two, or the `baseRepo` of the last two, exists and git cannot start in it, for example a regular file. See [Git-directory trust check](git-rules-trust.md#git-directory-trust-check) |
| git.* | `git cannot run on this host; git not run: <exec error>: <git text>` | in the frame of each method, when the configuration listing fails and `git version` fails too. The exec error and the text are those of `git version`. Linux, macOS and Windows. It also follows a failed read of the test repository's config in [the start test](git-rules-settings-copy.md#the-start-test), and a failed [call about the repository](git-rules-settings-copy.md#the-call-about-the-repository), when `git version` fails too. For `git.worktree_remove` see the table in [Git-directory trust check](git-rules-trust.md#git-directory-trust-check) |
| git.* | `this repository's git config cannot be used: <detail>` | -32603 on `git.info`, `git.status` and `git.list_branches`, and in the frame of `git.worktree_create` and `git.worktree_remove`. The details are in [Repository config refusals](git-rules-config.md#repository-config-refusals) |
| git.* | `a file in this repository's .git directory cannot be used: <detail>` | The same frames. See [Repository config refusals](git-rules-config.md#repository-config-refusals) |
| git.* | `Claude could not test git on the remote machine (<cause>). Check free disk space and quota there, then try again.` | -32603 on `git.info`, `git.status` and `git.list_branches`, and in the frame of `git.worktree_create` and `git.worktree_remove`. See [The settings copy](git-rules-settings-copy.md#the-settings-copy) |
| git.* | `Claude could not prepare its own files on the remote machine: cannot write its copy of the repository's git config: <cause>` | The same frames. The daemon cannot make the folder of the copy |
| git.* | `Claude could not prepare its own files on the remote machine: git is running in 128 repositories at once; try again` | The same frames. See [The settings copy](git-rules-settings-copy.md#the-settings-copy) |
| git.* | `Claude could not prepare its own files on the remote machine: its copy of this repository's settings was cleared away before git started; try again` | The same frames. Measured on `git.status` only. From the code: a checkout puts it after `git worktree add failed (checkout): `. See [The settings copy](git-rules-settings-copy.md#the-settings-copy) |
| git.* | `git could not check this repository on the remote machine: at "<root>": exit status <n>` | The same frames, for a failed call about the repository. See [The call about the repository](git-rules-settings-copy.md#the-call-about-the-repository) |
| git.* | `git could not check this repository on the remote machine: it did not finish within 30 seconds in "<root>"; try again` | The same frames, for a call about the repository that the daemon ended |
| git.* | `git and Claude disagree about which repository holds this folder: from "<root>" git finds "<path>", Claude finds "<git directory>"; try again` | The same frames, for a call about the repository that names another path |
| git.* | `config-defined hooks could not be pinned off; git not run: listing the configuration in force: a configuration key is longer than the listing reads` | in the frame of each method, for a configuration key over 1048576 bytes. Measured on `git.info`. See [Hardened git calls](git-rules-calls.md#hardened-git-calls) |
| git.* | `config-defined hooks could not be pinned off; git not run: too many configured hooks to pin` / `config-defined hooks could not be pinned off; git not run: configured hook names exceed the aggregate pin byte bound` | in the frame of each method, for more than 1024 hook names, or names over 65536 bytes in sum. Measured on `git.info`. See [Hardened git calls](git-rules-calls.md#hardened-git-calls) |
| git.* | `the repository's git directory could not be trusted; git not run: commondir not as git writes it: <S1, S2 or S3>` | in the frame of each method, for a stray `commondir`. [Git-directory trust check](git-rules-trust.md#git-directory-trust-check) gives the three texts |
| git.worktree_create / git.worktree_remove | `baseRepo is inside a managed worktrees directory (beneath .claude/worktrees, or beneath a directory holding a .claude-managed-worktrees marker), or could not be validated as a trust root` | in `error`. Create adds `errorCode:"nested_base_repo"`, and remove has none. It answers a `baseRepo` under a managed worktrees tree, or one that fails claustrum's own trust-root test. No git runs. See [git.worktree_create](git-worktree-create.md) and [git.worktree_remove](git-worktree-remove.md#where-the-paths-can-be) |
| git.worktree_create | The texts that only this method sends | On the page: [git.worktree_create → Error texts](git-worktree-create.md#error-texts) |
| git.worktree_create / git.worktree_remove | `refusing to <verb> worktree: <p> is not <worktree location>/<directory>/<name> beneath <root>` | in `error`, as are the rows below it up to `passes through too many symbolic links`. `<p>` and `<root>` as sent. Create adds `errorCode:"unsafe_path"` in each of these rows that names no other code |
| git.worktree_create / git.worktree_remove | `refusing to <verb> worktree: <p> uses the reserved name <name> for a directory under the worktree location` | `<name>` is `.git` or `.claude-managed-worktrees`, in any letter case |
| git.worktree_create / git.worktree_remove | `refusing to <verb> worktree: <root> has a component named <name>, which is reserved; …` | A `.claude-managed-worktrees` component is not measured |
| git.worktree_create / git.worktree_remove | `refusing to <verb> worktree: <root> would place worktrees in <dir>, which contains the repository <repo>; choose a location the repository is not inside` | |
| git.worktree_create / git.worktree_remove | `refusing to <verb> worktree: <root> would place worktrees in <dir>, which is (or is inside) <wt>, a worktree of the repository <repo>; …` | |
| git.worktree_create / git.worktree_remove | `refusing to <verb> worktree: <root> resolves to the filesystem root; …` | |
| git.worktree_create / git.worktree_remove | `refusing to <verb> worktree: <root> passes through too many symbolic links (a loop?)` | Create: `errorCode:"unsafe_path"`. Linux and macOS, with `worktreeRoot`. A symlink loop or too many links lie in the path of the root. claustrum uses the kernel limit (not measured). `<root>` is `worktreeRoot` without a trailing slash. claustrum also drops a `//` here (not measured). Nothing is created or removed |
| git.worktree_remove | The texts that only this method sends | On the page: [git.worktree_remove → Error texts](git-worktree-remove.md#error-texts) |
| process.spawn | The texts that only this method sends | On the page: [process.spawn → Errors](process-spawn.md#errors) |
| launcher.resolve | The texts that only this method sends | On the page: [launcher.resolve → Errors](launcher-resolve.md#errors) |
| process.stdin | The texts that only this method sends | On the page: [process.stdin → Errors](process-stdin.md#errors) |
| process.spawn / process.killAndWait / process.reattach | `Process ID is required` | `id` is absent or empty |
| plugins.prune | The texts that only this method sends | On the page: [plugins.prune → Errors](plugins-prune.md#errors) |

`-install` reports failures inside the `__INSTALL_RESULT__` facts line as
`cliError` strings, not through an exit code. They are catalogued in
[-install](../INSTALL.md#-install-ensure-the-agent-cli).
