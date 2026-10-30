---
default: minor
---

`process.kill` and `process.killAndWait` take a `farewell` for the stdin of the child and a `graceMs` before the signal, and their replies end with a `farewellEnd` member. The exit log line of a child that the daemon ends now says `asked to end by this daemon`.
