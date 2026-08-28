---
default: patch
---

`server.shutdown` now replies `{"ok":true}` before the daemon stops, matching the reference (a client that reads the shutdown reply previously saw EOF from claustrum); delivery races the teardown on both, and `-stop` still prints nothing.
