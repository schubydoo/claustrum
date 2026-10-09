# process.killAndWait: measurement record

This page is a record, not reading material. Its text moved here from the protocol reference on 2026-10-09. Only its cross-references changed in the move. It holds the detailed rules of `process.killAndWait` and the measurements behind them, and it names the reference build of a measurement where the old text did. A new measurement of the method goes into this page.

To use the method, read [process.killAndWait](../protocol/process-killandwait.md).

`{id[,signal][,timeoutMs][,escalate]}` → `{"found":<bool>,"died":<bool>[,"alreadyExited":true][,"escalated":true]}`

Added by `7c2f88d`. It blocks until the process is gone, up to the grace, and
reports the outcome as a *result*. An unknown id is not an error:
- Missing `id` → `-32602 Process ID is required`. Absent `params` → `-32602 Invalid
  params`.
- Unknown id → `{"found":false,"died":false}`.
- Already exited → `{"found":true,"died":true,"alreadyExited":true}`. The daemon
  sends no signal.
- Inside the exit drain this method is the exception. Since `90fca6e6`,
  `process.reattach` and `process.stdin` answer as if the process is not running
  there. In claustrum, `killAndWait` still reads the flag that flips with the
  exit frame. A call inside the drain therefore answers `alreadyExited:false` and
  waits for the frame rather than reporting an already-exited process. No signal
  is delivered either, because the daemon refuses to signal a reaped process.
  Keeping this method unchanged is claustrum's choice. The reference answer inside
  the drain is not probe-measured.
- Live process → the daemon sends the graceful `signal`, `SIGTERM` by default, and
  then waits up to the grace:
    - `timeoutMs` sets the grace. A non-positive or absent value gives the
      3000 ms default. The daemon honors a positive value verbatim up to a
      30000 ms ceiling, and it clamps a larger value. `timeoutMs:45000` against a
      signal-ignoring child therefore answers after about 30 s. The `30000` ceiling
      is a black-box bracket `(29500, 30500]`. It is the only round value in that
      bracket, not a measured-exact figure.
    - `escalate` is `true` by default. If the process is still alive after the
      grace, `true` escalates to a process-group `SIGKILL`, waits up to 7 s
      for the reap, and adds `"escalated":true`. This is measured. `timeoutMs:500`
      against an unreapable child makes the reference reply at 7.51 s. The daemon
      sends the SIGKILL at the end of the grace also when the graceful signal
      already ended the child. A process in the group of the child that holds the
      stdout pipe keeps the drain pending past the grace. A Linux VM measured both
      cases against `89cb6289`: a child that ignores `SIGTERM` (row B2), and a
      child that exited with such a pipe holder in its group (row X1). In both,
      `kill(-<pid>, SIGKILL)` came about 3 s after the `SIGTERM`. `false` leaves the process running and reports
      `{"found":true,"died":false}`, with no `escalated` and no SIGKILL, which
      spares the tree. On Windows the escalation ends the direct child only. See
      [Windows child trees](../PROTOCOL.md#windows-child-trees).
- A process that dies within the grace → `{"found":true,"died":true}`, with no
  `escalated`. Its group gets no `SIGKILL`. On a Linux VM (rows X2a and X2b, by
  `strace`), the child exited on `SIGTERM`, and a kid in its group had its own
  stdio. `89cb6289` sent the `SIGTERM` to the one process and made no other call.
  That holds for `process.killAndWait` with the default (3 of 3 runs) and for a
  session supersede (4 of 4 runs). The kid was alive 5 s later. Build `ac5cadb`
  of claustrum sent `kill(-<pid>, SIGKILL)` right after the exit of the child,
  and the kid ended. claustrum is now built to those rows.
    - That group `SIGKILL` came from a run against `5db5e4a` with a child that
      backgrounds a sleeper. Whether the sleeper held the pipes of the child is
      not recorded.
    - Four more forms were equal on `89cb6289` and build `ac5cadb` (rows X2c to
      X2f). `escalate:false` and a plain `process.kill` sent the one `SIGTERM`.
      `process.kill` with `signal:"KILL"` and `-stop` sent one group `SIGKILL`.
    - macOS rows X2a to X2c give the same on `89cb6289` and claustrum: the kid
      lives, 3 of 3 runs each. Windows has no X2 row. There is no group there, so
      nothing changes.
