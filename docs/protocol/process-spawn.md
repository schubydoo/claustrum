# process.spawn

`process.spawn` starts a child process as the user of the daemon and streams its output to the connection that sent the request. The client names the process with its own `id`. The process outlives that connection, and another connection can take it over with [process.reattach](process-reattach.md).

Treat the socket and the token as shell access. The daemon runs any command that a request names. See [`SECURITY.md`](https://github.com/schubydoo/claustrum/blob/main/SECURITY.md).

## Request

```json
{"id": "p1", "command": "/usr/bin/env", "args": ["-0"], "cwd": "/repo", "env": {"FOO": "bar"}}
```

| Parameter | Required | Meaning |
|---|---|---|
| `id` | yes | Any string. The id of the process in each later `process.*` request and in each stream frame. |
| `command` | yes | The program. A bare name is looked up on the `PATH` of the daemon. With `launcher`, it must be an absolute path. |
| `args` | no | An array of strings, the arguments after the program. |
| `cwd` | no | The working directory of the child. A leading `~` is expanded, as in [Tilde expansion](../PROTOCOL.md#tilde-expansion-in-path-params). Without it, the child gets the working directory of the daemon. |
| `env` | no | An object of string values, merged over the daemon environment. See [The environment of the child](#the-environment-of-the-child). |
| `disableShellAgentSocket` | no | `true` skips the SSH agent hand-off for this spawn. `null` counts as `false`. |
| `launcher` | no | An array of strings that names a managed launcher. See [The managed launcher](#the-managed-launcher). Absent or `null` means no launcher. |
| `wantPid` | no | `true` adds `pid` and `startTime` to the result. An addition of claustrum, see [CT-1](../DIVERGENCES.md#ct-1). Any other value counts as absent. |

## Response

Success:

```json
{"success": true}
```

With `"wantPid": true` the result is `{"success":true,"pid":<int>,"startTime":<number>}`. `pid` is the OS pid of the child. `startTime` is the wall clock of the daemon at the spawn, in epoch seconds. `process.reattach` with `wantPid` returns the same value for the same process. Use it as an opaque token to detect a reused pid. Compare it only with a later daemon value. It does not equal a start time that the OS reports for the process.

After the result, the output comes as [stream notifications](../PROTOCOL.md#stream-notifications) with the `id` as `processId`. The last one is the `exit` frame.

Failure is a JSON-RPC error. There is no `errorCode` member.

| Code | Text | Cause |
|---|---|---|
| `-32602` | `Invalid params` | No `params`, or a member that the method reads has the wrong type. A `launcher` that is not an array, or that holds an element that is not a string, gets it too. A `null` element becomes an empty argument. See [the rules for `params`](../PROTOCOL.md#params-presence-and-typing). |
| `-32602` | `Process ID is required` | `id` is absent or empty. |
| `-32602` | `Command is required` | `command` is absent or empty. |
| `-32004` | `the managed launcher cannot be used: <reason>` | The launcher checks refuse `launcher`. On Windows every `launcher` gets this, with the reason `a launcher cannot be applied on a Windows host`. |
| `-32602` | `command must be an absolute path when a launcher is given` | A relative `command` with a `launcher`. |
| `-32603` | `chdir <cwd>: stat <cwd>: no such file or directory` | `cwd` does not exist. |
| `-32603` | `chdir <cwd>: not a directory` | `cwd` is not a folder. |
| `-32603` | `exec: "<name>": executable file not found in $PATH` | A bare name that the lookup does not find, and on Windows also a path with no extension. On Windows the text ends in `%PATH%`. |
| `-32603` | `fork/exec <command>: <reason>` | The command is missing, a folder or not executable, or the start fails. On Windows a missing file gives the reason `The system cannot find the file specified.` |
| `-32005` | `the managed launcher <path> could not be started: <reason>` | The launcher passes the checks and does not start. |

`<cwd>` is the value after the tilde expansion. `<command>` is the path of the start: the value of the request, or for a bare name the path that the lookup found on `PATH`. A failure starts no child and makes no record. The daemon logs `[process.Manager] Failed to start process <id>: <message>`, where `<message>` is the text of the frame. An `Invalid params`, `Process ID is required` or `Command is required` error writes no line.

One failure ends another process first: a session spawn whose command exists and does not start. See [Session supersede](#session-supersede).

## What the child gets

- **Process group.** On Linux and macOS the child leads its own process group. A `KILL` from [process.kill](../PROTOCOL.md#processkill) reaches the whole group. On Windows the child starts in a new process group and in no Job Object. A kill there ends the direct child only, as [Windows child trees](process-kill.md#windows-child-trees) says.
- **Streams.** The stdout and the stderr of the child are pipes of the daemon. The daemon sends what it reads as stream frames to the connection that spawned the process, and it stores them for a replay. The stdin of the child is a pipe that [process.stdin](process-stdin.md) writes to.
- **The life of the process.** A close of its connection does not end the process. A graceful shutdown (`-stop`, `server.shutdown`, `SIGTERM`, `SIGINT`, the self-exit) ends the processes of the table. With `-keep-children` it leaves them alive ([CT-2](../DIVERGENCES.md#ct-2)).

## The environment of the child

The child gets the environment of the daemon with the `env` of the request merged over it. On Linux and macOS the entries come in this order:

1. The entries of the daemon, with `CLAUDE_SSH_DAEMON_CHILD=1` last. A caller key that the daemon has replaces that entry in place. An empty caller value of `CLAUDE_SSH_DAEMON_CHILD` stays empty (measured on Linux).
2. The new caller keys. Two or more of them come in no fixed order. With three new keys, the reference and claustrum each gave a rotation of the request order. More keys are not measured.
3. `SSH_AUTH_SOCK` from the hand-off below. Without that step there is no entry here.
4. `CLAUDE_SSH_RUN_DIR` and then `CLAUDE_SSH_CHILD`, the two identity markers below. A caller value of either does not reach the child.

After a good read, the `PATH` entry of the child is the login-shell `PATH` of the user. A `PATH` in `env` wins over it. The read runs at the first spawn, and that spawn waits for it. See [-serve](../PROTOCOL.md#-serve-run-the-daemon). The value never changes how the daemon looks up `command`.

The daemon removes these names:

| Name | Removed from |
|---|---|
| `CLAUDE_RPC_TOKEN` | The daemon value, for every child. A value in the `env` of the request reaches the child. |
| `CLAUDE_SSH_MANAGED_LAUNCHER`, `CLAUDE_SSH_E2E_MANAGED_SETTINGS_DIR` | Every child. The removal covers the `env` of the request too. Measured on Linux and macOS. On Windows the removal is claustrum's own. |
| `CLAUDE_CODE_PROCESS_WRAPPER` | A child with a `launcher`, also from the `env` of the request. A child without one keeps it. Measured on Linux and macOS, claustrum's own on Windows. |
| `CLAUDE_SSH_PEER_CHECK` | The daemon removes it from its own environment at its start. On Windows that covers any letter case. A value in the `env` of the request reaches the child. Measured on `5fd08069` on Linux, macOS and Windows. If the removal fails, claustrum logs one WARN line of its own, and the children inherit it. |

The Go runtime variables `GODEBUG`, `GOGC`, `GOMAXPROCS`, `GOMEMLIMIT` and `GOTRACEBACK` reach the child with their values and at their places. `GOGC` and `GOMAXPROCS` are measured on Linux. claustrum treats the other three the same way.

### The identity markers (Linux and macOS)

The daemon starts the child through a short re-exec of its own binary, the exec-child trampoline. That step is `19f30c46` parity on Linux and macOS. It adds two markers, so a later daemon can tell the child from a process that reuses its pid:

- `CLAUDE_SSH_RUN_DIR=<run dir>`. The run dir is the folder of the socket path, cleaned and not made absolute. The socket `<R>/b/s.sock` gives `<R>/b`. A relative `s.sock` gives `.`. `<R>//run/./c1/rpc.sock` gives `<R>/run/c1`.
- `CLAUDE_SSH_CHILD=<pid>:<start>`. On Linux `<start>` is the start time of the child in `/proc` clock ticks. On macOS it is the start time from `ps` as a date text with one blank between its words, for example `Fri Oct 2 17:35:19 2026`. Only days 1 and 2 of a month are measured.

The trampoline adds no frame. A command that does not resolve to a runnable file gets the same error frame as without it. If the trampoline itself does not start, the daemon logs `[process.Manager] exec trampoline failed for <id> (<error>); starting it directly — a successor daemon will not be able to reap it` and starts the command directly. That is the Linux text, measured on Linux. The frame then names the command, for example `fork/exec <command>: permission denied` for a `cwd` that the daemon cannot enter. From the code, and not measured: if that direct start works, the reply is success, and the child gets `CLAUDE_SSH_RUN_DIR` and no `CLAUDE_SSH_CHILD`.

### The SSH agent hand-off (Linux and macOS)

This step is `f6010b97` parity, measured on Linux and macOS. The daemon advertises it as the feature `process.spawn.shellAgentSocket`. If the environment of the child has no `SSH_AUTH_SOCK` entry, the child gets the `SSH_AUTH_SOCK` that the login shell of the user exports.

- An entry that is there wins, even with an empty value, such as `"SSH_AUTH_SOCK":""` in `env`. `"disableShellAgentSocket":true` skips the step too.
- The daemon runs `<shell> -l -i -c …` at the first spawn that needs a socket. An executable `$SHELL` is the shell. Without one, the shell is the first of `/bin/zsh`, `/bin/bash` and `/bin/sh`. The run has a 4 s limit, and the daemon gives up on a shell that the kill cannot end at 5 s.
- The first spawn of a daemon can therefore wait for two shell runs: the `PATH` read and this one. With a login shell that sleeps 10 s, it answers after about 8 s.
- A socket that does not accept a connection in 250 ms is not handed on. The daemon keeps the answer and runs the shell again only 10 minutes after the last run. After 3 failed runs in a row it stops asking. A connection attempt that has not returned after 500 ms ends the hand-off for the life of the daemon.
- The step never fails a spawn. Each shell run and each give-up writes a `[shellenv]` line to the daemon log.

### Windows

- The child gets the `PATH` of the daemon as an entry named exactly `PATH`. claustrum reads it once, at the first spawn (from the code). No shell runs. An entry named `PATH` is replaced in place, else `PATH` is added. Then the `env` of the request comes.
- Of the entries whose names differ only in letter case, the child gets the last one (from the code, Go's `os/exec`). So a daemon entry named `Path` does not reach the child, and the child gets its value as `PATH`. With a daemon `Path`, a caller `Path` value is lost. A caller `PATH` or `path` value reaches the child with its own name.
- A daemon with no path entry gives the child none. A daemon `Path` with an empty value gives no `PATH` entry.
- There is no SSH agent hand-off and no identity marker. The capability and the parameter exist. These Windows rules are measured on a Windows VM, unless a rule says otherwise.
- The block of the child is in name order ([D23](../DIVERGENCES.md#d23)).

## A spawn with an `id` that is in use

A spawn with the `id` of a process that still runs succeeds and replaces the entry of the table. The first process gets no signal and keeps running. Then:

- `process.kill`, `process.stdin` and `process.reattach` of that `id` reach the second process only.
- `-stop` and `server.shutdown` end only the processes of the table, so the first process outlives the daemon. On Linux and macOS its record stays, and the next daemon start ends it.
- Each process keeps the connection that spawned it, under the same `processId`, and each counts its own `seq`. So a connection that sent both spawns gets two streams under one `processId`, with two `exit` frames (measured on Linux).
- A `process.reattach` of the `id` names the second process. It moves the stream of the second process and closes the connection that had it. A connection that still receives the frames of the first process goes on getting them (measured on Linux).

## Session supersede

A spawn whose `args` name a stream-json session of the Claude Code CLI ends every other running process of the same session. Its `args` must hold:

- `--input-format=stream-json`, `--output-format=stream-json` or a bare `stream-json`, and
- a session id from `--session-id <v>` or `--session-id=<v>`. Without one, it is `--resume <v>` or `--resume=<v>`, unless `--fork-session` is there too.

The argument after a bare `--session-id` or `--resume` is its value. From the code: the id has 1 to 128 characters. Each is a letter, a digit, `-`, `_`, `.` or `:`. The first is not `-`. Any other spawn supersedes nothing.

The daemon sends `SIGTERM` to the direct child of the old process and waits. After 3 s it sends `SIGKILL` to the group. The `exit` frame of the old process reaches its client with `"killedBy":"client"`. The pipe drain of that frame can take up to 5 s. Then the new process starts, and its reply comes after that `exit` frame. On Windows the old child ends at once, and its descendants live on. claustrum also runs two spawns of one session one after the other (claustrum's own).

A spawn with the `id` and the session of a process that runs supersedes nothing. It is the replace above.

Before it ends the old process, a session spawn tests its command. If the test fails, the old process gets no signal. These tests are measured on Linux, and the missing path on macOS too:

- The test is one `stat` of `command` as the request gives it. It is not joined with `cwd`. So a session spawn of `./tool` with a `cwd` that holds `tool` answers `fork/exec ./tool: no such file or directory`. The same spawn without session arguments starts.
- A bare name that the lookup does not find answers its lookup error.
- A missing or wrong `cwd` answers its own error first.
- A command that exists and does not start passes the test. A file of mode 0644, a folder and a script with a missing interpreter each end the old process first, and then the error comes.
- With a `launcher`, step 5 of [the launcher checks](#the-managed-launcher) is the test. So a missing, folder or not executable command ends no old process. A launcher that does not start ends the old process first, and then `-32005` comes.
- On Windows the lookup answers first. Then the daemon runs one `stat` of an absolute `command` only. A missing path that ends in `.exe` answers `fork/exec <path>: The system cannot find the file specified.` A relative path gets no `stat` (not measured on the reference).

A `-stop` can come while a spawn waits for the old process (measured on Linux and macOS). The spawn then gets no reply, and the new process never starts. The daemon sends `SIGKILL` to the group of the old process, and no connection gets its `exit` frame.

## The managed launcher

With `launcher`, the child runs as `<launcher...> <command> <args...>`. The launcher is the process, and the stream frames are its own. A client gets the launcher from [launcher.resolve](launcher-resolve.md) and passes it here. A spawn reads no managed settings, and the `CLAUDE_SSH_MANAGED_LAUNCHER` gate does not change it (measured on Linux).

The checks run in this order. They are measured on Linux. Step 5 is measured on macOS too, and so is the interpreter text of step 6:

1. On Windows any `launcher`, `[]` too, gets `-32004 … a launcher cannot be applied on a Windows host`.
2. A relative `command` gets `-32602 command must be an absolute path when a launcher is given`.
3. The checks of `launcher.resolve` run, with `command` in place of `cliPath`. A refusal gets `-32004 the managed launcher cannot be used: <reason>`. `[]` gets the text for no launcher.
4. The `cwd` test, with its usual error.
5. A `command` that is missing, a folder or not executable gets `fork/exec <command>: <reason>`, as without a launcher. The launcher does not run.
6. A launcher that does not start gets `-32005`. A missing `#!` interpreter, or a script with Windows CRLF line ends, gives the reason `its interpreter was not found (the program on its #! line, or the loader of an ELF binary; a script saved with Windows CRLF line endings fails this way)`. A 0755 text file with no `#!` gives `exec format error`.

claustrum's own order, not measured on the reference: step 1 comes before step 2, and step 5 comes before the launcher starts. A start failure other than those two gives its errno text. claustrum gives the step 6 frames through its trampoline, which is tested and not measured on a VM.

A started launcher logs `[process.Manager] Process <id> started, PID=<n>, command=<command> via launcher <argv0>`. The line names the first element of `launcher` only. A refusal writes no `[LauncherHandler]` line.

## A spawn during a shutdown

A spawn that reaches the start of its child after the shutdown began gets no reply. The daemon ends that child at once, also with `-keep-children`. The reference leaves such a child running ([CT-2](../DIVERGENCES.md#ct-2)).

## A later daemon start

On Linux and macOS a daemon that holds the [run-dir lock](../PROTOCOL.md#run-dir-lock-daemonlock) records each child in its run dir. A later daemon start on the same socket reads the records of a daemon that is gone. It ends each child whose process still matches its record, with `SIGTERM` and then `SIGKILL`. So a child that outlives its daemon can end at the next start. [Child records and the reap](../PROTOCOL.md#child-records-and-the-reap-linux-and-macos) has the rules. Windows records no child. A record that cannot be written does not fail the spawn. The reply is success, and the child still gets both identity markers.

## Differences by system

| | Linux | macOS | Windows |
|---|---|---|---|
| Process group | Own group. `KILL` reaches the group. | Own group. `KILL` reaches the group. | New process group, no Job Object. A kill ends the direct child only. |
| `PATH` of the child | The login-shell `PATH`. | The login-shell `PATH`. | The `PATH` of the daemon, as `PATH`. |
| SSH agent hand-off | Yes. | Yes. | No. |
| `launcher` | Allowed. | Allowed. | Refused with `-32004`. |
| Identity markers, records, reap | Yes, start in `/proc` ticks. | Yes, start as the `ps` date text. | None. |
| Order of the environment | As listed above. | As listed above. | Name order ([D23](../DIVERGENCES.md#d23)). |
| `signal` in the `exit` frame | Yes. | Yes. | Never. |

## Differences from the reference

Unless a rule names a build or says otherwise, the rules of this page are measured against `89cb6289`. The supersede comes from `4534d86`, the SSH agent hand-off from `f6010b97` and the trampoline from `19f30c46`, and `89cb6289` was measured for each again. The peer-check removal and the spawn during a shutdown are measured on `5fd08069`.

These entries of the divergence catalog apply to this method:

- [CT-1](../DIVERGENCES.md#ct-1): the optional `wantPid` parameter with `pid` and `startTime` in the result.
- [CT-2](../DIVERGENCES.md#ct-2): the `-keep-children` flag. A child that starts after the shutdown began is ended even with that flag.
- [D20](../DIVERGENCES.md#d20): the 50 ms wait in the reap before the `SIGKILL` of a group whose leader reads as gone.
- [D23](../DIVERGENCES.md#d23): on Windows the environment block of a child is in name order.

claustrum also removes the launcher variables on Windows, which is not measured on the reference. The measured cells behind each rule are in the tests and in the [reference build history](../REFERENCE-BUILDS.md).
