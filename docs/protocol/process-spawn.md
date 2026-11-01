# process.spawn

`process.spawn` starts a child process as the user of the daemon and streams its output to the connection that sent the request. [process.reattach](process-reattach.md) takes the process over.

Treat the socket and the token as shell access. See [`SECURITY.md`](https://github.com/schubydoo/claustrum/blob/main/SECURITY.md).

An untagged rule on this page is measured against build `89cb6289`. The removal of `CLAUDE_SSH_PEER_CHECK` and the spawn during a shutdown are measured against build `5fd08069`.

## Request

```json
{"id": "p1", "command": "/usr/bin/env", "args": ["-0"], "cwd": "/repo", "env": {"FOO": "bar"}}
```

| Parameter | Required | Meaning |
|---|---|---|
| `id` | yes | Any string. The id of the process in each later `process.*` request and in each stream frame. |
| `command` | yes | The program. A bare name is looked up on the `PATH` of the daemon. With `launcher`, it must be an absolute path. |
| `args` | no | The arguments after the program, as strings. |
| `cwd` | no | The working directory of the child. A leading `~` is expanded, as in [Tilde expansion](../PROTOCOL.md#tilde-expansion-in-path-params). Without it, the child gets the working directory of the daemon. |
| `env` | no | An object of string values, merged over the daemon environment. See [The environment of the child](#the-environment-of-the-child). |
| `disableShellAgentSocket` | no | `true` skips the SSH agent hand-off for this spawn. `null` counts as `false`. |
| `launcher` | no | An array of strings that names a managed launcher. See [The managed launcher](#the-managed-launcher). Absent or `null` means no launcher. |
| `linkBound` | no | See [its page](process-link-bound.md). |
| `wantPid` | no | `true` adds `pid` and `startTime` to the result. An addition of claustrum, see [CT-1](../DIVERGENCES.md#ct-1). Any other value counts as absent. |

## Response

```json
{"success": true}
```

With `"wantPid": true` the result is `{"success":true,"pid":<int>,"startTime":<number>}`. `pid` is the OS pid of the child. `startTime` is the wall clock of the daemon at the spawn, in epoch seconds.

`startTime` is an opaque token that detects a reused pid, not an OS start time.

After the result, the output comes as [stream notifications](streams.md#stream-notifications) with the `id` as `processId`. The last one is the `exit` frame.

## Errors

A failure is a JSON-RPC error. There is no `errorCode` member.

| Code | Text | Cause |
|---|---|---|
| `-32602` | `Invalid params` | No `params`, or a member that the method reads has the wrong type. A `launcher` that is not an array, or that holds an element that is not a string, gets it too. A `null` element becomes an empty argument. See [the rules for `params`](../PROTOCOL.md#params-presence-and-typing). |
| `-32602` | `Process ID is required` | `id` is absent or empty. |
| `-32602` | `Command is required` | `command` is absent or empty. |
| `-32004` | `the managed launcher cannot be used: <reason>` | The launcher checks refuse `launcher`. On Windows every `launcher` gets this, with the reason `a launcher cannot be applied on a Windows host`. |
| `-32602` | `command must be an absolute path when a launcher is given` | A relative `command` with a `launcher`. |
| `-32602` | `linkBound cannot be combined with pty or launcher` | With `launcher` (`683cc6a9`). |
| `-32602` | `this id is in use, or was a terminal's or a link-bound process's: take a fresh one` | A [spent](process-link-bound.md#the-spent-id) `id` (`683cc6a9`). |
| `-32603` | `chdir <cwd>: stat <cwd>: no such file or directory` | `cwd` does not exist. |
| `-32603` | `chdir <cwd>: not a directory` | `cwd` is not a folder. |
| `-32603` | `exec: "<name>": executable file not found in $PATH` | A bare name that the lookup does not find, and on Windows also a path with no extension. On Windows the text ends in `%PATH%`. |
| `-32603` | `fork/exec <command>: <reason>` | The command is missing, a folder or not executable, or the start fails. On Windows a missing file gives the reason `The system cannot find the file specified.` |
| `-32005` | `the managed launcher <path> could not be started: <reason>` | The launcher passes the checks and does not start. |

`<cwd>` is the value after the tilde expansion. `<command>` is the path of the start: the value of the request, or for a bare name the path that the lookup found on `PATH`.

### After a failure

A failure starts no child and makes no record. It logs the failed-start line of [Lines of child processes](../DAEMON.md#lines-of-child-processes). An `Invalid params`, `Process ID is required`, `Command is required` or `linkBound cannot be combined with pty or launcher` error writes no line.

### The reason member (build `52d82f0a`)

A start refusal adds `data` after `message`: `{"reason":"<reason>"}`. `Invalid params`, `Command is required` and `Process ID is required` (claustrum's own) carry none.

| Reason | Linux | Windows |
|---|---|---|
| `cwd_missing` | missing or a file | the same |
| `cwd_unusable` | a `cwd` of mode 000 that exists | none (claustrum's own) |
| `not_found` | not found | not found, a folder |
| `not_executable` | mode 0644, a folder | not measured |
| `launcher` | missing (`-32004`) | any `launcher` |
| `other` | a relative `command` with a `launcher` | not a program |

On macOS only `cwd_unusable` is measured. The rest follows Linux, as do the other `-32004` refusals and `-32005` (`launcher`) and other errors (`other`) (claustrum's own).

One failure ends another process first: a session spawn whose command exists and does not start. See [Session supersede](#session-supersede).

## Behavior

### What the child gets

- **Streams.** The child's stdin, stdout and stderr are daemon pipes, which send what they read as stream frames to the spawning connection and store them.
- **The life of the process.** A close of its connection ends only a [link-bound](process-link-bound.md) process. A graceful shutdown ends the processes of the table, except with `-keep-children` ([CT-2](../DIVERGENCES.md#ct-2)).

### The environment of the child

The child gets the environment of the daemon with the `env` of the request merged over it. On Linux and macOS the entries come in this order:

1. The entries of the daemon, with `CLAUDE_SSH_DAEMON_CHILD=1` last. A caller key that the daemon has replaces that entry in place. An empty caller value of `CLAUDE_SSH_DAEMON_CHILD` stays empty (measured on Linux).
2. The new caller keys, in no fixed order (measured on Linux).
3. `SSH_AUTH_SOCK` from [the hand-off](#the-ssh-agent-hand-off-linux-and-macos). Without that step there is no entry here.
4. `CLAUDE_SSH_RUN_DIR` and then `CLAUDE_SSH_CHILD`, the two [identity markers](#the-identity-markers-linux-and-macos). A caller value of either does not reach the child.

After a good read, the child gets the login-shell `PATH` of the user. A `PATH` in `env` wins over it. See [The login-shell PATH](#the-login-shell-path-linux-and-macos). The value never changes how the daemon looks up `command`.

The daemon removes these names:

| Name | Removed from |
|---|---|
| `CLAUDE_RPC_TOKEN`, `CLAUDE_SSH_NO_JOBS` (`52d82f0a`, measured on Linux, macOS and Windows) | The daemon value, for every child. A value in the `env` of the request reaches the child (claustrum's own for `CLAUDE_SSH_NO_JOBS`). |
| `CLAUDE_SSH_MANAGED_LAUNCHER`, `CLAUDE_SSH_E2E_MANAGED_SETTINGS_DIR` | Every child. The removal covers the `env` of the request too (measured on Linux and macOS). The removal on Windows (claustrum's own). |
| `CLAUDE_CODE_PROCESS_WRAPPER` | A child with a `launcher`, also from the `env` of the request. A child without one keeps it (measured on Linux and macOS, claustrum's own on Windows). |
| `CLAUDE_SSH_NO_LOGIN_PATH_WARM` | The daemon value, for every child (claustrum's own on Windows). A value in `env` reaches the child (claustrum's own). |
| `CLAUDE_SSH_PEER_CHECK` | The daemon environment, at its start. On Windows that covers any letter case. A value in `env` reaches the child. These three are measured on Linux, macOS and Windows. If the removal fails, claustrum logs one WARN line of its own, and the children inherit it. |

The Go runtime variables `GODEBUG`, `GOGC`, `GOMAXPROCS`, `GOMEMLIMIT` and `GOTRACEBACK` reach the child unchanged. `GOGC` and `GOMAXPROCS` are measured on Linux, the other three are not measured.

### A temp folder that takes no file (build `683cc6a9`)

On Linux and macOS a daemon value of `CLAUDE_CODE_TMPDIR`, `TMPDIR`, `TMP` or `TEMP` does not reach a child if its folder cannot take a new file `.claude-ssh-<n>`. For a missing folder the nearest existing parent folder takes the test. A missing folder under a folder that takes the file stays. On Linux a relative value stays too, and spawns at once share one test, which removes the file. `TMP`, the order of the names and the test of the parent are claustrum's own, not measured. So is the wait of at most 2 s per test.

### The managed launcher

With `launcher`, the child runs as `<launcher...> <command> <args...>`. The launcher is the process, and the stream frames are its own. A spawn reads no managed settings. The `CLAUDE_SSH_MANAGED_LAUNCHER` gate does not change it (measured on Linux).

The checks run in this order. Step 1 is the Windows rule. The other steps are measured on Linux. Step 5 is measured on macOS too, and so is the interpreter text of step 6:

1. On Windows any `launcher`, `[]` too, gets `-32004 … a launcher cannot be applied on a Windows host`.
2. A relative `command` gets `-32602 command must be an absolute path when a launcher is given`.
3. The checks of `launcher.resolve` run, with `command` in place of `cliPath`. A refusal gets `-32004 the managed launcher cannot be used: <reason>`, with a reason of [Unusable reasons](launcher-resolve.md#unusable-reasons). `[]` gets the text for no launcher.
4. The `cwd` test, with its usual error.
5. A `command` that is missing, a folder or not executable gets `fork/exec <command>: <reason>`, as without a launcher. The launcher does not run.
6. A launcher that does not start gets `-32005`. A missing `#!` interpreter, or a script with Windows CRLF line ends, gives the reason `its interpreter was not found (the program on its #! line, or the loader of an ELF binary; a script saved with Windows CRLF line endings fails this way)`. A 0755 text file with no `#!` gives `exec format error`.

claustrum's own: step 1 comes before step 2, step 5 before the launcher starts, and any other start failure gives its errno text. The step 6 frames of claustrum are tested, not measured.

A started launcher logs the start line of [Lines of child processes](../DAEMON.md#lines-of-child-processes), which names the first element of `launcher` only. A refusal writes no `[LauncherHandler]` line.

## Platform notes

=== "Linux and macOS"

    - **Process group.** The child leads its own process group. A `KILL` from [process.kill](process-kill.md) reaches the whole group.
    - **SSH agent hand-off and `launcher`.** Yes, and allowed.
    - **Identity markers, records, reap.** Yes.
    - **`signal` in the `exit` frame.** Yes.

=== "Windows"

    - **Process group and job.** The child starts in a new process group and in a [job](process-kill.md#windows-child-trees) (`52d82f0a`).
    - **`launcher`.** Refused with `-32004`.
    - **`signal` in the `exit` frame.** Never.

    These rules are measured on Windows, unless a tag says otherwise.

    - There is no SSH agent hand-off and no identity marker. The capability and the parameter exist.
    - The block of the child is in name order ([D23](../DIVERGENCES.md#d23)).
    - The child gets the `PATH` of the daemon as an entry named exactly `PATH`. claustrum reads it once, at its start (claustrum's own). An entry named `PATH` is replaced in place, else `PATH` is added. Then the `env` of the request comes.
    - Of the entries whose names differ only in letter case, the child gets the last one (from the code). So a daemon `Path` reaches the child as `PATH`, and a caller `Path` value is lost. A caller `PATH` or `path` value keeps its own name.
    - A daemon with no path entry, or a `Path` with an empty value, gives the child no `PATH` entry.

### The login-shell PATH (Linux and macOS)

The read starts in the background with the daemon. These rules are measured against `52d82f0a`.

- `CLAUDE_SSH_NO_LOGIN_PATH_WARM=1` moves the start of the read to the first spawn. The value 0 does not.
- A spawn waits for a running read at most 4 s, and not past 8 s after its start. Until a good read, children get the inherited `PATH`.
- A later spawn tries a failed read again after 30 s, then 1m0s and 2m0s. A good read is kept.
- `"disableShellAgentSocket":true` kills the shell group of a read that still runs after the wait, and after a failure starts no read. The shutdown kills it too.
- The read runs `$SHELL -l -i -c …` if `$SHELL` is an executable file, or else the first usable of `/bin/zsh`, `/bin/bash` and `/bin/sh`.
- The `PATH` is the line after a fresh sentinel line, on stdout or on stderr, with a final newline and an absolute directory.
- 2 MiB on stdout fails the read, and a full stderr does not. A shell that leaves a process on its stdout still gives its PATH, about 500 ms after it ends. The log holds no shell output.
- The shell gets `HOME`, `PATH`, `SHELL`, `TERM` and `USER` of the daemon, with `TERM` empty if the daemon has none, plus `DISABLE_AUTO_UPDATE=true`, `ZSH_DISABLE_COMPFIX=true` and `CLAUDE_DESKTOP_RESOLVING_ENVIRONMENT=1`.
- claustrum's own: a 30 s limit per read, the stream order, the doubling after 2m0s, no try after 16 failures, and no wait after 5.

### The identity markers (Linux and macOS)

The daemon starts the child through a short re-exec of its own binary, the exec-child trampoline. It adds two markers, so a later daemon can tell the child from a process that reuses its pid:

- `CLAUDE_SSH_RUN_DIR=<run dir>`. The run dir is the folder of the socket path, cleaned and not made absolute. The socket `<R>/b/s.sock` gives `<R>/b`.
- `CLAUDE_SSH_CHILD=<pid>:<start>`. On Linux `<start>` is the start time of the child in `/proc` clock ticks. On macOS it is the start time from `ps` as a date text with one blank between its words. Only days 1 and 2 of a month are measured.

The trampoline adds no frame.

??? note "The trampoline does not start"
    If the trampoline does not start, the daemon logs `[process.Manager] exec trampoline failed for <id> (<error>); starting it directly — a successor daemon will not be able to reap it` and starts the command directly (measured on Linux). The frame then names the command, for example `fork/exec <command>: permission denied` for a `cwd` that the daemon cannot enter. From the code: if that direct start works, the reply is success, and the child gets `CLAUDE_SSH_RUN_DIR` and no `CLAUDE_SSH_CHILD`.

??? note "A later daemon start"
    A later daemon start ends a child of a gone daemon that still matches its record, with `SIGTERM` and then `SIGKILL` ([Child records and the reap](../DAEMON.md#child-records-and-the-reap-linux-and-macos)). A record that cannot be written does not fail the spawn, and the child still gets both identity markers.

### The SSH agent hand-off (Linux and macOS)

The daemon advertises it as the feature `process.spawn.shellAgentSocket`. A child whose environment has no `SSH_AUTH_SOCK` entry gets the one that the user's login shell exports. Its rules are measured on Linux and macOS.

- An entry that is there wins, even an empty one such as `"SSH_AUTH_SOCK":""` in `env`. `"disableShellAgentSocket":true` skips the step too.
- The daemon runs the same shell as the `PATH` read at the first spawn that needs a socket. The run has a 4 s limit, and the daemon gives up on a shell that the kill cannot end at 5 s.
- Measured on Linux against `683cc6a9`: a plain file gets no connect call. A live answer holds with no connect at 60 s, for a dead agent, and is gone at 610 s. A new socket there is dialed again. claustrum's own: any other non-socket, the 600 s, and the key of path, device, inode and mtime.
- A dialed socket that does not accept a connection in 250 ms is not handed on. The shell runs again only 10 minutes after the last run, and not after 3 failed runs in a row. A connection attempt still open after 500 ms ends the hand-off for good, and so does a stat (claustrum's own).
- While the `PATH` read runs, the step runs no shell (measured against `52d82f0a`).
- The step never fails a spawn. Each shell run and each give-up writes a `[shellenv]` log line.

## Rare states

### A spawn with an `id` that is in use

A spawn with the `id` of a process that still runs succeeds and replaces its table entry. The first process gets no signal and keeps running. Then:

- `process.kill`, `process.stdin` and `process.reattach` of that `id` reach the second process only.
- `-stop` and `server.shutdown` end only the processes of the table, so the first process outlives the daemon, except on Windows with jobs on and no `-keep-children` (claustrum's own).
- Each process keeps the connection that spawned it, under the same `processId`, and each counts its own `seq`. So a connection that sent both spawns gets two streams under one `processId`, with two `exit` frames (measured on Linux).
- A `process.reattach` moves the stream of the second process and closes the connection that had it. A connection that gets the frames of the first process keeps getting them (measured on Linux).

### Session supersede

A spawn whose `args` name a stream-json session of the Claude Code CLI ends every other running process of the same session. Its `args` must hold:

- `--input-format=stream-json`, `--output-format=stream-json` or a bare `stream-json`, and
- a session id from `--session-id <v>` or `--session-id=<v>`. Without one, it is `--resume <v>` or `--resume=<v>`, unless `--fork-session` is there too.

The argument after a bare `--session-id` or `--resume` is its value. The id has 1 to 128 characters (from the code). Each is a letter, a digit, `-`, `_`, `.` or `:`, and the first is not `-` (from the code). Any other spawn supersedes nothing.

#### How the old process ends

The daemon sends `SIGTERM` to the direct child of the old process and waits. After 3 s it sends `SIGKILL` to the group. The `exit` frame of the old process reaches its client with `"killedBy":"client"`. The pipe drain of that frame takes up to 5 s. Then the new process starts, and its reply comes after that `exit` frame. On Windows the old job ends at once (claustrum's own). Two spawns of one session run one after the other (claustrum's own).

A spawn with the `id` and the session of a running process only replaces it, as [the `id` in use](#a-spawn-with-an-id-that-is-in-use) says.

#### Edge cases

??? note "The test before the old process ends"
    Before it ends the old process, a session spawn tests its command. If the test fails, the old process gets no signal. These tests are measured on Linux, and the missing path on macOS too:

    - The test is one `stat` of `command` as the request gives it. It is not joined with `cwd`. So a session spawn of `./tool` with a `cwd` that holds `tool` answers `fork/exec ./tool: no such file or directory`.
    - A bare name that the lookup does not find answers its lookup error.
    - A missing or wrong `cwd` answers its own error first.
    - A command that exists and does not start passes the test. A file of mode 0644, a folder and a script with a missing interpreter each end the old process first, and then the error comes.
    - With a `launcher`, step 5 of [the launcher checks](#the-managed-launcher) is the test. So a missing, folder or not executable command ends no old process. A launcher that does not start ends the old process first, and then `-32005` comes.
    - On Windows the lookup answers first. Then the daemon runs one `stat` of an absolute `command` only. A missing path that ends in `.exe` answers `fork/exec <path>: The system cannot find the file specified.` A relative path gets no `stat` (not measured).

??? note "A -stop during the wait"
    A `-stop` can come while a spawn waits for the old process (measured on Linux and macOS). The spawn then gets no reply, and the new process never starts. The daemon sends `SIGKILL` to the group of the old process, and no connection gets its `exit` frame.

### A spawn during a shutdown

A spawn that reaches the start of its child after the shutdown began gets no reply. The daemon ends that child at once, also with `-keep-children` ([CT-2](../DIVERGENCES.md#ct-2)). The reference leaves it running (measured on Linux, macOS and Windows).

## Differences from the reference

[Divergence catalog](../DIVERGENCES.md) entries of this method:

- [CT-1](../DIVERGENCES.md#ct-1): the optional `wantPid` parameter with `pid` and `startTime` in the result.
- [CT-2](../DIVERGENCES.md#ct-2): the `-keep-children` flag.
- [D20](../DIVERGENCES.md#d20): the 50 ms wait in the reap before the `SIGKILL` of a group whose leader reads as gone.
- [D23](../DIVERGENCES.md#d23): on Windows the environment block of a child is in name order.

claustrum lacks the terminal of the pinned `683cc6a9` and ignores its `pty` member without `linkBound`. On Windows `c8e2f083` refuses `pty` with `-32602`, and claustrum runs the command (measured on Windows, [open items](../REFERENCE-BUILDS.md#52d82f0a28317830ccacea80f6f4aac1d7563353-2026-10-09-built)).
