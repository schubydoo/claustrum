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

One request line has a limit of 1 MiB: see [Transport](../PROTOCOL.md#transport). `data` is base64, so it costs a third more than the bytes that it carries. Send a large input in several requests, each well below that limit.

## Response

```json
{"success": true, "applied": 6}
```

| Member | Meaning |
|---|---|
| `success` | Always `true` in a result. |
| `applied` | The number of bytes that the daemon accepted for this process, over all requests. It counts the bytes after the base64 decode. Each result has it. A count of `0` is in the result too. |
| `duplicate` | If the request has `offset` and `data` ends at or before `applied`, `true`. The daemon then accepted no new byte. Each other result has no `duplicate` member. |

`applied` says that the daemon accepted the bytes. It does not say that the child read them. The daemon writes the bytes to the child from a queue, and it does not wait for that write before it answers. If the child exits or closes its input first, the child never gets the bytes in the queue, and no response says so. After the child closes its input, the queue keeps the bytes of later requests and `applied` grows, until the queue is full.

## How `offset` works

The daemon compares `offset` with `applied`, the count before this request:

| Case | What the daemon does |
|---|---|
| No `offset`, or `offset` equal to `applied` with some `data` | It accepts all of `data`. `applied` grows by the length of `data`. |
| `offset` above `applied` | It accepts nothing and answers `-32003`. Bytes are missing between the two positions. Send again from `applied`. |
| `offset` given and all of `data` at or before `applied` | It accepts nothing and answers a result with `"duplicate": true`. `applied` does not change. |
| `data` starts before `applied` and ends after it | It accepts only the bytes from `applied` on. `applied` moves to the end of `data`. The result has no `duplicate` member. |

After a reconnect, read the count from the `stdinApplied` member of `process.reattach` and send from that position. A caller that never sends `offset` always appends. The daemon handles the `process.stdin` requests of one connection in the order of their arrival.

The daemon advertises this behavior as the feature `process.stdin.offset` in `server.capabilities`.

## Errors

This method has no `errorCode` member. A failure is a JSON-RPC error. The daemon makes the tests in the order of the table.

| Code | Text | Cause |
|---|---|---|
| `-32602` | `Invalid params` | The request has no `params` member, or `params` or a member that it reads has the wrong type. A negative `offset` gets this error too. See [the rules for `params`](../PROTOCOL.md#params-presence-and-typing). |
| `-32602` | `Invalid base64 data` | `data` is not valid base64. This test comes before the test of `id`. |
| `-32602` | `Process not found` | The daemon has no process with this `id`. |
| `-32003` | `stdin offset gap: offset ahead of applied bytes` | `offset` is above `applied`. |
| `-32602` | `Process not running` | The process exited, and the request is not a gap and not a duplicate. A request with no bytes and no `offset` gets this error too. The answer can come before the `exit` frame of the process. |
| `-32002` | `stdin backpressure: queue full` | The queue of the process holds bytes that the child did not read yet. This request does not fit in the limit of 16 MiB. The daemon accepts nothing, and `applied` does not change. Send the request again later. After the child closes its input the queue does not drain, so each later request that does not fit gets this error (from the code). |

The tests for `offset` come before the test for a process that exited. So for a process that exited, a request with a gap still gets `-32003`. A request with only old bytes still gets `"duplicate": true`. That holds until the daemon forgets the process. Then each request gets `Process not found`.

The daemon never holds a request back because the queue is full. It answers `-32002` at once.

## Differences from the reference

claustrum is built to answer as the reference daemon does, and no entry of the divergence catalog applies to this method. `5fd08069` answers the same for a request with no bytes and after the child closes its input (rounds `tier3-03` and `tier3-12`, macOS and Linux VMs).
