# The end member of a line

Every reply line and every stream frame line that the daemon writes on a connection ends with an `end` member. The member sits just before the closing brace of the top-level object. The rules below say which lines were measured. Most frame shapes on the other pages leave the member out.

```json
{"jsonrpc":"2.0","id":1,"result":{"pong":true},"end":{"at":0,"len":46}}
```

- `len` is the byte count of the line before `,"end"`. It is the length of the plain line minus its closing brace.
- `at` is the count of bytes that the daemon sent on that connection before the line. It counts each earlier LF and the digits of each earlier `end` member. It starts at `0` on each connection.
- Both rules hold for the ping, `process.spawn` and stream frame lines (build `52d82f0a`, measured on Linux, macOS and Windows).
- A second ping on the same connection gets `"at":72`. The first line has 71 bytes and a LF (build `52d82f0a`, measured on Linux, macOS and Windows).
- The `server.shutdown` reply carries the member too (build `52d82f0a`, measured on Linux, macOS and Windows).
- The refusal line of [the namespace check](server.md#a-refused-caller-linux) carries it at `0` (build `52d82f0a`, measured on Linux).

The parse error, the auth error and the replay lines of `process.reattach` carry it the same way (build `52d82f0a`, measured on Linux, macOS and Windows). The bridge relays lines as they are and adds nothing (from the code).
