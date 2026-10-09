# process.stdin

`process.stdin` sends bytes to the standard input of a child process that `process.spawn` started. With `offset`, a caller can send the same bytes again after a reconnect, and the daemon accepts them one time.

## Request

```json
{"id": "p1", "data": "aGVsbG8K", "offset": 0}
```

| Parameter | Required | Meaning |
|---|---|---|
| `id` | yes | The id of the process, as given to `process.spawn`. A request without `id` gets `Process not found`. |
| `data` | no | The bytes, in base64. Without `data`, the request sends no bytes. |
| `offset` | no | The position of the first byte of `data` in the stream of all bytes sent to this process, counted from 0. Without `offset`, the daemon appends `data`. |

One request line has a limit of 1 MiB: see [Transport](../PROTOCOL.md#transport). Send a large input in several requests.

## Response

```json
{"success": true, "applied": 6}
```

| Member | Meaning |
|---|---|
| `success` | Always `true` in a result. |
| `applied` | The number of bytes that the daemon accepted for this process, over all requests. It counts the bytes after the base64 decode. Each result has it. A count of `0` is in the result too. |
| `duplicate` | If `offset` is below `applied` and `data` ends at or before `applied`, `true`. The daemon then accepted no new byte. Each other result has no `duplicate` member. |

`applied` says that the daemon accepted the bytes. It does not say that the child read them. The daemon writes the bytes to the child from a queue, after the response. If the child exits or closes its input first, the daemon drops the bytes that are still in the queue, and no response says so. After the child closes its input, the daemon still answers success for later requests and raises `applied`, and it drops their bytes. No frame reports that.

## How `offset` works

The daemon compares `offset` with `applied`, the count before this request:

| Case | What the daemon does |
|---|---|
| No `offset`, or `offset` equal to `applied` | It accepts all of `data`. `applied` grows by the length of `data`. |
| `offset` above `applied` | It accepts nothing and answers `-32003`. Bytes are missing between the two positions. Send again from `applied`. |
| All of `data` is before `applied` | It accepts nothing and answers a result with `"duplicate": true`. `applied` does not change. |
| `data` starts before `applied` and ends after it | It accepts only the bytes from `applied` on. `applied` moves to the end of `data`. The result has no `duplicate` member. |

After a reconnect, read the count from the `stdinApplied` member of `process.reattach` and send from that position. A caller that never sends `offset` always appends. The daemon handles the `process.stdin` requests of one connection in the order of their arrival.

The daemon advertises this behavior as the feature `process.stdin.offset` in `server.capabilities`.

## Errors

This method has no `errorCode` member. A failure is a JSON-RPC error. The daemon makes the tests in the order of the table.

| Code | Text | Cause |
|---|---|---|
| `-32602` | `Invalid params` | The request has no `params` member, or `params` or a member of it has the wrong type. A negative `offset` gets this error too. See [the rules for `params`](../PROTOCOL.md#params-presence-and-typing). |
| `-32602` | `Invalid base64 data` | `data` is not valid base64. This test comes before the test of `id`. |
| `-32602` | `Process not found` | The daemon has no process with this `id`. |
| `-32003` | `stdin offset gap: offset ahead of applied bytes` | `offset` is above `applied`. |
| `-32602` | `Process not running` | The process exited, and the request is not a gap and not a duplicate. A request with no bytes, and with no `offset` or with `offset` equal to `applied`, gets this error too. The answer can come before the `exit` frame of the process. |
| `-32002` | `stdin backpressure: queue full` | The queue of the process holds bytes that the child did not read yet. This request does not fit in the limit of 16 MiB. The daemon accepts nothing, and `applied` does not change. Send the request again later. |

The tests for `offset` come before the test for a process that exited. So for a process that exited, a request with a gap still gets `-32003`. A request with only old bytes still gets `"duplicate": true`. That holds until the daemon forgets the process. Then each request gets `Process not found`.

The daemon never holds a request back because the queue is full. It answers `-32002` at once.

## Differences from the reference

claustrum is built to answer as the reference daemon does, and no entry of the divergence catalog applies to this method. Two rules of this page are from the code of claustrum, and no measurement against the reference covers them: the `Process not running` answer for a request with no bytes, and the behavior after the child closes its input.

## More detail

The measurements against the reference are in the [measurement record](../record/process-stdin.md). Most readers do not need that page.
