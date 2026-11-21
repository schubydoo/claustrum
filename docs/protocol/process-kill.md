# process.kill

`process.kill` sends a signal to a child process that `process.spawn` started. It answers at once. To wait for the end, use [process.killAndWait](process-killandwait.md).

An untagged rule on this page matches build `89cb6289`.

## Request

```json
{"id": "p1", "signal": "TERM"}
```

| Parameter | Required | Meaning |
|---|---|---|
| `id` | no | The id of the process, as given to `process.spawn`. An absent, empty, unknown or [spent](process-link-bound.md#the-spent-id) `id` signals nothing. An unknown `id` of up to 128 bytes is [given up](process-spawn.md#a-given-up-id). |
| `signal` | no | `TERM`, `KILL`, `INT` or `HUP`, with or without the prefix `SIG`. The default is `TERM`. The names are case-sensitive, and each other value means `TERM`. On Windows the name is ignored. |
| `farewell`, `graceMs` | no | The [farewell](process-farewell.md) for the stdin of the child, and the wait before the signal. |

For an `id` that two spawns share, see [process.spawn](process-spawn.md#a-spawn-with-an-id-that-is-in-use).

## Response

```json
{"success": true}
```

A farewell adds `farewellEnd`: see [Farewell](process-farewell.md).

## Errors

This method has no `errorCode` member. A failure is a JSON-RPC error.

| Code | Text | Cause |
|---|---|---|
| `-32602` | `Invalid params` | The request has no `params` member, or `params` or a member that it reads has the wrong type. See [the rules for `params`](../PROTOCOL.md#params-presence-and-typing). |
| `-32603` | The system error, such as `operation not permitted` | Linux or macOS refused the signal of a kill with no farewell, as to a root child. Its later `exit` frame has no `killedBy` (`52d82f0a`, measured). For a farewell see [its page](process-farewell.md#the-end-of-the-child). |

## Which processes a signal reaches

On Linux and macOS, each child runs in its own process group.

- `KILL` goes to the whole process group of the child. So the processes that the child started end too, unless one left the group.
- Each other signal goes to the direct child only. A process that the child started in the background keeps running.

`52d82f0a` keeps both, also in `process.killAndWait`, and no signal reaches another session (Linux and macOS, measured).

## The exit frame

A process that got the signal of this request gets an `exit` frame with `"killedBy": "client"`. On Linux and macOS a process that the signal ended has `"exitCode": -1` and the name of the signal:

```json
{"type": "stream", "processId": "p1", "stream": "exit", "seq": 3, "exitCode": -1, "signal": "SIGTERM", "killedBy": "client"}
```

A child that catches the signal and exits by itself gets its own exit code, and `killedBy` stays. A child that ended before the request gets no `killedBy`. Frame rules: [Stream notifications](streams.md#stream-notifications).

The `exit` frame waits for the output of the child. If another process holds the output pipe open, the frame comes up to 5 seconds after the child ended.

## Differences by system

=== "Linux and macOS"
    On Linux the signal to the direct child goes through a pid file descriptor, so it cannot reach another process. macOS and an old Linux kernel send it by pid. `KILL` goes to the group by its number on both. The skip of an ended child guards that case, except for `KILL` in the exit drain.

### Windows child trees

Since `52d82f0a` each child runs in a job, since `4c111e86` after a passed start test. A kill and `process.killAndWait` end the job at once, with exit code 1. `server.shutdown` ends the job too. A `taskkill /F` of the daemon ends the tree, but not with `-keep-children` ([CT-2](../DIVERGENCES.md#ct-2)). A process outside the job stays, also one that the child started with `CREATE_BREAKAWAY_FROM_JOB`. When the child exits, the rest of its job ends, and the `exit` frame comes at once. The start line (measured on Windows):

```
[daemon] job objects for child processes as the socket opens: true (off switch: false)
```

With `CLAUDE_SSH_NO_JOBS=1` the line ends `false (off switch: true)`. There is no job, and the shutdown and the exit of the child leave the tree. A kill ends the direct child only, as on `89cb6289`. claustrum starts a child suspended until it is in its job (claustrum's own, not measured).

The `exit` frame on Windows has no `signal` member:

```json
{"type": "stream", "processId": "c1", "stream": "exit", "seq": 1, "exitCode": 1, "killedBy": "client"}
```

With no job, a process that the child started can hold its output pipes. Then the `exit` frame comes 5 seconds after the kill. For the answers of `process.killAndWait` in that state, see [its page](process-killandwait.md#which-processes-the-force-reaches).

#### The start test of the jobs

Since `4c111e86` the daemon first runs its program with `--version` in a job (measured on Windows). As admin it passes. As a non-admin user it fails: the line ends `false (off switch: false)`, and each spawn answers `{"success":true,"outsideJob":true}`. Its child is in no job, so a kill ends the direct child only (from the code). At 1 minute the daemon ends the job (flags `0x2800`) of a hung test child, and the child and its conhost exit `0x1`.

From 5 s to that end the test is pending, and the socket opens. A spawn then answers `{"success":true,"outsideJob":true}`, as after a failed test (`4c111e86`, measured on Windows).

claustrum's close of the test job ends the rest: at the reap with code 0 (measured on Windows, claustrum's own), and at the daemon exit. There a stop of the daemon in the pending time ends the test child and its conhost with `0x0` (`4c111e86`, measured on Windows).

The resume can leave a thread of the test child with a suspend count above 1. Then the test fails at once with `starting it inside a Windows job object failed: a thread had been suspended <n> times, so it still sleeps`. `4c111e86` then ends the job, and the child exits `0x1` (measured on Windows, only with n = 2).

##### claustrum's own, not measured

- A clean exit before the end turns the jobs on.
- A failed start of the test child fails the test with its error.
- A failed join fails the test with `starting it inside a Windows job object failed: <error>`. With a child left asleep, the asleep text wins.
- At the bound, after a failed end, as with no job or no wait handle, a reap within 5 s gives the result. Else the test fails with the bound reason.
- At an asleep failure with no job the child stays asleep until the daemon exits. One with no wait handle can miss its end.
- Another error of the resume does not stop the test.
- The line rounds the time to the nearest millisecond.
- The end reaches nothing else.

## Rare states

??? note "A child that already ended"
    `process.kill` sends no signal to a child that already ended. That holds also before its `exit` frame, while the daemon collects its last output, with one exception. On Linux and macOS `"signal": "KILL"` then sends `SIGKILL` to the group at once, as `b3081b51` does (measured). If a live process has the pid of the ended child, that request sends nothing (claustrum's own, not measured). So a holder of the output pipe in the group ends, and the `exit` frame comes at once with no `killedBy`. A holder that left the group keeps the frame waiting up to 5 seconds. On Windows, and with a valid farewell on every system, that request sends nothing (claustrum's own, not measured). The result is `{"success": true}`. After the `exit` frame `52d82f0a` signals no group member either (measured on Linux and macOS).

## Differences from the reference

No entry of the [divergence catalog](../DIVERGENCES.md) changes this method.

The skip of a child that already ended guards against a reused process id (pid) or group number ([item 21 of the ledger](../IMPROVEMENTS.md)). For `KILL` claustrum follows the reference and accepts the risk of a reused group number. Before the `exit` frame a `TERM` of `b3081b51` reaches no process either (measured on Linux). On macOS and for `INT` and `HUP` that skip is claustrum's own, not measured.
