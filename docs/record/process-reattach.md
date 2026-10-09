# process.reattach: measurement record

This page is a record, not reading material. Its text moved here from the protocol reference on 2026-10-09, unchanged. It holds the detailed rules of `process.reattach` and the measurements behind them, and it names the reference build of a measurement where the old text did. A new measurement of the method goes into this page. The rules that the text names "under `process.stdin`" are on the page [process.stdin](../protocol/process-stdin.md), and the exit-drain note is in [Stream notifications](../PROTOCOL.md#stream-notifications).

To use the method, read [process.reattach](../protocol/process-reattach.md).

`{id,fromSeq[,wantPid]}` → `{"found","running","firstSeq","lastSeq","stdinApplied"}`
- A missing or empty `id` → `-32602 Process ID is required`. This is the same frame
  `spawn` and `killAndWait` document. Both `"id":""` and an absent `id` were
  probed.
- The daemon replays buffered frames with a seq above `fromSeq`, exclusive, to this
  connection. It transfers the frame stream to that connection, and then returns
  the result.
- The transfer is exclusive. A reattach does not add a second listener. Any
  connection attached before stops receiving frames for that process. This is what
  makes a resume safe.
- Since `90fca6e6` the transfer also CLOSES the connection it replaced. The
  daemon closes it once and logs a reason naming the process. Before `90fca6e6`
  the old connection stayed open. A client then held a connection that never
  carried another frame, and it had no sign that the session moved. This is
  measured. On
  `19f30c46` the old connection still answers `server.ping` after another
  connection reattaches, and on `90fca6e6` the next write to it fails. A reattach
  on the connection that is already attached closes nothing.
- Since `90fca6e6`, `running` is false for a reaped process. Inside the bounded
  exit drain the daemon already waited on the process but did not yet emit the
  exit frame. A reattach in that window answers `running:false`. See the
  exit-drain note under Stream notifications, and the same rule under
  `process.stdin`.
- The cut is by `seq`, not by wall-clock. The transfer point is the reported
  `lastSeq`. The old connection never receives a frame above it. It can still
  receive one `<= lastSeq`, from a write already in flight when the
  transfer took the process off it. Since `90fca6e6` that window is bounded by the
  supersede's close rather than lasting until the client hangs up. claustrum
  runs the close before it writes the reply. Whether a frame can still land on the
  old connection after the client reads the reply is unmeasured. No frame reaches
  the old connection and is also absent from the new connection's replay. That is
  what `fromSeq` is for.
- Unknown id → `{found:false,running:false,firstSeq:0,lastSeq:0,stdinApplied:0}`.
- The daemon retains an exited process for a bounded time and then drops it,
  together with its replay buffer. An id last seen longer ago therefore answers
  exactly like an unknown one, and `process.kill` on it still reports
  `{"success":true}`. The daemon never drops a running process. On the wire, the
  reference retention brackets only to `(45 s, 960 s]`. claustrum retains for 15
  minutes. It sweeps on a 60-second timer and inline on every `process.spawn`.
  Those two values are claustrum's own and are not probe-measured. Read `found:false` after a long gap as "finished and forgotten",
  not as "never existed".
- `stdinApplied` was added by `7c2f88d`. It is the process's cumulative
  applied-stdin byte count, as described under `process.stdin`. It is always
  present after `lastSeq`. A reconnecting client resumes stdin from this offset. It
  is an acknowledgement, not a delivery receipt. `process.stdin` returns before the
  child reads, so the daemon counts bytes accepted just before exit even though the
  writer never delivered them. A client that must know that data arrived makes sure
  of that in-band.
- `wantPid` is opt-in, CT-1. With `"wantPid":true`, and with the process found, the
  reply appends `"pid":<int>,"startTime":<number>` after `stdinApplied`. It
  reports the same pid and startTime the spawn reported. A client can therefore
  make sure that it reattached to the same process and not to a pid-reuse. The
  daemon omits both fields otherwise.
