---
default: minor
---

`process.kill` and `process.killAndWait` take a `farewell` for the stdin of the child and a `graceMs` before the signal, and their replies end with a `farewellEnd` member.
