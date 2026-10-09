# process.stdin: measurement record

This page is a record, not reading material. Its text moved here from the protocol reference on 2026-10-09, unchanged. It holds the detailed rules of `process.stdin` and the measurements behind them, and it names the reference build of a measurement where the old text did. A new measurement of the method goes into this page. The exit-drain note that the text names is in [Stream notifications](../PROTOCOL.md#stream-notifications).

To use the method, read [process.stdin](../protocol/process-stdin.md).

`{id,data[,offset]}` → `{"success":true,"applied":<int>[,"duplicate":true]}`
- `data` is base64. The daemon writes it to the child's stdin.
- The tests run in a fixed order: decode, then exists, then offset, then running.
    - Invalid base64 → `-32602 Invalid base64 data`. The daemon returns this
      *before* it looks up the process, so an unknown id with a bad payload still
      reports the decode error.
    - Unknown id → `-32602 Process not found`.
    - The offset idempotency verdict is evaluated next, even for an exited
      process. An offset gap returns `-32003`, and a wholly-duplicate write
      returns `{"success":true,…,"duplicate":true}`, whatever the running state is.
    - A known process that exited or was already reaped → `-32602 Process not
      running`. This applies only when the write carries fresh bytes. Since
      `90fca6e6` the reap counts, not just the exit frame, so this also covers the
      drain window. See the exit-drain note under Stream notifications.
- `offset` and `applied` are the resumable-stdin contract. `7c2f88d` added them,
  and they are advertised as `process.stdin.offset`. The reply always carries
  `applied`, which is the cumulative count of stdin bytes accepted for delivery,
  the high-water mark. `offset` is the byte position the caller believes this
  `data` starts at. `offset` makes stdin idempotent across reconnects:
    - An absent `offset`, or `offset == applied`, makes the daemon append, and
      `applied` grows by `len(data)`.
    - `offset > applied` → `-32003 stdin offset gap: offset ahead of applied bytes`.
      If accepted, that gap drops input. Resend from `applied`. The daemon
      enqueues nothing.
    - `offset + len(data) <= applied`, which is wholly applied, is a no-op. The
      reply adds `"duplicate":true`, `applied` does not change, and nothing reaches
      the child.
    - A partial overlap (`offset < applied < offset+len`) makes the daemon write
      only the fresh tail `data[applied-offset:]`, and `applied` advances to
      `offset+len(data)`. The daemon does not flag this as a duplicate.
  `applied` counts base64-decoded bytes, and it is never `omitempty`, because the
  daemon emits it at 0. The daemon drops `duplicate` when it is false. A legacy
  client that never sends `offset` still works, because it always appends.
- Backpressure gives `-32002`. The per-process async stdin queue is bounded at
  16 MiB. The queue can be non-empty while a producer outruns a slow or non-reading
  child. When this write then pushes the queue past the cap, `process.stdin`
  returns `-32002 stdin backpressure: queue full` and enqueues nothing. `applied`
  does not change, so resend once the child drains. The request is rejected, never
  blocked. This is parity with `4534d86`, because the reference emits this frame at
  the same boundary, as measured by the probe `scratch/probe/stdincap`.
  A lone write larger than the whole cap on an empty queue is exempt. claustrum
  enqueues it rather than rejecting it. This is an internal edge, not a parity
  claim. A `data` field that large exceeds the 1 MiB request-line cap and closes
  the connection first. No wire client can therefore reach it on either daemon.
