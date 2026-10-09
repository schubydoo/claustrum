# process.killAndWait

`process.killAndWait` sends a signal to a child process and waits for the process to end. If the process does not end in time, the daemon can end it by force. The result says what happened.

## Request

```json
{"id": "p1", "signal": "TERM", "timeoutMs": 5000}
```

| Parameter | Required | Meaning |
|---|---|---|
| `id` | yes | The id of the process, as given to `process.spawn`. |
| `signal` | no | The first signal: `TERM`, `KILL`, `INT` or `HUP`, with or without the prefix `SIG`. The default is `TERM`. The names are case-sensitive, and each other value means `TERM`. On Windows the daemon ignores the name: see [Differences by system](#differences-by-system). |
| `timeoutMs` | no | How long the daemon waits for the process to end after the signal, in milliseconds. The default is 3000. A value of `0` or below means the default. The daemon limits a larger value to 30000. |
| `escalate` | no | What the daemon does at the end of that time. With `true`, the default, it ends the process by force. With `false`, it leaves the process alone. |

The first signal reaches the same processes as a signal of [process.kill](../PROTOCOL.md#processkill).

## Response

```json
{"found": true, "died": true}
```

| Member | Meaning |
|---|---|
| `found` | If the daemon has a process with this `id`, `true`. |
| `died` | If the process ended and the daemon sent its `exit` frame, `true`. |
| `alreadyExited` | `true` in a result for a process whose `exit` frame went out before the request. The daemon then sent no signal. Each other result has no `alreadyExited` member. |
| `escalated` | `true` in a result of a request whose wait ran out, after which the daemon used the force. Each other result has no `escalated` member. |

The results:

| Result | What happened |
|---|---|
| `{"found": false, "died": false}` | The daemon has no such process. This is not an error. |
| `{"found": true, "died": true, "alreadyExited": true}` | The `exit` frame of the process went out before the request. |
| `{"found": true, "died": true}` | The process ended after the first signal, inside the wait. |
| `{"found": true, "died": true, "escalated": true}` | The process, or its output, did not end in time, and the daemon used the force. |
| `{"found": true, "died": false, "escalated": true}` | The daemon used the force, and the process, or its output, still did not end in the 7 seconds after that. |
| `{"found": true, "died": false}` | Only with `"escalate": false`: the wait ran out. The process still runs, or it ended and its output is not complete. |

The response comes at the end of the process and its output, or at the end of the wait, whichever is first. With the defaults that is up to 3 seconds, and up to 7 seconds more after the use of force.

## Which processes the force reaches

On Linux and macOS, the force is a `SIGKILL` to the process group of the child. So the processes that the child started end too, unless one left the group.

If the child ends inside the wait and its output is complete, the daemon sends no `SIGKILL`. A process that the child started then stays, and it can go on to run. Use `"signal": "KILL"` to end the whole group at once.

The output of a child is not complete while another process holds the output pipe open, in its group or not. The daemon waits at most 5 seconds for that output after the child ended. If the wait of the request ends first, the daemon uses the force although the child itself ended. If those 5 seconds end first, the answer is `{"found": true, "died": true}`, and the daemon sends no `SIGKILL`.

Windows has no process group: see [Differences by system](#differences-by-system).

## Errors

This method has no `errorCode` member. A failure is a JSON-RPC error.

| Code | Text | Cause |
|---|---|---|
| `-32602` | `Invalid params` | The request has no `params` member, or `params` or a member of it has the wrong type. See [the rules for `params`](../PROTOCOL.md#params-presence-and-typing). |
| `-32602` | `Process ID is required` | `id` is absent or empty. |

## Differences by system

On Windows the daemon ignores the signal name. Each first signal ends the direct child at once, with exit code 1. The force ends the direct child only too. See [Windows child trees](../PROTOCOL.md#windows-child-trees).

## Differences from the reference

claustrum is built to answer as the reference daemon does. No entry of the divergence catalog names this method yet. The case below can change a response, and the reference is not measured for it.

One case is claustrum's own choice, and no measurement of the reference covers it. For a short time after a child ends, the daemon still collects its last output and did not yet send the `exit` frame. A request in that time does not answer `alreadyExited`, and the daemon sends no first signal. If the `exit` frame comes inside the wait, the answer is `{"found": true, "died": true}`. If the wait ends first, the request goes on as for a child whose output is not complete.

That missing first signal is a guard of claustrum: the operating system can give the process id (pid) of a child that ended to another process. With `"signal": "KILL"` the guard also changes the answer. The other processes of the group then do not end at once, the wait can run out, and the answer has `"escalated": true`. Item 21 of the [ledger of completed work](../IMPROVEMENTS.md) has the guard for `process.kill`.

## More detail

The measurements against the reference are in the [measurement record](../record/process-killandwait.md). Most readers do not need that page.
