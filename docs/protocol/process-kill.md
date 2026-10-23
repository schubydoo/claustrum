# process.kill

`process.kill` sends a signal to a child process that `process.spawn` started. It answers at once and does not wait for the process to end. To wait for the end, use [process.killAndWait](process-killandwait.md).

A rule on this page that is not marked "claustrum's own" or "not measured" follows the reference build `89cb6289`, and the rules for Windows children that outlive the daemon follow `f6010b97` too.

## Request

```json
{"id": "p1", "signal": "TERM"}
```

| Parameter | Required | Meaning |
|---|---|---|
| `id` | no | The id of the process, as given to `process.spawn`. An absent, empty or unknown `id` signals nothing and gets the same result as a known one. |
| `signal` | no | `TERM`, `KILL`, `INT` or `HUP`, with or without the prefix `SIG`. The default is `TERM`. The names are case-sensitive, and each other value means `TERM`. So `quit` and `int` send `SIGTERM`. On Windows the daemon ignores the name: see [Differences by system](#differences-by-system). |

After a second `process.spawn` with the `id` of a process that still runs, the request reaches the newer process only. See [process.spawn](process-spawn.md#a-spawn-with-an-id-that-is-in-use).

## Response

```json
{"success": true}
```

The result is always `{"success": true}`. It does not say whether a process got the signal or ended. The `exit` frame of the stream says that.

## Errors

This method has no `errorCode` member. A failure is a JSON-RPC error.

| Code | Text | Cause |
|---|---|---|
| `-32602` | `Invalid params` | The request has no `params` member, or `params` or a member that it reads has the wrong type. See [the rules for `params`](../PROTOCOL.md#params-presence-and-typing). |

## Which processes a signal reaches

On Linux and macOS, each child runs in its own process group. Two cases:

- `KILL` goes to the whole process group of the child. So the processes that the child started end too, unless one left the group.
- Each other signal goes to the direct child only. A process that the child started in the background keeps running. A graceful `process.kill` does not end the tree. Use `"signal": "KILL"` for that.

On Windows each signal ends the direct child only: see [Windows child trees](#windows-child-trees).

`process.kill` sends no signal to a child that already ended. That holds also while the daemon still collects the last output of the child and did not yet send its `exit` frame. The result is `{"success": true}` all the same. On Linux and macOS, in that time `process.killAndWait` with `"signal": "KILL"` sends `SIGKILL` to the group at once, and `process.kill` does not.

## The exit frame

A process that got the signal of this request gets an `exit` frame with `"killedBy": "client"`. On Linux and macOS a process that the signal ended has `"exitCode": -1` and the name of the signal:

```json
{"type": "stream", "processId": "p1", "stream": "exit", "seq": 3, "exitCode": -1, "signal": "SIGTERM", "killedBy": "client"}
```

A child that catches the signal and exits by itself gets its own exit code, and `killedBy` stays. A child that ended before the request gets no `killedBy`. The full frame rules are in [Stream notifications](../PROTOCOL.md#stream-notifications).

The `exit` frame waits for the output of the child. If another process holds the output pipe open, the frame comes up to 5 seconds after the child ended.

## Windows child trees

On Windows a child is in no Job Object, and the kill goes to the direct child, never to its process group. A kill ends the direct child only, with exit code 1. The processes that the child started live on.

| Action | Direct child | Processes that the child started |
|---|---|---|
| `process.kill` with each `signal` value | ends, exit code 1 | live on |
| `process.killAndWait` with each form | ends, exit code 1 | live on |
| `-stop` and `server.shutdown` | ends, exit code 1 (without `-keep-children`, see [CT-2](../DIVERGENCES.md#ct-2)) | live on |
| `taskkill /F` of the daemon | lives on | live on |

A new daemon on the same socket does not end them either.

The `exit` frame on Windows has no `signal` member:

```json
{"type": "stream", "processId": "c1", "stream": "exit", "seq": 1, "exitCode": 1, "killedBy": "client"}
```

If a process that the child started holds the output pipes of the child, the `exit` frame comes 5 seconds after the kill. The reply of `process.kill` still comes at once. For the answers of `process.killAndWait` in that state, see [its page](process-killandwait.md#which-processes-the-force-reaches).

## Differences by system

| | Linux and macOS | Windows |
|---|---|---|
| The `signal` name | Selects the signal. | Ignored. Each value ends the direct child. |
| `KILL` | Goes to the process group of the child. | Ends the direct child only. |
| Each other signal | Goes to the direct child only. | Ends the direct child only. |
| `exit` frame of a killed child | `"exitCode": -1` and a `signal` member. | `"exitCode": 1` and no `signal` member. |
| Pid reuse | On Linux the signal to the direct child goes through a pid file descriptor, so it cannot reach another process. macOS and an old Linux kernel send it by pid. `KILL` goes to the group by its number on both. The skip of an ended child guards that case. | The daemon holds a handle of the child, so the kill cannot reach another process. |

## Differences from the reference

No entry of the [divergence catalog](../DIVERGENCES.md) changes this method.

The skip of a child that already ended is a guard of claustrum. The operating system can give the process id (pid) or the group number of a child that ended to another process. The reply is the same with or without the guard. Item 21 of the [ledger of completed work](../IMPROVEMENTS.md) has the guard. Whether the reference sends a signal to such a child is not measured.
