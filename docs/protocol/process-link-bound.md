# Link-bound processes

`process.spawn` with `linkBound` binds the process to the connection of the spawn, its link. Once the link is gone, the process ends after a time.

An untagged rule on this page is measured on Linux against build `683cc6a9`. macOS and Windows are not measured.

```json
{"id": "p1", "command": "/bin/cat", "linkBound": {"unattendedSeconds": 120}}
```

The result is `{"success":true}`, as for a plain spawn.

## The refusals

- With `launcher`, the spawn gets `-32602` `linkBound cannot be combined with pty or launcher`, with `"data":{"reason":"other"}`.
- With `"pty":true`, it gets `-32602` `Invalid params`, with no `data`.

claustrum's own: both tests come after the tests of `id` and `command`, and the `launcher` test comes first. A `linkBound` that is no object, or an `unattendedSeconds` that is no integer, gets `Invalid params` (not measured).

## The link

- The stream frames of the process go to the link only.
- [process.stdin](process-stdin.md) from another connection answers `Process not found`. The link writes as usual.
- [process.reattach](process-reattach.md) from another connection answers `"found": true` while the process runs, with the `firstSeq` and `lastSeq` of the link.
- The `exit` frame of a process that ends by itself has no `killedBy` and no `signal`.
- Of 200000 bytes in 7 frames, the daemon kept frames 6 and 7.

claustrum's own: a reattach from another connection gets the stored frames and no new frame, and the link stays open. The store holds 64 KiB. `process.kill` works from each connection.

## The unattended end

When the link closes, a clock starts.

| `unattendedSeconds` | Time |
|---|---|
| 0 | 60 s |
| 1 to 599 | That number of seconds |
| 600 or more | 600 s |

claustrum's own: an absent or negative value counts as 0.

At the end of the time the daemon logs this line. `<time>` is a Go duration text, such as `4s`, `1m0s` or `10m0s`.

```
[process.Manager] Process <id>: no connection attached for <time>; ending it
```

The ending line is followed by `SIGHUP`. A child that is still there gets `SIGKILL` about 7 s later. The exited line ends in `terminated by SIGHUP, asked to end by this daemon (unattended)`, or `terminated by SIGKILL, …` (`683cc6a9`, measured on Linux). claustrum sends the `SIGHUP` to the child alone (claustrum's own). No frame goes out, because no link is there.

claustrum's own: the end comes up to one step after the time, a quarter of the time and at most 60 s. The step starts at the spawn, and so does the clock of a link that is gone by then.

claustrum's own: on Linux and macOS the `SIGKILL` goes to the group, as for [process.kill](process-kill.md). On Windows the first signal ends the job of the child. A reattach does not stop the clock.

## The spent id

At the exit the daemon removes the record of the process. Then:

- `process.reattach` answers `"found": false`, and `process.kill` answers `{"success":true}`.
- A spawn with the `id` gets `-32602` `this id is in use, or was a terminal's or a link-bound process's: take a fresh one`, with `"data":{"reason":"other"}`. The command does not run.
- A spawn 931.8 s after the exit starts the child.

claustrum's own: the `id` is spent for 900 s, and the refusal logs the failed-start line. A spawn with the `id` of a link-bound process that runs gets the same error. The test comes after the `launcher` tests and before the `cwd` test.
