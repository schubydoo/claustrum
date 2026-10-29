# The end member of a line

Every reply line and every stream frame line that the daemon writes on a connection ends with an `end` member. The member is the last member of the top-level object, just before the closing brace (build `52d82f0a`, measured on Linux, macOS and Windows). The frame shapes on the other pages leave it out.

```json
{"jsonrpc":"2.0","id":1,"result":{"pong":true},"end":{"at":0,"len":46}}
```

- `len` is the byte count of the line before `,"end"`. It is the length of the plain line minus its closing brace.
- `at` is the count of bytes that the daemon sent on that connection before the line. It counts each earlier LF and the digits of each earlier `end` member. It starts at `0` on each connection.
- A second ping on the same connection gets `"at":72`, because the first line has 71 bytes and a LF (build `52d82f0a`, measured on Linux, macOS and Windows).
- The `server.shutdown` reply carries the member too (build `52d82f0a`, measured on Linux, macOS and Windows).
- The refusal line of [the namespace check](server.md#a-refused-caller-linux) carries it at `0` (build `52d82f0a`, measured on Linux).

The parse error, the auth error and the lines of a `process.reattach` replay carry the member the same way. This is claustrum's own, not measured. So is the refusal line on macOS and Windows. The bridge relays lines as they are and adds nothing.
