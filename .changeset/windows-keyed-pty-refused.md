---
default: patch
---

On Windows, `process.spawn` refuses a `pty` object with a valid key with `this host's helper cannot open a terminal`, as 4c111e86 and 87375cc7 do on the measured Windows host, and on all systems a `linkBound` spawn with a launcher logs its failed-start line.
