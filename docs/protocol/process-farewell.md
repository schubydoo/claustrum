# The farewell of a kill

`process.kill` and `process.killAndWait` take two optional members, `farewell` and `graceMs`. The daemon writes the farewell to the stdin of the child. The signal comes after `graceMs`, unless the child ends first.

An untagged rule on this page matches build `52d82f0a`, measured on Linux and macOS.

```json
{"id": "p1", "signal": "TERM", "farewell": "aGkK", "graceMs": 2000}
```

- `farewell` is the base64 of at most 65536 bytes.
- `graceMs` is an integer. The daemon limits a larger value to 30000 (measured on Linux).
- The farewell does not move the stdin offset of `process.stdin` (claustrum's own, not measured).

## The rules

The checks run in this order (measured on Linux). A failure logs `[process] kill <id>: its farewell is not said: <rule>`, and the signal goes at once.

1. `of the wrong type`: `farewell` is no string, or `graceMs` is no integer, such as `1.5`.
2. `a farewell with no graceMs`
3. `graceMs with no farewell`
4. `not base64`
5. `over the cap`: over 65536 bytes after the decode.

A `null` member and a negative `graceMs` count as absent. `""` with `0` counts as no farewell (measured on Linux). Each other `""` or `0` counts as absent (claustrum's own, not measured).

## The farewellEnd member

The reply gets `farewellEnd` as its last member. A request with no farewell gets none.

| Value | Method | What happened |
|---|---|---|
| `queued` | `process.kill` | The reply comes at once. The signal follows after `graceMs`. |
| `left` | `process.killAndWait` | The child ended inside `graceMs`. The reply has no `escalated`. |
| `expired` | `process.killAndWait` | `graceMs` ran out. Then the signal and `timeoutMs` run as without a farewell. |
| `cut` | `process.killAndWait` | Another kill ended the farewell. The reply comes at once, with `"died": true`. |
| `unsent` | both | The `id` is unknown, or the process got a signal already. The signal goes at once. |
| `invalid` | both | A rule above failed. |

An example is `{"found":true,"died":true,"escalated":true,"farewellEnd":"expired"}`.

## The end of the child

- The `exit` frame has `"killedBy": "client"` from the farewell on. A child that exits with code 0 gets no `signal` member.
- Another kill of the process cuts a pending farewell and signals at once.
- After `server.shutdown` a waiting `process.killAndWait` gets no reply and no `exit` frame. Its connection ends.

Not measured, claustrum's own:

- Windows, and `unsent` for a second farewell on a pending one.
- `invalid` on `process.killAndWait`: the kill goes at once. `invalid` also for an unknown `id` on `process.kill`.
- `cut` for a child that outlives the signal of the cut.
- Under `-keep-children` a shutdown sends the signal of each pending farewell at once.
