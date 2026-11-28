# process.spawn

`process.spawn` starts a child process as the user of the daemon and streams its output to the connection that sent the request. [process.reattach](process-reattach.md) takes the process over.

Treat the socket and the token as shell access. See [`SECURITY.md`](https://github.com/schubydoo/claustrum/blob/main/SECURITY.md).

An untagged rule on this page is measured against build `89cb6289`. The removal of `CLAUDE_SSH_PEER_CHECK` is measured against `5fd08069`.

## Request

```json
{"id": "p1", "command": "/usr/bin/env", "args": ["-0"], "cwd": "/repo", "env": {"FOO": "bar"}}
```

| Parameter | Required | Meaning |
|---|---|---|
| `id` | yes | Any string. The id of the process in each later `process.*` request and in each stream frame. |
| `command` | yes | The program. A bare name is looked up on the `PATH` of the daemon. With `launcher`, it must be an absolute path. With `launcherSuppliesCli`, it is `""`. |
| `args` | no | The arguments after the program, as strings. |
| `cwd` | no | The working directory of the child. A leading `~` is expanded, as in [Tilde expansion](../PROTOCOL.md#tilde-expansion-in-path-params). Without it, the child gets the working directory of the daemon. |
| `env` | no | An object of string values, merged over the daemon environment. See [The environment of the child](#the-environment-of-the-child). |
| `disableShellAgentSocket` | no | `true` skips the SSH agent hand-off for this spawn. `null` counts as `false`. |
| `launcher` | no | An array of strings that names a managed launcher. See [The managed launcher](#the-managed-launcher). Absent or `null` means no launcher. |
| `launcherCheck` | no | With `launcher`, run [the file check](launcher-resolve.md#the-file-check-build-c9125eb8) on argv[0] (`c9125eb8`). |
| `launcherSuppliesCli` | no | `true` with a `launcherCheck`: the launcher runs Claude Code itself (`c9125eb8`). See [A launcher check](#a-launcher-check-build-c9125eb8). `null` counts as absent for this member, `launcherCheck` and `envOnlyIfSent` (claustrum's own, not measured). |
| `envOnlyIfSent` | no | A list of names, with `launcherSuppliesCli` (`c9125eb8`). Such names reach the child only from `env`, and an empty list counts as present (claustrum's own, not measured). |
| `noLoginShell` | no | Accepted, and the spawn succeeds (`c9125eb8`, measured on Linux and Windows). It changes nothing, and a value of any type passes (claustrum's own, not measured). |
| `unattendedSeconds` | no | A whole number of seconds (`87375cc7`). See [The unattendedSeconds member](#the-unattendedseconds-member-build-87375cc7). |
| `hostCreds` | no | An object with an `env` object of strings (`13047a51`). See [The hostCreds member](#the-hostcreds-member-build-13047a51). |

### Members that turn on a feature

| Parameter | Required | Meaning |
|---|---|---|
| `linkBound` | no | See [its page](process-link-bound.md). |
| `gitRead` | no | See [its page](process-git-read.md). |
| `sessionFoldersOff` | no | `true` turns off the check of [session folders](git-rules-session-folders.md) while the request runs. |
| `cliTempRoot` | no | `true` asks for [a private temp folder](#the-private-temp-folder-build-b3081b51). A text or an object gets `Invalid params` (Linux). Other cases are claustrum's own. |
| `cliTempRootEnd` | no | `true` asks the daemon to end the child on Linux and macOS when [its private temp folder is lost](#a-lost-folder-build-4c111e86) (`4c111e86`). Any other value counts as absent (claustrum's own). |
| `wantPid` | no | `true` adds `pid` and `startTime` to the result. An addition of claustrum, see [CT-1](../DIVERGENCES.md#ct-1). Any other value counts as absent. |

### The unattendedSeconds member (build `87375cc7`)

An accepted value above 0 arms [the unattended end](#the-unattended-end-build-87375cc7). The refusals:

- A text, a float or `null`: `Invalid params` (measured on Linux and Windows, macOS for a text). Other forms that are no whole number in int64, such as `1e3`, `1.0` or a larger number, get it too (claustrum's own).
- A negative value: `-32602` `unattendedSeconds refused: it is negative, or came with pty, linkBound, gitRead or cliTempRootEnd, or for a Claude Code that was not started with --no-session-persistence`, reason `other` (Linux, macOS and Windows). A refusal logs the failed-start line (Linux and macOS). `Invalid params` logs none (Linux).
- A positive value with a `pty` object with a key, a `linkBound` object, `gitRead`, `cliTempRootEnd`, or a Claude Code start without `--no-session-persistence`: the same refusal (Linux, Windows, macOS for `gitRead`, `cliTempRootEnd` and a Claude Code start). A Claude Code start has the shape of [the session folders](git-rules-session-folders.md#which-spawn-makes-a-session-folder): a command named `ccd-cli` or in a folder of that name, or a `stream-json` arg on a command other than a bare `git` (Linux). A bare `ccd-cli` with no args counts, and `ccd-cli.exe`, `ccd-cli-x` and `claude` do not (Linux and macOS). On Windows `ccd-cli.exe` does not count. A launcher counts (Linux, macOS and Windows).
- A `pty` error comes first (Linux and Windows, macOS for `true`), except the Windows refusal of a valid key, which comes after this test on `87375cc7`. `0` and `1000000000000` pass (Linux, and `99999999999` on Windows), and `0` passes this test with each of these members (Linux).
- The test comes after the `id` and `command` tests and before `linkBound` with a `launcher` (Linux). The place of the type test, of this test after the test of the launcher members, and of this test before every step of [the managed launcher](#the-managed-launcher) is claustrum's own.

### The unattended end (build `87375cc7`)

The rules are measured on Linux, macOS and Windows against `87375cc7`, unless a rule names a system.

The daemon ends an armed child that had no connection for a time. The spawn logs this line right after its started line. `<time>` is a Go duration text, such as `1m0s`.

```
[process.Manager] Process <id> is to be ended once no connection has been attached to it for <time>
```

| `unattendedSeconds` | Time |
|---|---|
| 1 to 60 | 60 s |
| 61 to 604799 | That number of seconds |
| 604800 or more | 168 h |

- The close of the last connection starts the clock. A client that dies counts at once. A [reattach](process-reattach.md) clears the clock.
- A check runs each quarter of the time, counted from the spawn. Closes at 1, 20 and 37 s end at 75, 90 and 105 s, and 61 ends at 76.25 s. The end comes at the first check after the whole time.
- Linux and macOS: `SIGTERM` to the child only. The daemon waits until the child leaves, at most 7 s. Then `SIGKILL` goes to its group, also after the child left. No `SIGHUP` goes out.
- Windows: the `SIGTERM` is the end of the [job](process-kill.md#windows-child-trees). The child leaves at once with code 1.
- One of these lines comes before the exited line, which ends in `asked to end by this daemon (unattended)`. The stayed line is measured on Linux and macOS.
- A refused group `SIGKILL` gives the third line in place of the second (measured on Linux). On macOS, and after an error other than `EPERM`, the line is claustrum's own, not measured.
- A later reattach gets the `exit` frame with `"killedBy":"unattended"`.
- A child that a `process.kill` ended, an own exit and `server.shutdown` give no end and no line. The shutdown ends the child as usual.

```
[process.Manager] Process <id>: no connection attached for <time>: it left at SIGTERM
[process.Manager] Process <id>: no connection attached for <time>: it stayed at SIGTERM, and its group was killed
[process.Manager] Process <id>: no connection attached for <time>: it stayed at SIGTERM, and its group could not be killed
```

#### A refused `SIGTERM`

A `SIGTERM` refused with `EPERM` logs this line once for each child, at its first refusal (87375cc7, measured on Linux and macOS with the value 60, two refused children on Linux only). A reattach and a new detach do not log it again. The four rules below have the same scope.

```
[process.Manager] Process <id>: no connection attached for <time>, but it could not be sent SIGTERM
```

- The daemon sends `SIGTERM` again at each later check while no connection holds the child. It sends no `SIGKILL` while the `SIGTERM` is refused.
- During a reattach it sends nothing. After a new detach, the next try comes at the first check after the whole time.
- Once a `SIGTERM` goes through, the usual end runs, with its lines (measured for a child that stays on Linux and one that leaves on macOS).
- A child that exits by itself ends the checks, with only its exited line.

#### claustrum's own rules of the end

These rules are not measured.

- A value from 2 to 59, and 1 on Windows, gets a check each 15 s, a quarter of its 60 s. A check comes at most each 60 s, and no end row of this member has a value above 240.
- An `<id>` of more than 8 bytes is [cut](../DAEMON.md#a-long-id-in-a-line) in these lines.
- On Linux and macOS, a `SIGTERM` error other than `EPERM` or `ESRCH` stops the end. It logs no line and sends no `SIGKILL`. That child gets no later end.
- A spawn or a reattach on a closed connection starts the clock.
- Once the shutdown began, no end starts and the end sends no more signals.
- A reattach or a shutdown after the check and before the `SIGTERM` ends nothing.
- A child that survives a `process.kill` still gets the end. It keeps `"killedBy":"client"`, so its exited line ends in `(client)`.
- On Windows a child that exits by itself just before its reap can still get `"killedBy":"unattended"` and the left line. No signal leaves its handle or its job.

A launcher spawn without `--no-session-persistence` gets the refusal above (measured on Linux, macOS and Windows). With that arg, on Linux also with a `launcherCheck` object, claustrum arms the end, and a macOS `launcherCheck` gets its refusal (claustrum's own, not measured). On Windows every launcher is refused.

### The hostCreds member (build `13047a51`)

On Linux a Claude Code child reads the `env` of `hostCreds` from a pipe. Those names do not reach its environment (measured).

- `Invalid params` with no `data`: a `hostCreds` that is `null`, a text or a list, or an `env` value that is a number (Linux, macOS and Windows). An `env` that is a text or a list, or an `env` value of `null`, gets it too (Linux and macOS, and on Windows claustrum's own, not measured). Other members, such as `unless`, change nothing (Linux and macOS).
- macOS and Windows refuse a `hostCreds` of the right form with `-32602` `hostCreds is not supported on this host`, reason `other`. No child starts (macOS). The failed-start line on Windows is claustrum's own, not measured.

#### The Linux refusals

Each is `-32602` with reason `other` and logs the failed-start line, as on macOS.

| Shape | Text |
|---|---|
| `{}` or an empty `env` | `hostCreds refused: its env is empty` |
| An empty name, or a name with `=` or NUL | `hostCreds refused: a name is empty or holds = or NUL` |
| A value of 70000 bytes | `hostCreds refused: its names and values come to over 32 KiB` |
| `gitRead` or `linkBound` | `hostCreds refused: it came with pty, linkBound, gitRead or launcher` |
| A name that the spawn's `env` holds too | `hostCreds refused: a name is also in the spawn's env` |
| A program that is not Claude Code | `hostCreds refused: the program is not Claude Code, which alone reads it` |
| A spawn `env` without both names at `1` | `hostCreds refused: the spawn's env does not set CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST and CLAUDE_CODE_SDK_HAS_HOST_AUTH_REFRESH to 1` |

- Names with a space or a leading digit pass, and so does a NUL in a value. The names `CLAUDE_CODE_HOST_CREDS_FILE` and `LD_PRELOAD` pass too.
- A `pty` object with no key keeps its own text, and `"launcher":true` gets `Invalid params`. A `gitRead` spawn of `git` gets the combine text, not the `gitRead` text.
- A command named `ccd-cli` counts as Claude Code. claustrum uses the shape of [the unattendedSeconds test](#the-unattendedseconds-member-build-87375cc7) for other commands (claustrum's own, not measured).
- The child gets `CLAUDE_CODE_HOST_CREDS_FILE=/proc/self/fd/99`, the read end of a pipe. It reads `{"env":{…},"expiresAt":null,"pid":…,"procStart":…}` with the names in name order, and then the end.
- claustrum's own, not measured: the values of `pid` and `procStart`, the pid of the child and its start in clock ticks. So is an `env` of `null`, which counts as empty. So is the combine text for a keyed `pty` object or a `launcher` list. So is the exact 32 KiB bound, on the length of each name plus its value. So are the order of the other tests and the place of the test in the spawn. So are the place of the variable, the replace of a caller entry of that name and the bytes of the JSON text.

## Response

```json
{"success": true}
```

With `"wantPid": true` the result is `{"success":true,"pid":<int>,"startTime":<number>}`. `pid` is the OS pid of the child. `startTime` is the daemon's wall clock at the spawn, in epoch seconds.

`startTime` is an opaque token that detects a reused pid, not an OS start time.

On Windows a failed or pending (`4c111e86`, measured on Windows) [start test](process-kill.md#the-start-test-of-the-jobs) adds `"outsideJob":true` after `success`.

With `"cliTempRoot": true` on Linux and macOS the result can carry `cliTempRoot` after `success`. See [The private temp folder](#the-private-temp-folder-build-b3081b51).

After the result, the output comes as [stream notifications](streams.md#stream-notifications) with the `id` as `processId`. The last one is the `exit` frame.

### The group of an exited launcher (build `87375cc7`)

On Linux and macOS, `87375cc7` ends what a launcher left in its process group after the launcher ends, also after a kill of the launcher by another process or a `process.kill` (measured). A spawn with no launcher gets no such end (measured on Linux and macOS), except [a Claude Code](#the-group-of-an-exited-claude-code-build-13047a51) on `13047a51`. claustrum has no such end on Windows, and `87375cc7` lists no `launcher.exit_ends_group` there (measured).

**Linux** (measured):

- The daemon sees the exit without a reap, so the leader holds the group number. The reap comes after the group `SIGKILL`.
- Pipes at their end: one group `SIGKILL` at once, with no `SIGTERM` and no line.
- Pipes still open 200 ms after the exit: a group `SIGTERM`, also with no member in the group. The group `SIGKILL` follows once the pipes are at their end, at the latest 1.5 s after the `SIGTERM`.
- The 5 s pipe drain of the `exit` frame counts from the exit of the launcher, not from its reap.
- A shutdown inside this wait, before or after the `SIGTERM`: a group `SIGKILL` at once, then the reap. No `SIGTERM` follows. In some runs `87375cc7` sends a second group `SIGKILL` to the same group a few ms after the first (measured on Linux). claustrum sends one. The cleanup line counts the group as ended, except after a member that already ended on the `SIGTERM` or a refused `SIGKILL`. The record goes.
- A stream-json launcher that the shutdown asked to end with `SIGTERM` gets the steps above after its exit. A group `SIGKILL` of the shutdown ends them.
- A running leader that took a group `SIGKILL` of the shutdown gets one more right after its exit (`87375cc7` and `13047a51`, measured on Linux). It comes before the reap, with no `SIGTERM`. `13047a51` sends one more also after the 1.5 s `SIGKILL` of a Claude Code (measured on Linux). That it comes before the reap there, and the kill for a launcher after the 1.5 s `SIGKILL`, are claustrum's own, not measured.

#### The lines of the end on Linux

- On Linux, after the group `SIGKILL` of this end, a `SIGTERM` of the end logs the first line below. A shutdown in the wait logs none. In some runs with the second `SIGKILL` above, `87375cc7` logs it (measured on Linux). After a client `KILL` that ended the wait the line still goes (claustrum's own, not measured).
- On Linux a refused group `SIGKILL` of this end logs the second line below, before the first (`87375cc7`, measured). One of a shutdown logs none. On macOS claustrum logs no such line (claustrum's own, not measured).

```
[process.Manager] Process <id> exited and its output was not at its end 200ms later: what was left of its process group, if anything, was sent SIGTERM ahead of the SIGKILL
[process.Manager] the process group of pid <pid>, which has exited, was not sent SIGKILL: operation not permitted
```

The first line is that of `13047a51` (measured on Linux).

#### The end on macOS (measured)

- The daemon reaps the leader first. Then it probes with `kill(-pgid, 0)`. After 0 it sends `kill(-pgid, SIGKILL)`. After `ESRCH` it sends none. After `EPERM`, `87375cc7` still sends it, and claustrum sends none (claustrum's own). No `SIGTERM` goes out.
- A leader with no member gets the probe (`ESRCH`) and no `SIGKILL`.
- A leader that exits during a shutdown gets these steps too, also a stream-json launcher that the shutdown asked to end.
- After the reap the kernel can reuse the number. In a staged run, `87375cc7` killed an unrelated group that way. claustrum follows this, an accepted risk.

#### `CLAUDE_SSH_LEAVE_GROUPS`

This holds on Linux and macOS (measured). The daemon reads it once at its start. The value `1` turns the end off, also [the end of a Claude Code](#the-group-of-an-exited-claude-code-build-13047a51). The daemon then logs one line before its listening line and lists no `launcher.exit_ends_group` and no `process.cli_exit_ends_group`. `0`, `yes` and empty leave it on. Each of these values leaves the environment of the children. Another value also leaves it on (claustrum's own, not measured).

```
[daemon] what an exited launcher or Claude Code leaves in its process group lives on: CLAUDE_SSH_LEAVE_GROUPS is set
```

That is the line of `13047a51` (measured on Linux and macOS).

claustrum's own rules of this end, not measured:

- A group number of 1 or less, the daemon's pid and the daemon's group get no signal.
- After the temp-root or unattended end killed the group, this end sends nothing. It sends nothing either after the shutdown sent its `SIGKILL` to the group of an already held leader. On macOS, after any group `SIGKILL` of the shutdown, it sends nothing either.
- After the exit of the leader, this end, the 1.5 s step of a shutdown and a client `KILL` share one claim on the group `SIGKILL`. A client `KILL` that passed its test just before the exit can still send a second one. A refused client or 1.5 s `KILL` gives the claim back, unless a mark of the end came meanwhile.
- With `-keep-children` a shutdown that began before the last test of the Linux end gets no group `SIGKILL` from it. A signal that passed its test just before the shutdown still goes.
- On Linux a request in the wait treats the child as exited, so its `exit` frame gets no `killedBy` from that request. A `process.kill` with `KILL` there ends the group at once.

### The group of an exited Claude Code (build `13047a51`)

On Linux and macOS `13047a51` ends the group of a Claude Code spawn with no launcher, as it ends [the group of a launcher](#the-group-of-an-exited-launcher-build-87375cc7) (measured). `87375cc7` does not (measured). claustrum follows `13047a51`.

- A Claude Code is a command named `ccd-cli` (measured on Linux and macOS). A command in a folder of that name counts too, such as `<dir>/ccd-cli/2.0.0` (measured on Linux). On macOS that shape is claustrum's own, not measured. A plain name gets no such end (measured). Other name shapes are claustrum's own, not measured.
- Linux: a member that ignores the `SIGTERM` gets the `SIGKILL` 1 to 3 s after it (measured). claustrum sends it at 1.5 s.
- A `pty` spawn gets no such end from claustrum. `87375cc7` and `13047a51` end what it leaves in its group, also with `CLAUDE_SSH_LEAVE_GROUPS=1` (measured on macOS). claustrum keeps that member.
- A Claude Code with `gitRead` gets no such end (claustrum's own, not measured). Windows has no such end, and `13047a51` lists no `process.cli_exit_ends_group` there (measured).

## Errors

A failure is a JSON-RPC error. There is no `errorCode` member.

| Code | Text | Cause |
|---|---|---|
| `-32602` | `Invalid params` | No `params`, or a member that the method reads has the wrong type. A `launcher` that is not an array, or that holds an element that is not a string, gets it too. A `null` element becomes an empty argument. See [the rules for `params`](../PROTOCOL.md#params-presence-and-typing). |
| `-32602` | `Process ID is required` | `id` is absent or empty. |
| `-32602` | `invalid process.spawn: launcherCheck needs a launcher; launcherSuppliesCli needs a launcherCheck and an empty command; orManagedFolder can't be set with sha256 or ownerUid` | A `launcherCheck` with no `launcher`, a `launcherSuppliesCli` with a `command` or with no `launcherCheck`, or an `envOnlyIfSent` with no `launcherSuppliesCli` (`c9125eb8`). This test comes before `Command is required`. The text follows `13047a51`, measured on Windows for a `launcherCheck` with no `launcher`. For the `envOnlyIfSent` cases, which get this same text, and on Linux and macOS, it is claustrum's own, not measured. A spawn does not test `orManagedFolder`: it drops the member, so a `launcherCheck` with `orManagedFolder` and a `sha256` passes this test (claustrum's own, not measured). |
| `-32602` | `Command is required` | `command` is absent or empty, with no `launcherSuppliesCli`. |
| `-32004` | `the managed launcher cannot be used: <reason>` | The launcher checks refuse `launcher`. On Windows every `launcher` gets this, with the reason `a launcher cannot be applied on a Windows host`. A `launcherSuppliesCli` spawn gets `the launcher cannot be used: <reason>` (`c9125eb8`). |
| `-32602` | `command must be an absolute path when a launcher is given` | A relative `command` with a `launcher`. |
| `-32602` | `linkBound cannot be combined with pty or launcher` | With `launcher` (`683cc6a9`). It logs the failed-start line (`4c111e86`, measured on Linux and Windows, and `87375cc7`, measured on Windows). On macOS, and for `87375cc7` on Linux, the line is claustrum's own (not measured). |
| `-32602` | `this id is in use, or was a terminal's or a link-bound process's: take a fresh one` | A [spent](process-link-bound.md#the-spent-id) `id` (`683cc6a9`). |
| `-32602` | `this id was asked after, or asked to be ended, while no process had it: take a fresh one` | A [given-up](#a-given-up-id) `id` (`4c111e86`). |

### Errors of the `pty` member

| Code | Text | Cause |
|---|---|---|
| `-32602` | `Invalid params` | A `pty` that is present and is no object and not `null`, such as `true` or `false` (`4c111e86` and `c9125eb8`, measured on Linux, Windows and macOS). Its place before the `id` test is claustrum's own. |
| `-32602` | `Invalid params` | On Windows, a `pty` object with a bad member type or size. See [a keyed `pty` on Windows](#a-keyed-pty-on-windows). |
| `-32602` | `A terminal needs a key of 32 bytes in hex` | A `pty` object with no `key` of 64 hex digits: `{}` (`4c111e86` and `c9125eb8`, measured on Linux), or a `key` that is absent, `null`, empty, not hex or of another length (`4c111e86` and `87375cc7`, measured on Windows). On macOS this text is claustrum's own. |
| `-32602` | `this host's helper cannot open a terminal` | On Windows, a `pty` object with a valid key. See [a keyed `pty` on Windows](#a-keyed-pty-on-windows). |

### Start failures

| Code | Text | Cause |
|---|---|---|
| `-32603` | `chdir <cwd>: stat <cwd>: no such file or directory` | `cwd` does not exist. |
| `-32603` | `chdir <cwd>: not a directory` | `cwd` is not a folder. |
| `-32603` | `exec: "<name>": executable file not found in $PATH` | A bare name that the lookup does not find, and on Windows also a path with no extension. On Windows the text ends in `%PATH%`. |
| `-32603` | `fork/exec <command>: <reason>` | The command is missing, a folder or not executable, or the start fails. On Windows a missing file gives the reason `The system cannot find the file specified.` |
| `-32603` | `For security, Claude won't use a git inside a folder it works in, and this host has no other git on its PATH. Install git somewhere else, such as /usr/bin` | A bare `git` whose every git on `PATH` lies in a [session folder](git-rules-session-folders.md#with-no-other-git) (`b3081b51`). On Windows the text ends `C:\Program Files\Git`. |
| `-32603` | `Claude couldn't save its list of session folders to ~/.claude/remote on this host. Free up disk space or fix that folder's permissions` | A [session folder](git-rules-session-folders.md#a-record-that-cannot-be-saved) of the spawn that cannot be saved and holds the first PATH entry of the daemon (`b3081b51`). |
| `-32005` | `the managed launcher <path> could not be started: <reason>` | The launcher passes the checks and does not start. |

`<cwd>` is the value after the tilde expansion. `<command>` is the path of the start: the value of the request, or for a bare name the path that the lookup found on `PATH`.

### After a failure

A failure starts no child and makes no record. It logs the failed-start line of [Lines of child processes](../DAEMON.md#lines-of-child-processes). On Windows that line after a `hostCreds` refusal is claustrum's own, not measured. An `Invalid params`, `Process ID is required`, `Command is required` or `A terminal needs a key of 32 bytes in hex` error writes no line (the key text: `4c111e86` and `87375cc7`, measured on Windows).

### A keyed `pty` on Windows

This section matches `4c111e86` and `87375cc7`, measured on one Windows host, except where it says claustrum's own. That every Windows host refuses is claustrum's own.

A `pty` object with a valid key gets `this host's helper cannot open a terminal`, with the reason `other`. Uppercase hex is valid, and absent or `0` `cols` and `rows` reach this refusal. It starts no child and logs one failed-start line.

A key that is a number, an array, an object or `true` gets `Invalid params`, and so do `cols` or `rows` that are negative, a text, `80.5`, `65536`, `2^31`, `2^32` or `10^12`. A `null` key gets the key text. `cols` or `rows` of `65535` or `null` reach the refusal.

A bad member type, an empty `id` or `command`, a bad key and `gitRead` get their own replies first, and on `87375cc7` a refused `unattendedSeconds` too. The refusal then wins over the command lookup, the `cwd` test, the `launcher` refusal, `linkBound` with a launcher, a live `id` and a given-up `id`. claustrum's own (not measured): its place after the launcher shape test, and that it wins over a spent `id`.

The refusal does not reserve the `id`, so a plain spawn of it starts. A `process.stdin` for it gets `Process not found`. After a `process.kill`, `process.killAndWait` or `process.reattach` of it, the `id` is [given up](#a-given-up-id).

### The reason member (build `52d82f0a`)

A start refusal adds `data` after `message`: `{"reason":"<reason>"}`. `Invalid params`, `Command is required` and `Process ID is required` (claustrum's own) carry none.

A `-32004` refusal of `c9125eb8` puts `launcherFault` first: `{"launcherFault":"<fault>","reason":"launcher"}`. Windows adds `windows_host` to every launcher refusal. Linux adds a fault of [the file check](launcher-resolve.md#the-file-check-build-c9125eb8). On Linux with no `launcherCheck`, a missing launcher adds `missing`, an empty one `not_absolute` (`c9125eb8`, measured on Linux), and the own path `other` (`4c111e86` and `87375cc7`, measured on Linux). With `launcherSuppliesCli` this is claustrum's own. The other Linux faults are claustrum's own: `not_absolute` for a relative launcher and `script_suffix` for a script. macOS keeps `{"reason":"launcher"}` there (not measured).

| Reason | Linux | Windows |
|---|---|---|
| `cwd_missing` | missing or a file | the same |
| `cwd_unusable` | a `cwd` of mode 000 that exists | none (claustrum's own) |
| `not_found` | not found | not found, a folder |
| `not_executable` | mode 0644, a folder | not a program |
| `busy` | a program open for a write | a program open for a write |
| `launcher` | missing (`-32004`) | any `launcher` |
| `other` | a relative `command` with a `launcher` | a keyed `pty` |
| `host_settings` | [a bad host settings file](#the-host_settings-refusal-build-13047a51) | none (from the code) |

`busy` and the Windows `not_executable`: `87375cc7`, measured on Linux and Windows. The test order is not measured.

On macOS only `cwd_unusable` is measured. The rest follows Linux, `busy` included, as do the other `-32004` refusals and `-32005` (`launcher`) and other errors (`other`) (claustrum's own).

### The host_settings refusal (build `13047a51`)

On Linux a Claude Code spawn gets `-32010` while [the host settings file](../DAEMON.md#the-host-settings-file-linux-build-13047a51) cannot be used, and logs its failed-start line. A plain spawn starts. `data` is `{"reason":"host_settings","settingsFault":"<fault>"}`. The message is `Claude Code was not started: this host's settings file for Claude Desktop, /etc/claude-desktop-ssh/settings.json, can't be used. <detail> Only the host's administrator can change it.` All this is measured on Linux. The refusal reaches `ccd-cli` with or without args, a command with `stream-json` in its args and a launcher spawn with such args. A plain command and a bare `git` with `--output-format=stream-json` start. The refusal comes after the `unattendedSeconds` test and before the command lookup. Claustrum's own, not measured: it refuses each [Claude Code shape](git-rules-session-folders.md#which-spawn-makes-a-session-folder) but no `gitRead` spawn, after the `linkBound` test.

| `settingsFault` | `<detail>` |
|---|---|
| `value` | `unattendedLimitMinutes must be a whole number of minutes, 1 or more, with no quotes. To set no limit, remove it.` |
| `syntax` | `It must hold one JSON object, such as {"unattendedLimitMinutes": 60}.` |
| `unreadable` | `It can't be read: it is larger than 64 KiB.`, `It can't be read: it is not a regular file.` or `The account that signs in can't read it. Give it mode 0644.` |
| `writable` | ``` `<file or folder>` can be changed by accounts other than its owner (mode <mode>). Remove that write access. ``` |
| `owner` | ``` `/etc/claude-desktop-ssh/settings.json` belongs to the account that signs in (uid <uid>). Root must own the settings file and every folder above it. ``` |

On macOS `13047a51` sends no `host_settings` (measured on macOS).

One failure ends another process first: a session spawn whose command exists and does not start. See [Session supersede](#session-supersede).

## Behavior

### What the child gets

- **Streams.** The child's stdin, stdout and stderr are daemon pipes, which send what they read as stream frames to the spawning connection and store them.
- **The life of the process.** A close of its connection ends only a [link-bound](process-link-bound.md) process. A graceful shutdown ends the processes of the table, except with `-keep-children` ([CT-2](../DIVERGENCES.md#ct-2)).

### The environment of the child

The child gets the environment of the daemon with the `env` of the request merged over it. A good [server.adoptEnv](server-adopt-env.md) sets the base. A `gitRead` spawn is the exception. On Linux and macOS the entries come in this order:

1. The entries of the daemon, with `CLAUDE_SSH_DAEMON_CHILD=1` last. A caller key that the daemon has replaces that entry in place. An empty caller value of `CLAUDE_SSH_DAEMON_CHILD` stays empty (measured on Linux).
2. The new caller keys, in no fixed order (measured on Linux).
3. `CLAUDE_SSH_TAKES_COMMONDIR_PLACEHOLDER`. If `env` holds no value of that name, the value is 1 (`4c111e86`, measured on Linux and macOS). A value in `env` wins, with no second entry. A daemon entry of the name gets the value 1 at its place. The place after the new caller keys is measured on Linux. The place before `SSH_AUTH_SOCK` is claustrum's own. On Windows the entry is not measured.
4. `SSH_AUTH_SOCK` from [the hand-off](#the-ssh-agent-hand-off-linux-and-macos). Without that step there is no entry here.
5. `CLAUDE_SSH_RUN_DIR` and then `CLAUDE_SSH_CHILD`, the two [identity markers](#the-identity-markers-linux-and-macos). A caller value of either does not reach the child.

After a good read, the child gets the login-shell `PATH` of the user. A `PATH` in `env` wins over it. See [The login-shell PATH](#the-login-shell-path-linux-and-macos). The value never changes how the daemon looks up `command`.

#### Names with their own rule

| Name | Removed from |
|---|---|
| `CLAUDE_RPC_TOKEN`, `CLAUDE_SSH_NO_JOBS` (`52d82f0a`, measured on Linux, macOS and Windows) | The daemon value, for every child. A value in the `env` of the request reaches the child (claustrum's own for `CLAUDE_SSH_NO_JOBS`). |
| `CLAUDE_SSH_MANAGED_LAUNCHER`, `CLAUDE_SSH_E2E_MANAGED_SETTINGS_DIR` | Every child. The removal covers the `env` of the request too (measured on Linux and macOS). The removal on Windows (claustrum's own). |
| `CLAUDE_SSH_CHILD`, `CLAUDE_SSH_RUN_DIR` | On Windows, a plain spawn, also from `env` (`87375cc7` and `13047a51`, measured). `gitRead`, adopted-base and launcher spawns: claustrum's own. Only the upper-case names match, so a name in another case, such as `claude_ssh_child`, gets through (claustrum's own, not measured). |
| `CLAUDE_CODE_PROCESS_WRAPPER` | A child with a `launcher`, also from the `env` of the request. A child without one keeps it (measured on Linux and macOS, claustrum's own on Windows). |
| `CLAUDE_SSH_NO_LOGIN_PATH_WARM` | The daemon value, for every child (claustrum's own on Windows). A value in `env` reaches the child (claustrum's own). |
| The eight editor names and the session folders | A spawn of the bare command `git`. See [Session folders](git-rules-session-folders.md#the-variables-of-a-git-child) (`b3081b51`). |
| `CLAUDE_SSH_PEER_CHECK` | The daemon environment, at its start. On Windows that covers any letter case. A value in `env` reaches the child. These three are measured on Linux, macOS and Windows. If the removal fails, claustrum logs one WARN line of its own, and the children inherit it. |
| `CLAUDE_SSH_LEAVE_GROUPS` | The daemon environment at its start, on Linux and macOS (`87375cc7`, measured). A value in `env` reaches the child (claustrum's own). Windows does not remove it (claustrum's own, not measured). If the removal fails, claustrum logs one WARN line of its own, and the children inherit it. |

The Go runtime variables `GODEBUG`, `GOGC`, `GOMAXPROCS`, `GOMEMLIMIT` and `GOTRACEBACK` reach the child unchanged. `GOGC` and `GOMAXPROCS` are measured on Linux, the other three are not measured.

### A temp folder that takes no file (build `683cc6a9`)

On Linux and macOS a daemon value of `CLAUDE_CODE_TMPDIR`, `TMPDIR`, `TMP` or `TEMP` does not reach a child if its folder cannot take a new file `.claude-ssh-<n>`. For a missing folder the nearest existing parent folder takes the test. A missing folder under a folder that takes the file stays. On Linux a relative value stays too, and spawns at once share one test, which removes the file. `TMP`, the order of the names and the test of the parent are claustrum's own, not measured. claustrum tests the names one after another and waits up to 2 s for each. `4c111e86` makes the file of the second folder about 2 s after the first (measured on Linux, macOS: claustrum's own). A name whose folder an earlier name of the same spawn named takes that test (claustrum's own, not measured). For two such folders `4c111e86` replies after about 4 s and logs the [slow temp folder line](../DAEMON.md#lines-of-child-processes) once for each (measured on Linux). The maintainer approved the test of the parent.

### The private temp folder (build `b3081b51`)

On Linux and macOS a spawn with `"cliTempRoot": true` tests the entry `claude-<uid>` in the temp dir of the child. On macOS `CLAUDE_CODE_TMPDIR` alone names the temp dir, with no `/tmp` fallback. `TMPDIR` gives no member (measured on macOS). `TMP`, `TEMP` and the missing fallback are claustrum's own there. These rules are measured on Linux:

- The temp dir is the first value that is not empty of `CLAUDE_CODE_TMPDIR`, `TMPDIR`, `TMP` and `TEMP` in the env of the child. `CLAUDE_CODE_TMPDIR` comes before `TMPDIR`, an empty `TMPDIR` goes on to `TMP`, and `TMP` or `TEMP` alone is taken.
- A temp dir that does not exist or a relative `TMPDIR` gives no member. So does a spawn with a `launcher`.
- With no entry, or an own folder of mode 0700 or 0755, the result has no member and the child gets nothing.
- An unfit entry gives `refused`: `a link`, `not a folder`, `another account's`, `on another file system` or `not readable by its owner`. The entry stays.
- For the other three kinds the daemon then makes `ccd-<10 hex>` beside the entry, with mode 0500 and an inner folder `t` of mode 0700. The child gets `CLAUDE_CODE_TMPDIR=<temp dir>/ccd-<10 hex>/t`, and the result names `ccd-<10 hex>` (`4c111e86`).
- The daemon logs `[process.Manager] Claude Code's temp folder is unfit (it is <kind>): Claude Code gets <name>/t beside it` (`4c111e86`).

```json
{"success":true,"cliTempRoot":{"refused":"not readable by its owner","name":"ccd-a5247ffbe0"}}
```

#### When no folder is given

With no folder, `whyNot` takes the place of `name`. The log line then ends `(it is <kind>), but no other was made: <whyNot>`. The spawn still succeeds.

| `whyNot` | Cause |
|---|---|
| `the temp dir or a folder above it could not be opened for reading` | A temp dir of mode 0333. This test comes first, so `refused` is absent and no line is logged. |
| `the temp dir or a folder above it belongs to, or can be written by, another account` | A temp dir of mode 0777. `/tmp` passes. |
| `only a folder of root's or of this account's own that others cannot write is stepped around: another account may have laid this` | The entry is a link or a file (`4c111e86`). A temp dir that cannot be searched (see [a failed test](#a-failed-test-of-the-temp-dir-build-4c111e86)). |
| `no folder could be made in the temp dir: permission denied` | The mkdir of the new folder failed (see [a failed test](#a-failed-test-of-the-temp-dir-build-4c111e86)). |
| `too many sessions of yours on this host have a private temp folder of their own; end one, or try again in a minute` | The daemon holds 8 folders (`c9125eb8` and `4c111e86`, measured on macOS). Spawns that share a folder take one place (measured on Linux). Or the daemon made 8 new folders in the last minute (see [the cap of new folders](#the-cap-of-new-folders-build-4c111e86)). |
| `the temp dir did not answer in time` | A temp dir on a stuck NFS mount. `b3081b51` replied after 2.13 s. The child gets its env as it was. `refused` is absent. On Linux `4c111e86` logs `[process.Manager] the temp dir has not answered in 2s: Claude Code starts anyway, and may refuse its folder there` once, for the spawn whose own step hits the limit, and claustrum logs it at WARN (see [while a step is late](#while-a-step-is-late-build-4c111e86)). A late step logs its own line and writes the record, and the next spawn gets its folder (`4c111e86`, measured on Linux; `b3081b51` kept the folder with no record). |
| `the new folder was not this account's own with mode 0700, and was removed` | The new folder changed before the daemon held it. |

#### While a step is late (build `4c111e86`)

While the step of a temp dir goes on after the limit, a spawn with that temp dir gets `the temp dir did not answer in time` at once, with no step and no line (`4c111e86`, measured on Linux). These are claustrum's own: macOS, the end of that time when the late step returns, a step that starts within the limit of an earlier one, no such time for a step that only waits for the lock of the folders, whoever holds it, and the key, which is the path of the temp dir as given, so two spellings of one dir or a link each run their own step.

While the mount of a held folder does not answer, a spawn with another temp dir that answers gets its folder at once (`4c111e86`, measured on Linux). These are claustrum's own: macOS, and a `whyNot` while a refresh does not answer.

#### A failed test of the temp dir (build `4c111e86`)

These rules are measured on macOS. On Linux they are claustrum's own.

- A temp dir whose attributes cannot be read gives no member and no line. A folder above it that cannot be searched counts as such a temp dir (claustrum's own).
- A temp dir that cannot be searched gives `refused` `out of reach` and the text of a link or a file.
- A mkdir of the new folder that fails with `EACCES` gives `no folder could be made in the temp dir: permission denied`. `refused` names the kind of the entry. Another errno gives its own text (claustrum's own).

#### The cap of new folders (build `4c111e86`)

These rules are measured on macOS. On Linux they are claustrum's own.

- A daemon makes at most 8 new folders within about 60 s. The next spawn that needs one gets the text of the cap and no folder.
- The child still runs with its env as it was. `refused` names the kind of the entry, and `lost` comes after `whyNot`.
- A spawn that takes an intact folder again makes none. A folder that was removed still counts.
- The count is of one daemon. A new daemon makes a folder at once.

These are claustrum's own: the window of 60 s slides with each make, and a make 60 s old no longer counts. The count is in memory. One count runs across all temp dirs, and only one `TMPDIR` is measured. A check that makes a folder anew at its name is no make. A make whose new folder fails its test and is removed counts. A mkdir that fails does not. The cap holds for each kind of entry.

#### The removes of a new folder

With the `removed` text, the daemon removes the new name by one `rmdir` through a handle of the temp dir. A link at the name stays, and the text still says `removed`. If the daemon made `t`, one `rmdir` of `t` through the handle of the new folder comes first.

#### The record of the folder

The run dir holds `cli-temp-root-<16 hex>`, mode 0600, with the folder name and no newline. The 16 hex are the first 8 bytes of the SHA-256 of the boot id, `/`, the pid namespace, a NUL and the temp dir. The old file goes by one plain remove before the new one.

- A later spawn, of a new daemon too, gets the folder that the file names again.
- A folder that another process holds by `flock`, a folder of 25 h, or another content in the file gives a new name. The old folder stays.
- While a child runs, the daemon holds the folder by `flock` and sets its write time to now, at the hold and every 600 s.

#### claustrum's own rules

These are not measured:

- Random hex, an age bound of 24 h, and the order of the `refused` tests. The link and file test comes after the account test.
- The test of `t` and its `rmdir`. A `t` that is not an own folder of mode 0700 gives the `removed` text too. A later daemon takes again only a folder of mode 0500 with an own `t` of mode 0700.
- The rest of the order of the four names. With none of them set, the temp dir is `/tmp`. A relative value of `CLAUDE_CODE_TMPDIR`, `TMP` or `TEMP` gives no member too, and the next name is not tried.
- The limit is 2 s. After it the step goes on. The limit also covers a wait for the lock ([while a step is late](#while-a-step-is-late-build-4c111e86)).
- On macOS a late step logs its line and writes the record.
- The WARN level of the line of the time limit, and that macOS logs that line too. A late step can log the refuse line of its own `whyNot`.
- A failed stat of an entry that exists, other than `EACCES`, gives no member.
- The account test: a group write bit refuses, a sticky bit passes, and links resolve first.
- The owner test of the new folder.
- The count is per daemon. A folder gives its place back at the exit frame of its last spawn, or, while a step or a refresh holds the lock, once it ends.
- The step runs after the `cwd`, launcher and session command tests, and before the start.
- `outsideJob`, then `cliTempRoot`, come before `pid` and `startTime`.
- A failed registry write is not logged. A failed hold does not stop the spawn.

On macOS the kinds `a link`, `not a folder` and `another account's` are measured too. So are the folder with its `t`, the log line, the link and file text and the 0333 text. The other rules are claustrum's own on macOS. There the record takes `kern.bootsessionuuid` as the boot id, with no pid namespace.

On Windows `cliTempRoot` and `cliTempRootEnd` change nothing, as on `b3081b51` (measured).

#### A lost folder (build `4c111e86`)

On Linux and macOS, claustrum checks all its folders by name together, every 2 s from the first hold (on Linux claustrum's own). These rules are measured on Linux and macOS unless marked:

- A link at the name, a folder of another account or a folder of mode 0777 counts as lost. The daemon logs `[process.Manager] Claude Code's private temp folder <name> counts as lost: <why>`. The why is `it is a link`, `it is another account's (uid <n>)` or `it is open to other accounts`.
- A child whose spawn had `"cliTempRootEnd": true` gets SIGTERM, and the daemon logs `[process.Manager] Process <id> is being ended: its private temp folder is lost`. If it still runs 3 s later, it gets SIGKILL (measured on macOS).
- The `exit` frame carries `"killedBy":"temp_root"`. A `process.kill` in the wait keeps it (measured on macOS).
- An end after a `process.kill` that the child survived replaces `client` with `temp_root`. A child that ended on the SIGTERM of the kill keeps `client` (measured on macOS, on Linux claustrum's own).
- Other children of the folder run on, and the daemon logs `[process.Manager] <n> process(es) run on in <name>, which may be another account's: their starts did not ask to be ended over it (cliTempRootEnd); end those sessions` (measured on macOS).
- The losses that one check finds join the lost list together (measured on macOS).
- The next spawn gets a new folder. Each later member lists the last 16 lost folders, oldest first. A 17th loss cuts the oldest, and none expired within 130 s. The why is `changed` for a link, `anothers` for another account's folder and `open` for mode 0777 (measured on macOS, on Linux claustrum's own):

```json
{"refused":"another account's","name":"ccd-7c9ad7b07c","lost":[{"name":"ccd-ac02b40227","why":"changed","agoSeconds":7}]}
```

claustrum's rule on Linux and macOS: a child that ends on the SIGTERM gets a group SIGKILL before its reap. The SIGKILL of the wait goes to the group too.

#### Other kinds of loss (build `4c111e86`)

These count as lost too and end the child in the same way (measured on macOS, on Linux claustrum's own). Each why follows `counts as lost: `.

- A file at the name: `it is not a folder`.
- A mount on the held folder: `it is on another file system`.
- A file, a link or a mount at `t`: `it is holding a t that is not a folder`, `it is holding a t that is a link` or `it is holding a t that is on another file system`.
- A file or a link in place of the temp dir: `it is in a temp dir that is no longer a folder`.
- A new mount on the temp dir: `it is in a temp dir whose path now leads to another file system (a new mount?)`.
- A temp dir of mode 0777, also of root: `it is in a temp dir that another account can rearrange`.

#### Changes that end no child (build `4c111e86`)

- An own folder that took the place of the held one is held instead and gets `t` and mode 0500. The line ends `was no longer at its name: this account's folder that stands there is held instead` (measured on Linux and macOS).
- With no entry at the name, a new folder of that name is made. The line ends `was no longer at its name: it is made anew` (measured on macOS).
- A held folder whose `t` is gone gets `t` again (measured on macOS). A `t` of mode 0777 stays (measured on Linux and macOS).
- A sticky temp dir of mode 1777 changes nothing (measured on macOS).

#### Lines of a check that ends no child (build `4c111e86`)

- A temp dir that is gone logs `[process.Manager] Claude Code's private temp folder <name>: the temp dir is gone; nobody is ended over that, and it is looked at again` (measured on macOS).
- A held folder of mode 0 logs `[process.Manager] Claude Code's private temp folder <name>: its owner cannot use it (chmod 500 it); nobody is ended over that, and it is looked at again` (measured on macOS, and on Linux as one line per run of such checks).
- On Linux (on macOS claustrum's own) a temp dir that cannot be read logs `[process.Manager] Claude Code's private temp folder <name>: the temp dir could not be looked at: <error>; nobody is ended over that, and it is looked at again`. A later good check logs `[process.Manager] Claude Code's private temp folder <name>: all is well with it again`.
- On Linux (on macOS claustrum's own) a check that has not answered after 4 s, as on a dead NFS mount, logs `[process.Manager] Claude Code's private temp folder <name>: its name does not answer; nobody is ended over that, and it is looked at again` once, also for a folder given back. If that check then finds all well, it logs the line that all is well again. The bound of 4 s is claustrum's own.

#### The cap of these lines (build `4c111e86`)

A held folder logs 49 of these lines. In place of the 50th it logs `[process.Manager] Claude Code's private temp folder <name>: its state keeps changing, and is logged no more unless it is lost`. Later changes log nothing (measured on Linux, with a folder of mode 0 and then 0500 again). These are claustrum's own: the cap on macOS, a count for each folder, the lines of a gone or unreadable temp dir in the count, the line of a later loss, a new count when a given-back folder is held again, no count for the line that the name does not answer, and for a held folder the line of a failure or a loss in place of the line that all is well.

??? note "claustrum's own rules of a lost folder"
    The why `changed` for the other kinds of loss. Whole seconds, and an entry that never expires. A group or other write bit of the folder counts as open. A temp dir with an other write bit, in any mode, and no sticky bit counts as lost, and a group write bit alone ends nobody. A temp dir that group or others can write without the sticky bit, or that an account other than this one and root owns, gets no folder made anew. The order of the tests. A temp dir that was a link at the hold can stay a link. A folder with no owner read or search bit counts as mode 0. The text `<name>: ` before the line of a gone temp dir, or of mode 0 on macOS. One line for a run of other failed checks, and a gone temp dir and a folder of mode 0 count as failed checks. A lost folder is not checked or given again by this daemon. A spawn that binds its child after the loss ends it at once. A spawn between two folders of one check gets a new folder for one just lost, and the lost list of its reply lacks it. During the look of a held check, a folder given back can still be made anew, fitted or held at its name. The given-back folder keeps that hold. No end starts after the shutdown began. A check takes the folders in the order of the hold (`4c111e86` varied it).

#### A folder given back (build `4c111e86`)

A folder stays checked after its last child ended. A loss 5 s after that end still counts at the next check (measured on macOS).

The folder keeps its `flock`, as does the folder of a late step (`4c111e86`, measured on Linux). These are claustrum's own: macOS, and the `flock` goes when its checks end, but stays with a new hold. Its checks write nothing and log no line but a loss and the two lines of a check that does not answer (those two measured on Linux). They end when it is lost or held again, when its temp dir is gone, or when its name holds no entry or another own folder. They also end after 10 min, and for the oldest past 8 such folders.

### The managed launcher

With `launcher`, the child runs as `<launcher...> <command> <args...>`. The launcher is the process, and the stream frames are its own. A spawn reads no managed settings. The `CLAUDE_SSH_MANAGED_LAUNCHER` gate does not change it (measured on Linux).

A spawn with a `hostCreds` member gets [the hostCreds test](#the-hostcreds-member-build-13047a51) before every step of this list, on every system. The place of that test is claustrum's own, not measured.

The checks run in this order. Step 1 is the Windows rule. The other steps are measured on Linux. Step 5 is measured on macOS too, and so is the interpreter text of step 6:

1. On Windows any `launcher`, `[]` too, gets `-32004 … a launcher cannot be applied on a Windows host`. With `unattendedSeconds` 60 and no `--no-session-persistence`, `87375cc7` answers the refusal of that member instead (measured on Windows). Other refused values here are claustrum's own.
2. A relative `command` gets `-32602 command must be an absolute path when a launcher is given`.
3. The checks of `launcher.resolve` run, with `command` in place of `cliPath`. A refusal gets `-32004 the managed launcher cannot be used: <reason>`, with a reason of [Unusable reasons](launcher-resolve.md#unusable-reasons). `[]` gets the text for no launcher.
4. The `cwd` test, with its usual error.
5. A `command` that is missing, a folder or not executable gets `fork/exec <command>: <reason>`, as without a launcher. The launcher does not run.
6. A launcher that does not start gets `-32005`. A missing `#!` interpreter, or a script with Windows CRLF line ends, gives the reason `its interpreter was not found (the program on its #! line, or the loader of an ELF binary; a script saved with Windows CRLF line endings fails this way)`. A 0755 text file with no `#!` gives `exec format error`.

claustrum's own: step 1 comes before step 2, step 5 before the launcher starts, and any other start failure gives its errno text. The step 6 frames of claustrum are tested, not measured.

A started launcher logs the start line of [Lines of child processes](../DAEMON.md#lines-of-child-processes), which names the first element of `launcher` only. A refusal writes no `[LauncherHandler]` line.

#### A launcher check (build `c9125eb8`)

With `launcherSuppliesCli` the child runs as `<launcher...> <args...>`, with no command, and step 5 does not run. With no `args` the launcher gets no args (measured on Linux).

- On Windows step 1 adds the fault `windows_host`.
- On macOS a `launcherCheck` gets `-32004 … This launcher can't be used on a macOS host: …` with the fault `macos_host`, before any file test (the text of `4c111e86` on a spawn: claustrum's own, not measured). The text is on [the resolve page](launcher-resolve.md#platform-notes).
- On Linux step 3 runs the file check. `[]` gets the text for no launcher, with the fault `not_absolute` (measured with `launcherSuppliesCli`).

claustrum's own: the macOS refusal comes before step 2. With a command, the own-path test runs first and has no fault, and `[]` gets `not_absolute` too.

## Platform notes

=== "Linux and macOS"

    - **Process group.** The child leads its own process group. A `KILL` from [process.kill](process-kill.md) reaches the whole group.
    - **SSH agent hand-off and `launcher`.** Yes, and allowed.
    - **Identity markers, records, reap.** Yes.
    - **`signal` in the `exit` frame.** Yes.

=== "Windows"

    - **Process group and job.** The child gets a new process group and a [job](process-kill.md#windows-child-trees) (`52d82f0a`), since `4c111e86` after a passed [start test](process-kill.md#the-start-test-of-the-jobs).
    - **`launcher`.** Refused with `-32004`.
    - **`signal` in the `exit` frame.** Never.

    These rules are measured on Windows, unless a tag says otherwise.

    - There is no SSH agent hand-off and no identity marker. The capability and the parameter exist.
    - The block of the child is in name order ([D23](../DIVERGENCES.md#d23)).
    - With no adopt the child gets the `PATH` of the daemon as an entry named exactly `PATH`. claustrum reads it once, at its start (claustrum's own). An entry named `PATH` is replaced in place, else `PATH` is added. Then the `env` of the request comes.
    - Of the entries whose names differ only in letter case, the child gets the last one (from the code). So a daemon `Path` reaches the child as `PATH`, and a caller `Path` value is lost. A caller `PATH` or `path` value keeps its own name.
    - A daemon with no path entry, or a `Path` with an empty value, gives the child no `PATH` entry.

### The login-shell PATH (Linux and macOS)

The read starts in the background with the daemon. These rules are measured against `52d82f0a`.

- `CLAUDE_SSH_NO_LOGIN_PATH_WARM=1` moves the start of the read to the first spawn. The value 0 does not.
- A spawn waits for a running read at most 4 s, and not past 8 s after its start. Until a good read, children get the inherited `PATH`.
- A later spawn tries a failed read again after 30 s, then 1m0s and 2m0s. A good read is kept.
- The failure line says `until one works, programs keep the last PATH that was read, or sshd's if none was`, also before a good read (measured on Linux, `c9125eb8` and `4c111e86`).
- `"disableShellAgentSocket":true` kills the shell group of a read that still runs after the wait, and after a failure starts no read. The shutdown kills it too.
- The read runs `$SHELL -l -i -c …` if `$SHELL` is an executable file, or else the first usable of `/bin/zsh`, `/bin/bash` and `/bin/sh`.
- The `PATH` is the line after a fresh sentinel line, on stdout or on stderr, with a final newline and an absolute directory.
- 2 MiB on stdout fails the read, and a full stderr does not. A shell that leaves a process on its stdout still gives its PATH, about 500 ms after it ends. The log holds no shell output.
- The shell gets `HOME`, `PATH`, `SHELL`, `TERM` and `USER` of the daemon, with `TERM` empty if the daemon has none, plus `DISABLE_AUTO_UPDATE=true`, `ZSH_DISABLE_COMPFIX=true` and `CLAUDE_DESKTOP_RESOLVING_ENVIRONMENT=1`. On Linux a read again after an adopt gets the PATH of the adopt (`c9125eb8`, [server.adoptEnv](server-adopt-env.md#a-new-read-of-the-login-shell-path)).
- claustrum's own: a 30 s limit per read, the stream order, the doubling after 2m0s, no try after 16 failures, and no wait after 5.

### The identity markers (Linux and macOS)

The daemon starts the child through a short re-exec of its own binary, the exec-child trampoline. It adds two markers, so a later daemon can tell the child from a process that reuses its pid:

- `CLAUDE_SSH_RUN_DIR=<run dir>`. The run dir is the folder of the socket path, cleaned and not made absolute. The socket `<R>/b/s.sock` gives `<R>/b`.
- `CLAUDE_SSH_CHILD=<pid>:<start>`. On Linux `<start>` is the start time of the child in `/proc` clock ticks. On macOS it is the start time from `ps` as a date text with one blank between its words. Only days 1 and 2 of a month are measured.

The trampoline adds no frame.

??? note "The trampoline does not start"
    If the trampoline does not start, the daemon logs `[process.Manager] exec trampoline failed for <id> (<error>); starting it directly — a successor daemon will not be able to reap it` and starts the command directly (measured on Linux). The frame then names the command, for example `fork/exec <command>: permission denied` for a `cwd` that the daemon cannot enter. From the code: if that direct start works, the reply is success, and the child gets `CLAUDE_SSH_RUN_DIR` and no `CLAUDE_SSH_CHILD`.

??? note "A later daemon start"
    A later daemon start can end the child ([Child records and the reap](../DAEMON.md#child-records-and-the-reap-linux-and-macos)). A record that cannot be written does not fail the spawn, and the child still gets both identity markers.

### The SSH agent hand-off (Linux and macOS)

Its `server.capabilities` feature is `process.spawn.shellAgentSocket`. A child whose environment has no `SSH_AUTH_SOCK` entry gets the one that the user's login shell exports. Its rules are measured on Linux and macOS.

- An entry that is there wins, even an empty one (`"SSH_AUTH_SOCK":""` in `env`).
- The daemon runs the same shell as the `PATH` read at the first spawn that needs a socket. The run has a 4 s limit, and the daemon gives up on a shell that the kill cannot end at 5 s.
- Measured on Linux against `683cc6a9`: a plain file gets no connect call. A live answer holds with no connect at 60 s, for a dead agent, and is gone at 610 s. A new socket there is dialed again. claustrum's own: any other non-socket, the 600 s, and the key of path, device, inode and mtime.
- A dialed socket that does not accept a connection in 250 ms is not handed on. The shell runs again only 10 minutes after the last run, never after 3 failed runs in a row. A connect still open after 500 ms ends the hand-off for good (`f6010b97`, measured on Linux: a dial that hangs gets no login shell run 610 s later). A stat that does not answer ends it too (for good: claustrum's own). The reply then comes after 0.5 to 1.5 s, also for a stat that answers at 1.5 s (`4c111e86`, measured on Linux). It logs `[shellenv] A check of SSH_AUTH_SOCK=<path> never returned (a hung filesystem?); not using it or asking the login shell again for this daemon's lifetime` (`4c111e86`, measured on Linux).
- While the `PATH` read runs, the step runs no shell (measured against `52d82f0a`).
- The step never fails a spawn. Each run and give-up writes a `[shellenv]` line.
- A `gitRead` spawn skips this step. It runs no shell for the socket, logs no agent line and uses up no try, and its git gets an empty `SSH_AUTH_SOCK` (`87375cc7` and `4c111e86`, measured on Linux at a first run). With a login shell that exports none, the next plain spawn logs `try 1 of 3` (Linux and macOS). On macOS, with that shell, the `gitRead` spawn logs no agent line and its git gets the empty value. A macOS login shell that exports a socket and a skip with a cached socket are claustrum's own, not measured.

## Rare states

### A spawn with an `id` that is in use

A spawn with the `id` of a process that still runs succeeds and replaces its table entry. The first process gets no signal and keeps running. Then:

- `process.kill`, `process.stdin` and `process.reattach` of that `id` reach the second process only.
- `-stop` and `server.shutdown` end only the processes of the table, so the first process outlives the daemon, except in a Windows job with no `-keep-children` (claustrum's own).
- Each process keeps the connection that spawned it, under the same `processId`, and each counts its own `seq`. So a connection that sent both spawns gets two streams under one `processId`, with two `exit` frames (measured on Linux).
- A `process.reattach` moves the stream of the second process and closes the connection that had it. A connection that gets the frames of the first process keeps getting them (measured on Linux).

### Session supersede

A spawn whose `args` name a stream-json session of the Claude Code CLI ends every other running process of the same session. Its `args` must hold:

- `--input-format=stream-json`, `--output-format=stream-json` or a bare `stream-json`, and
- a session id from `--session-id <v>` or `--session-id=<v>`. Without one, it is `--resume <v>` or `--resume=<v>`, unless `--fork-session` is there too.

The argument after a bare `--session-id` or `--resume` is its value. The id has 1 to 128 characters. Each is a letter, a digit, `-`, `_`, `.` or `:`, and the first is not `-` (both from the code). Any other spawn supersedes nothing.

#### How the old process ends

The daemon sends `SIGTERM` to the direct child of the old process and waits. After 3 s it sends `SIGKILL` to the group. The `exit` frame of the old process reaches its client with `"killedBy":"client"`. The pipe drain of that frame takes up to 5 s. Then the new process starts, and its reply comes after that `exit` frame. On Windows the old process ends at once, and two spawns of one session run one after the other (both claustrum's own).

A spawn with the `id` and the session of a running process only replaces it ([the `id` in use](#a-spawn-with-an-id-that-is-in-use)).

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

### A given-up `id`

Build `4c111e86`, measured on Linux, macOS and Windows:

- `process.kill`, `process.killAndWait` and `process.reattach` of an `id` that no process has give that `id` up. Their replies stay as before.
- A spawn of a given-up `id` gets `-32602` with the text of [Errors](#errors) and `"data":{"reason":"other"}`. For an `id` given up before the start, the command does not run. The daemon log gets `[process.Manager] Failed to start process <id>: <text>`.
- An `id` of 1 to 128 bytes is given up, and a longer one is not. Letter case and a trailing blank make another `id` (measured on Windows).
- The `id` is free again between 840 and 910 seconds after it was given up (measured on Linux and Windows). On macOS it is refused at 14 minutes and free at 16. A refused spawn does not renew the time (measured on Windows).
- On Linux, at most 4096 `id`s are given up, and a new give-up past that is dropped. The cap counts every given-up `id`. After 4100 give-ups one at a time, `id`s 0 to 4095 are refused and later ones start. In a burst of give-ups, the `id`s near the cap varied from run to run. A full set first drops the `id`s past their time, and then a new give-up gets in. While the set stays full, a new give-up of a given-up `id` does not renew its time.

claustrum's own, not measured: the `id` stays given up for 15 minutes, and a new give-up renews it outside a full set. The test comes right after the test of a spent `id`. A reattach with a `proof` gives the `id` up too. In a burst, the kept `id`s are its own.

#### A give-up during the start

Build `4c111e86`, measured on Linux: a `process.kill` of the `id` can come while the command starts, before the reply. The spawn gets the same `-32602` reply after the start. The log gets no `Process <id> started` line for it. The daemon sends `SIGKILL` to the group of the new child, with no `SIGTERM`, and waits 3 seconds for its end. If the child still runs then, the log gets `[process.Manager] Process <id> was started and not registered, and has not ended 3s after its kill (<nil>)`. Then the log gets `[process.Manager] Failed to start process <id>: <text>`, and the reply comes after that wait.

claustrum's own, not measured: macOS and Windows, and the end time of the child after the kill.

### A spawn during a shutdown

A start that returns after the shutdown began gets no reply. Windows ends its child, also with `-keep-children`. Linux and macOS send none (claustrum's own). On `4c111e86` ended groups equal start lines, and children with no start line outlive it (measured on Linux). Later writes fail (from the code).

## Differences from the reference

[Divergence catalog](../DIVERGENCES.md) entries of this method:

- [CT-1](../DIVERGENCES.md#ct-1): the `wantPid` parameter.
- [CT-2](../DIVERGENCES.md#ct-2): the `-keep-children` flag.
- [D20](../DIVERGENCES.md#d20): the 50 ms wait in the reap before the `SIGKILL` of a group whose leader reads as gone.
- [D23](../DIVERGENCES.md#d23): the Windows environment order.

claustrum lacks the terminal of the pinned `87375cc7`. On Linux and macOS a `pty` object with a valid key runs the command with no terminal (claustrum's own). On Windows `c8e2f083` refuses `pty` with `-32602` (measured on Windows, [open items](../REFERENCE-BUILDS.md#52d82f0a28317830ccacea80f6f4aac1d7563353-2026-10-09-built)).
