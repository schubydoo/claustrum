# Stream frames of a process

This page gives the id-less stream frames that the daemon sends for a process: the `stdout`, `stderr` and `exit` frames, their `seq` numbers and the replay buffer. A process survives the disconnect of the connection that spawned it, and another connection picks it up with `reattach`.

An untagged rule on this page matches build `5fd08069` or an earlier build.

## Stream notifications

A `process.*` client supplies its own `id`, which is any string. The daemon
delivers output as id-less stream notifications, and it buffers them for a
later replay.

```jsonc
{"type":"stream","processId":"<id>","stream":"stdout","seq":1,"data":"<base64>"}
{"type":"stream","processId":"<id>","stream":"stderr","seq":2,"data":"<base64>"}
{"type":"stream","processId":"<id>","stream":"exit","seq":3,"exitCode":0}
{"type":"stream","processId":"<id>","stream":"exit","seq":3,"exitCode":-1,"signal":"SIGTERM","killedBy":"client"}
```

- `seq` is per-process. It starts at 1 and is monotonic across
  stdout/stderr/exit.
- `data` is base64 for stdout/stderr. The `exit` frame carries `exitCode` and no
  `data`. A signal-terminated child reports `exitCode: -1`, not `128+signo`.
- `signal` and `killedBy` appear on the `exit` frame only, both after `exitCode`,
  and both are omitempty, so a normal exit stays byte-identical. `signal` is the
  SIG-prefixed name of the terminating signal, such as
  `SIGTERM` or `SIGKILL`. It is omitted always on
  Windows (measured on Windows). `killedBy` names who asked the daemon to kill the
  process: `client` for `process.kill` and `process.killAndWait`, and `shutdown`
  for the shutdown and `killAll` sweep. It is emitted on every OS. A refused signal sets none (`52d82f0a`, Linux and macOS). Signal names
  other than `SIGTERM` and `SIGKILL` are not measured. The shutdown sweep sets
  `shutdown` only on a `process.killAndWait` that still waits. Linux measured it in the log. Another kill
  keeps `client` (not measured).
- The `exit` frame waits at most 5 seconds after the process exits for
  stdout/stderr to reach EOF. The daemon then closes the read ends and emits the
  frame anyway. This matters when a grandchild holds the same pipe, as
  `npm run dev &` does. Output that the grandchild writes after the cap is lost,
  because that write fails `EPIPE`.
- Inside that drain window `process.reattach` reports `running: false`. claustrum
  flips it at the reap. The reference is measured `false` one second into the
  drain.
- Inside the same window `process.stdin` answers `-32602 Process not running` for
  a write that carries fresh bytes, so `applied` and `stdinApplied` do not count
  bytes that the closed pipe discarded. An offset gap still answers `-32003`, and
  a wholly duplicate write still answers `{"success":true,…,"duplicate":true}`.
  The acknowledgement caveat of `stdinApplied` (see
  [process.reattach](process-reattach.md)) still applies to a write
  accepted while the process was live.
- Each stdout/stderr frame carries at most one 32 KiB read. Larger output splits
  across frames. Concatenate `data` in `seq` order to reassemble it. The exact
  frame *boundaries* depend on pipe scheduling and are not stable. Only the
  reassembled bytes are stable.
- The replay buffer has a bound of 16 MiB per process. The daemon counts the
  serialized frame including its trailing newline, which is the bytes a subscriber
  receives, not the base64 `data` alone. An exit frame therefore costs its envelope
  although it carries no `data`. The daemon drops frames oldest-first, whole frames
  at a time, once adding a new frame pushes the buffer total over the cap. It always retains at least one
  frame, even a frame larger than the cap. `reattach{fromSeq:0}` therefore replays
  everything still retained, and not necessarily everything ever emitted.
  `firstSeq` is the floor. Compare it against the last `seq` you saw to detect a
  gap.
- A process survives the disconnect of the connection that spawned it. Another
  connection picks it up with `reattach`. This is the multi-attach and reconnect
  mechanism.
