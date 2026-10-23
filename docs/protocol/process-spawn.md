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
| `-32603` | `exec: "<name>": executable file not found in $PATH` | A bare name that the lookup does not find. On Windows the text ends in `%PATH%`. |
| `-32603` | `fork/exec <command>: <reason>` | The command is missing, a folder or not executable, or the start fails. On Windows a missing file gives the reason `The system cannot find the file specified.` |
| `-32005` | `the managed launcher <path> could not be started: <reason>` | The launcher passes the checks and does not start. |

`<cwd>` and `<command>` are the values after the tilde expansion. A failure starts no child and makes no record. The daemon logs `[process.Manager] Failed to start process <id>: <message>`, where `<message>` is the text of the frame. An `Invalid params` error writes no line.

One failure ends another process first: a session spawn whose command exists and does not start. See [Session supersede](#session-supersede).

## What the child gets

- **Process group.** On Linux and macOS the child leads its own process group. A `KILL` from [process.kill](../PROTOCOL.md#processkill) reaches the whole group. On Windows the child starts in a new process group and in no Job Object. A kill there ends the direct child only, as [Windows child trees](../PROTOCOL.md#windows-child-trees) says.
- **Streams.** The stdout and the stderr of the child are pipes of the daemon. The daemon sends what it reads as stream frames to the connection that spawned the process, and it stores them for a replay. The stdin of the child is a pipe that [process.stdin](process-stdin.md) writes to.
- **The life of the process.** A close of its connection does not end the process. `-stop` and `server.shutdown` end the processes of the table. With `-keep-children` they leave them alive ([CT-2](../DIVERGENCES.md#ct-2)).

## The environment of the child

The child gets the environment of the daemon with the `env` of the request merged over it. On Linux and macOS the entries come in this order:

1. The entries of the daemon, with `CLAUDE_SSH_DAEMON_CHILD=1` last. A caller key that the daemon has replaces that entry in place. An empty caller value of `CLAUDE_SSH_DAEMON_CHILD` stays empty.
2. The new caller keys. Two or more of them come in no fixed order. The reference gives a rotation of the request order, and claustrum does too.
3. `SSH_AUTH_SOCK` from the hand-off below. Without that step there is no entry here.
4. `CLAUDE_SSH_RUN_DIR` and then `CLAUDE_SSH_CHILD`, the two identity markers below. A caller value of either does not reach the child.

After a good read, the `PATH` entry of the child is the login-shell `PATH` of the user. A `PATH` in `env` wins over it. The read runs at the first spawn, and that spawn waits for it. See [-serve](../PROTOCOL.md#-serve-run-the-daemon). The value never changes how the daemon looks up `command`.

The daemon removes these names:

| Name | Removed from |
|---|---|
| `CLAUDE_RPC_TOKEN` | Every child. |
| `CLAUDE_SSH_MANAGED_LAUNCHER`, `CLAUDE_SSH_E2E_MANAGED_SETTINGS_DIR` | Every child. The removal covers the `env` of the request too. |
| `CLAUDE_CODE_PROCESS_WRAPPER` | A child with a `launcher`, also from the `env` of the request. A child without one keeps it. |
| `CLAUDE_SSH_PEER_CHECK` | The daemon removes it from its own environment at its start, in any letter case. A value in the `env` of the request reaches the child. If the removal fails, the daemon logs one WARN line, and the children inherit it. |

The Go runtime variables `GODEBUG`, `GOGC`, `GOMAXPROCS`, `GOMEMLIMIT` and `GOTRACEBACK` reach the child with their values and at their places.

### The identity markers (Linux and macOS)

The daemon starts the child through a short re-exec of its own binary, the exec-child trampoline. That step adds two markers, so a later daemon can tell the child from a process that reuses its pid:

- `CLAUDE_SSH_RUN_DIR=<run dir>`. The run dir is the folder of the socket path, cleaned and not made absolute. The socket `<R>/b/s.sock` gives `<R>/b`. A relative `s.sock` gives `.`. `<R>//run/./c1/rpc.sock` gives `<R>/run/c1`.
- `CLAUDE_SSH_CHILD=<pid>:<start>`. On Linux `<start>` is the start time of the child in `/proc` clock ticks. On macOS it is the start time from `ps` as a date text with one blank between its words, for example `Fri Oct 2 17:35:19 2026`.

The trampoline adds no frame. A command that does not resolve to a runnable file gets the same error frame as without it. If the trampoline itself does not start, the daemon logs `[process.Manager] exec trampoline failed for <id> (<error>); starting it directly — a successor daemon will not be able to reap it` and starts the command directly. The frame then names the command, for example `fork/exec <command>: permission denied` for a `cwd` that the daemon cannot enter. From the code, and not measured: if that direct start works, the reply is success, and the child gets `CLAUDE_SSH_RUN_DIR` and no `CLAUDE_SSH_CHILD`.

### The SSH agent hand-off (Linux and macOS)

The daemon advertises this step as the feature `process.spawn.shellAgentSocket`. If the environment of the child has no `SSH_AUTH_SOCK` entry, the child gets the `SSH_AUTH_SOCK` that the login shell of the user exports.

- An entry that is there wins, even with an empty value, such as `"SSH_AUTH_SOCK":""` in `env`. `"disableShellAgentSocket":true` skips the step too.
- The daemon runs `<shell> -l -i -c …` at the first spawn that needs a socket. An executable `$SHELL` is the shell. Without one, the shell is the first of `/bin/zsh`, `/bin/bash` and `/bin/sh`. The run has a 4 s limit, and the daemon gives up on a shell that the kill cannot end at 5 s.
- The first spawn of a daemon can therefore wait for two shell runs: the `PATH` read and this one. With a login shell that sleeps 10 s, it answers after about 8 s.
- A socket that does not accept a connection in 250 ms is not handed on. The daemon keeps the answer and runs the shell again only 10 minutes after the last run. After 3 failed runs in a row it stops asking. A connection attempt that has not returned after 500 ms ends the hand-off for the life of the daemon.
- The step never fails a spawn. Each shell run and each give-up writes a `[shellenv]` line to the daemon log.

### Windows

- The child gets the `PATH` of the daemon, read once at the first spawn, as an entry named exactly `PATH`. No shell runs. An entry named `PATH` is replaced in place, else `PATH` is added. Then the `env` of the request comes.
- Of the entries whose names differ only in letter case, the child gets the last one. So a daemon entry named `Path` does not reach the child, and the child gets its value as `PATH`. With a daemon `Path`, a caller `Path` value is lost. A caller `PATH` or `path` value reaches the child with its own name.
- A daemon with no path entry gives the child none. A daemon `Path` with an empty value gives no `PATH` entry.
- There is no SSH agent hand-off and no identity marker. The capability and the parameter exist.
- The block of the child is in name order ([D23](../DIVERGENCES.md#d23)).

## A spawn with an `id` that is in use

A spawn with the `id` of a process that still runs succeeds and replaces the entry of the table. The first process gets no signal and keeps running. Then:

- `process.kill`, `process.stdin` and `process.reattach` of that `id` reach the second process only.
- `-stop` and `server.shutdown` end only the processes of the table, so the first process outlives the daemon. On Linux and macOS its record stays, and the next daemon start ends it.
- Each process keeps the connection that spawned it, under the same `processId`, and each counts its own `seq`. So a connection that sent both spawns gets two streams under one `processId`, with two `exit` frames.
- A `process.reattach` of the `id` names the second process. It moves the stream of the second process and closes the connection that had it. A connection that still receives the frames of the first process goes on getting them.

## Session supersede

A spawn whose `args` name a stream-json session of the Claude Code CLI ends every other running process of the same session. Its `args` must hold:

- `--input-format=stream-json`, `--output-format=stream-json` or a bare `stream-json`, and
- a session id from `--session-id <v>` or `--session-id=<v>`. Without one, it is `--resume <v>` or `--resume=<v>`, unless `--fork-session` is there too.

The argument after a bare `--session-id` or `--resume` is its value. From the code: the id has 1 to 128 characters. Each is a letter, a digit, `-`, `_`, `.` or `:`. The first is not `-`. Any other spawn supersedes nothing.

The daemon sends `SIGTERM` to the direct child of the old process and waits. After 3 s it sends `SIGKILL` to the group. The `exit` frame of the old process reaches its client with `"killedBy":"client"`. The pipe drain of that frame can take up to 5 s. Then the new process starts, and its reply comes after that `exit` frame. claustrum also runs two spawns of one session one after the other.

A spawn with the `id` and the session of a process that runs supersedes nothing. It is the replace above.

Before it ends the old process, a session spawn tests its command. If the test fails, the old process gets no signal:

- The test is one `stat` of `command` as the request gives it. It is not joined with `cwd`. So a session spawn of `./tool` with a `cwd` that holds `tool` answers `fork/exec ./tool: no such file or directory`. The same spawn without session arguments starts.
- A bare name that the lookup does not find answers its lookup error.
- A missing or wrong `cwd` answers its own error first.
- A command that exists and does not start passes the test. A file of mode 0644, a folder and a script with a missing interpreter each end the old process first, and then the error comes.
- On Windows the lookup answers first. Then the daemon runs one `stat` of an absolute `command` only. A missing path that ends in `.exe` answers `fork/exec <path>: The system cannot find the file specified.` A relative path gets no `stat` (not measured on the reference).

A `-stop` can come while a spawn waits for the old process. The spawn then gets no reply, and the new process never starts. The daemon sends `SIGKILL` to the group of the old process, and no connection gets its `exit` frame.

## The managed launcher

With `launcher`, the child runs as `<launcher...> <command> <args...>`. The launcher is the process, and the stream frames are its own. A client gets the launcher from [launcher.resolve](../PROTOCOL.md#launcherresolve) and passes it here. A spawn reads no managed settings, and the `CLAUDE_SSH_MANAGED_LAUNCHER` gate does not change it.

The checks run in this order:

1. On Windows any `launcher`, `[]` too, gets `-32004 … a launcher cannot be applied on a Windows host`.
2. A relative `command` gets `-32602 command must be an absolute path when a launcher is given`.
3. The checks of `launcher.resolve` run, with `command` in place of `cliPath`. A refusal gets `-32004 the managed launcher cannot be used: <reason>`. `[]` gets the text for no launcher.
4. The `cwd` test, with its usual error.
5. A `command` that is missing, a folder or not executable gets `fork/exec <command>: <reason>`, as without a launcher. The launcher does not run.
6. A launcher that does not start gets `-32005`. A missing `#!` interpreter, or a script with Windows CRLF line ends, gives the reason `its interpreter was not found (the program on its #! line, or the loader of an ELF binary; a script saved with Windows CRLF line endings fails this way)`. A 0755 text file with no `#!` gives `exec format error`.

claustrum's own order, not measured on the reference: step 1 comes before step 2, and step 5 comes before the launcher starts. A start failure other than those two gives its errno text.

A started launcher logs `[process.Manager] Process <id> started, PID=<n>, command=<command> via launcher <argv0>`. The line names the first element of `launcher` only. A refusal writes no `[LauncherHandler]` line.

## A spawn during a shutdown

A spawn that reaches the start of its child after the shutdown began gets no reply. The daemon ends that child at once, also with `-keep-children`. The reference leaves such a child running ([CT-2](../DIVERGENCES.md#ct-2)).

## Child records (Linux and macOS)

A daemon that holds the [run-dir lock](../PROTOCOL.md#run-dir-lock-daemonlock) records each child in `<run dir>/children/<pid>.json`, on every socket shape. The child needs a pid of 2 or more and a start time that the daemon can read. A later daemon reads the records at its start and ends the children that a dead daemon left. Windows writes no record and reaps none.

The record is one JSON object with the members in this order:

```json
{"pid":<int>,"node":"<boot-id>/pid:[<inode>]","host":"machine-id:<hex>","instance":"<daemon instance id>","daemonPid":<int>,"daemonStart":"<start>","argv0":"<child argv0>","start":"<start>","at":<epoch-ms>}
```

`daemonStart` and `start` are strings. They hold clock ticks on Linux and the date text of `CLAUDE_SSH_CHILD` on macOS. On macOS the node is the boot-session UUID, and the host is the hostname. With a `launcher`, `argv0` is the launcher, and a `program` member holds `command`, after `argv0`. claustrum writes no `program` without a launcher (not measured on the reference).

- The daemon writes the record through a temp file `children/.rec-<pid>.json.<12 characters>` and a rename.
- The end of the child removes its record. That comes before the pipe drain, so it does not wait for the `exit` frame.
- A graceful shutdown removes the records of the children that it ends. claustrum waits 1 s at most for them. With `-keep-children` it removes the records of the children that stay alive, so the next daemon leaves them alone.
- A record stays after a kill of the daemon, and for a process whose `id` a later spawn took.
- The daemon holds its run dir open. After a rename of the run dir, a record goes under the new name.
- No record goes through a `children` that is not a real folder inside the run dir. A symbolic link to a folder inside the run dir is the exception for the remove at the end of the child.

## The reap at the next start (Linux and macOS)

At a `-serve` start, after the run-dir lock and before the bind, the daemon reads the records:

- It walks `children` in directory order and handles the first 128 names that end in `.json`. One more name ends the walk with a line.
- It sends `SIGTERM` to 64 groups at most. The others keep their records for the next start.
- Only a regular file is a record. A link, a FIFO or a folder with a `.json` name is removed, with no signal. The target of a link stays.
- The daemon reads at most 4096 bytes of a record. A record whose first 4096 bytes are not one JSON value is removed with no signal.
- If `children` is not a real folder, the daemon reads, signals and removes nothing.

A record of a daemon that is alive stays. So does a record from another machine, for 30 days (from the code). A record from an earlier boot of this machine, and a record of a dead pid, are removed. For a record of a dead daemon on this machine, the daemon tests the process. If it still is the recorded child, its group gets `SIGTERM`. The test has four parts:

- It started at the recorded `start`, so the pid is not reused. A `start` with two blanks before a one-digit day does not match a macOS text.
- It leads its own process group.
- It runs `argv0`. With a `program` member, it can also run `program`, or hold `program` as one whole argument. On Linux the program is the first argument of `/proc/<pid>/cmdline`, and a name with the same last path component matches. On macOS the daemon reads the command text of `ps`, which cuts words at blanks. A `program` with a blank therefore never matches an argument there.
- `CLAUDE_SSH_RUN_DIR` names this run dir, and `CLAUDE_SSH_CHILD` names the pid and start of the process itself.

A process that the daemon cannot inspect now keeps its record and gets no signal. The owner of the record file is not tested.

After the `SIGTERM` the daemon polls for 2 s. At each poll a group takes the whole test again. A group that fails it gets nothing more. A group that still passes after 2 s gets `SIGKILL`. After the last group is done, the records of all signalled groups go together. A leader that reads as gone is read again after 50 ms before the `SIGKILL` of its group ([D20](../DIVERGENCES.md#d20)).

## Log lines

claustrum prints a level tag before each line. The reference prints none.

| Event | Line |
|---|---|
| A start | `[process.Manager] Process <id> started, PID=<n>, command=<command>` (from the code) |
| An exit | `[process.Manager] Process <id> exited with code <n>`, then `, terminated by <signal>` for a signal and `, signalled at client request` or `, signalled at shutdown request` for a kill by the daemon |
| The drain limit | `[process.Manager] Process <id>: pipe drain grace expired (grandchild holding stdio?); force-closing` |
| A supersede | `[process.Manager] process <new id> supersedes <old id> for session <key>: died=<bool> escalated=<bool> alreadyExited=<bool>` (from the code), after the exit line of the old process |
| `children` not a folder | `[process.Registry] <run dir>/children is not a directory; recording nothing`, or `reaping nothing` at a start |
| A record write fails | `[process.Registry] cannot record child <pid>: <error>` |
| A record remove fails | `[process.Registry] cannot remove record of child <pid>: <error>` |
| Over 128 records | `[process.Registry] more than 128 records in <run dir>/children; leaving the rest for the next start` |
| Over 64 groups | `[process.Registry] <n> more orphaned group(s) recorded in <run dir>/children than the 64 one start ends; leaving those for the next start` |
| A record dropped | `[process.Registry] record <pid>.json: pid <pid> is not a child to end (<reason>); dropping the record, signalling nothing` |
| A group ended | `[process.Registry] ending orphaned process group <pgid> recorded by daemon instance "<instance>" (pid <daemon pid>): SIGTERM` |
| The grace ran out | `[process.Registry] group <pgid> outlived SIGTERM for 2s: SIGKILL` |
| A group fails the test | `[process.Registry] group <pgid> no longer verifies (<reason>); nothing more is sent to it` |
| The reap summary | `[daemon] serve: predecessor's children — <n> orphaned group(s): <a> ended on SIGTERM, <b> on SIGKILL, <c> survived; <d> stale record(s) dropped, <e> kept` |

The exit line is a race at a shutdown, on the reference and on claustrum. It can be missing. A reap that sent no `SIGTERM` and kept no record writes no summary.

The `<reason>` texts are these:

- `it does not run the recorded program "<argv0>"`, with `and neither runs nor was handed "<program>"` after it for a record with `program`
- `it does not lead its own process group`
- `it did not start at the recorded "<start>": the pid has been reused`
- `it was not started by a daemon of this run dir: no CLAUDE_SSH_RUN_DIR names it`
- `it belongs to a daemon of another run dir: its CLAUDE_SSH_RUN_DIR names a different one`
- `it was not started by a daemon itself: its CLAUDE_SSH_CHILD does not name its own pid and start (something a child started)`
- `environment unreadable: <error>`, `pid namespace unreadable: <error>` and `stat unreadable: <error>`

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

These entries of the divergence catalog apply to this method:

- [CT-1](../DIVERGENCES.md#ct-1): the optional `wantPid` parameter with `pid` and `startTime` in the result.
- [CT-2](../DIVERGENCES.md#ct-2): the `-keep-children` flag. A child that starts after the shutdown began is ended even with that flag.
- [D20](../DIVERGENCES.md#d20): the 50 ms wait in the reap before the `SIGKILL` of a group whose leader reads as gone.
- [D23](../DIVERGENCES.md#d23): on Windows the environment block of a child is in name order.

claustrum also removes the launcher variables on Windows, which is not measured on the reference. The measured cells behind each rule are in the tests and in the [reference build history](../REFERENCE-BUILDS.md).
