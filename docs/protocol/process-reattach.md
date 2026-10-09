# process.reattach

`process.reattach` moves the output stream of a child process to the connection that sends the request. A client uses it to continue a session after a reconnect. The daemon sends the stored output frames that the client missed, and then the result.

## Request

```json
{"id": "p1", "fromSeq": 42}
```

| Parameter | Required | Meaning |
|---|---|---|
| `id` | yes | The id of the process, as given to `process.spawn`. |
| `fromSeq` | no | The `seq` of the last frame that the client has. The daemon sends the stored frames with a higher `seq`. Without a value, or with `0`, the daemon sends each frame that it still has. A `fromSeq` above `lastSeq` gets no stored frame: the stream still moves, and new frames come from `lastSeq` plus 1. |
| `wantPid` | no | With `true`, the result of a found process also has `pid` and `startTime`. This is an addition of claustrum: see [CT-1](../DIVERGENCES.md#ct-1). |

## Response

```json
{"found": true, "running": true, "firstSeq": 1, "lastSeq": 57, "stdinApplied": 6}
```

| Member | Meaning |
|---|---|
| `found` | If the daemon has a process with this `id`, `true`. |
| `running` | If the process did not exit yet, `true`. A `false` can come before the `exit` frame of the process: that frame then arrives later on this connection. |
| `firstSeq` | The `seq` of the oldest frame that the daemon still has for the process. For a process with no output yet, `firstSeq` and `lastSeq` are `0`. |
| `lastSeq` | The `seq` of the newest frame that the daemon has. The stream moved to this connection at that frame. |
| `stdinApplied` | The number of input bytes that the daemon accepted for the process. Send further input from this position: see [process.stdin](process-stdin.md). |
| `pid`, `startTime` | Only with `"wantPid": true` and a found process. They hold the values that `process.spawn` gives with `wantPid`, so a client can make sure that it has the same process. |

With `wantPid`, the result of a found process ends with `"pid": <integer>, "startTime": <number>`, after `stdinApplied`.

Each result has the first five members. If the daemon has no such process, the result is not an error:

```json
{"found": false, "running": false, "firstSeq": 0, "lastSeq": 0, "stdinApplied": 0}
```

`"found": false` does not always mean that the process never existed. The daemon keeps a process that exited for some time, and then it forgets the process and its stored frames. claustrum keeps it for 15 minutes after the `exit` frame, and for up to about one minute more. So after a long gap, read `"found": false` as "finished and forgotten". The daemon never forgets a process that still runs.

## What the request does

1. The daemon makes this connection the one receiver of the frames of the process. A reattach does not add a second receiver.
2. The daemon closes each other connection that received the frames before. It closes the whole connection, so that client also loses the streams of its other processes. A reattach on the connection that already receives the frames closes nothing.
3. The daemon sends the stored frames with a `seq` above `fromSeq` to this connection, in order.
4. The daemon sends the result.

New frames of the process go to this connection from step 1 on. No frame above `lastSeq` reaches an old connection. An old connection can still get a frame at or below `lastSeq` that the daemon was already writing.

From the code, and not measured: a new frame with a `seq` above `lastSeq` can arrive on this connection before the stored frames end, or before the result. Order the frames by `seq`. On a reattach from the connection that already receives the frames, a frame at or below `lastSeq` can arrive twice. Drop a frame whose `seq` you already have, from either connection.

## Frames that the daemon no longer has

The daemon stores at most 16 MiB of frames for each process and drops the oldest frames first: see [Stream notifications](../PROTOCOL.md#stream-notifications). So a replay can start later than `fromSeq` asks.

Compare `firstSeq` with the last `seq` that you have. If `firstSeq` is more than one above it, frames are lost, and the output of the process has a gap that no request can fill.

## What `stdinApplied` does not say

`stdinApplied` is an acknowledgement, not a delivery receipt. The daemon counts a byte at the moment that it accepts the byte. The child reads the byte later. Bytes that the daemon accepted just before the exit of the process are in the count although the child never got them. A client that must know that its input arrived needs an answer from the child itself.

## Errors

This method has no `errorCode` member. A failure is a JSON-RPC error.

| Code | Text | Cause |
|---|---|---|
| `-32602` | `Invalid params` | The request has no `params` member, or `params` or a member of it has the wrong type. A `fromSeq` that is negative or not a whole number gets this error too. See [the rules for `params`](../PROTOCOL.md#params-presence-and-typing). |
| `-32602` | `Process ID is required` | `id` is absent or empty. |

## Differences from the reference

claustrum is built to answer as the reference daemon does. One entry of the divergence catalog applies to this method:

- [CT-1](../DIVERGENCES.md#ct-1): the optional `wantPid` parameter with the members `pid` and `startTime`. A request without `wantPid` gets no such member.

The exact time that the reference keeps a process that exited is not known. The measurements bracket it to more than 45 seconds and at most 960 seconds. The 15 minutes of claustrum are its own choice inside that range, and so is the extra time of up to about one minute.

## More detail

The measurements against the reference are in the [measurement record](../record/process-reattach.md). Most readers do not need that page.
