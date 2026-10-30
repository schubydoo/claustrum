# process.kill

`process.kill` sends a signal to a child process that `process.spawn` started. It answers at once and does not wait for the process to end. To wait for the end, use [process.killAndWait](process-killandwait.md).

An untagged rule on this page matches build `89cb6289`.

## Request

```json
{"id": "p1", "signal": "TERM"}
```

| Parameter | Required | Meaning |
|---|---|---|
| `id` | no | The id of the process, as given to `process.spawn`. An absent, empty or unknown `id` signals nothing. |
| `signal` | no | `TERM`, `KILL`, `INT` or `HUP`, with or without the prefix `SIG`. The default is `TERM`. The names are case-sensitive, and each other value means `TERM`. So `quit` and `int` send `SIGTERM`. On Windows the daemon ignores the name: see [Differences by system](#differences-by-system). |
| `farewell`, `graceMs` | no | The [farewell](process-farewell.md) for the stdin of the child, and the wait before the signal. |

After a second `process.spawn` with the `id` of a process that still runs, the request reaches the newer process only. See [process.spawn](process-spawn.md#a-spawn-with-an-id-that-is-in-use).

## Response

```json
{"success": true}
```

Without a farewell the result is `{"success": true}`. A farewell adds `farewellEnd`: see [Farewell](process-farewell.md). The `exit` frame says whether a process got the signal or ended.

## Errors

This method has no `errorCode` member. A failure is a JSON-RPC error.

| Code | Text | Cause |
|---|---|---|
| `-32602` | `Invalid params` | The request has no `params` member, or `params` or a member that it reads has the wrong type. See [the rules for `params`](../PROTOCOL.md#params-presence-and-typing). |

## Which processes a signal reaches

On Linux and macOS, each child runs in its own process group. Two cases:

- `KILL` goes to the whole process group of the child. So the processes that the child started end too, unless one left the group.
- Each other signal goes to the direct child only. A process that the child started in the background keeps running. A graceful `process.kill` does not end the tree. Use `"signal": "KILL"` for that.

On Windows a kill ends the job of the child (`52d82f0a`): see [Windows child trees](#windows-child-trees).

## The exit frame

A process that got the signal of this request gets an `exit` frame with `"killedBy": "client"`. On Linux and macOS a process that the signal ended has `"exitCode": -1` and the name of the signal:

```json
{"type": "stream", "processId": "p1", "stream": "exit", "seq": 3, "exitCode": -1, "signal": "SIGTERM", "killedBy": "client"}
```

A child that catches the signal and exits by itself gets its own exit code, and `killedBy` stays. A child that ended before the request gets no `killedBy`. The full frame rules are in [Stream notifications](streams.md#stream-notifications).

The `exit` frame waits for the output of the child. If another process holds the output pipe open, the frame comes up to 5 seconds after the child ended.

## Differences by system

=== "Linux and macOS"
    The `signal` name selects the signal. `KILL` and each other signal reach the processes in [Which processes a signal reaches](#which-processes-a-signal-reaches).

    On Linux the signal to the direct child goes through a pid file descriptor, so it cannot reach another process. macOS and an old Linux kernel send it by pid. `KILL` goes to the group by its number on both. The skip of an ended child guards that case.

=== "Windows"
    The `signal` name is ignored. A kill ends the job of the child (`52d82f0a`): see [Windows child trees](#windows-child-trees).

### Windows child trees

Since `52d82f0a` each child on Windows runs in a job. A kill and `process.killAndWait` end the whole job at once, with exit code 1. `server.shutdown` ends the job too. A process outside the job stays. When the child exits, the rest of its job ends, and the `exit` frame comes at once. The daemon logs one start line (measured on Windows):

```
[daemon] job objects for child processes as the socket opens: true (off switch: false)
```

With `CLAUDE_SSH_NO_JOBS=1` the line ends `false (off switch: true)`. There is no job, and the shutdown and the exit of the child leave the tree. A kill ends the direct child only, as on `89cb6289`. claustrum starts a child suspended until it is in its job (claustrum's own, not measured). A `taskkill /F` of the daemon ends no child, as on `89cb6289` (not measured on `52d82f0a`).

The `exit` frame on Windows has no `signal` member:

```json
{"type": "stream", "processId": "c1", "stream": "exit", "seq": 1, "exitCode": 1, "killedBy": "client"}
```

With no job, a process that the child started can hold the output pipes of the child. Then the `exit` frame comes 5 seconds after the kill. The reply of `process.kill` still comes at once. For the answers of `process.killAndWait` in that state, see [its page](process-killandwait.md#which-processes-the-force-reaches).

## Rare states

??? note "A child that already ended"
    `process.kill` sends no signal to a child that already ended. That holds also while the daemon still collects the last output of the child and did not yet send its `exit` frame. The result is `{"success": true}` all the same.

    On Linux and macOS, in that time `process.killAndWait` with `"signal": "KILL"` sends `SIGKILL` to the group at once, and `process.kill` does not.

## Differences from the reference

No entry of the [divergence catalog](../DIVERGENCES.md) changes this method.

The skip of a child that already ended is a guard (claustrum's own). The operating system can reuse the process id (pid) or the group number of an ended child. The reply does not change. Item 21 of the [ledger of completed work](../IMPROVEMENTS.md) has the guard. Whether the reference signals such a child is not measured.
