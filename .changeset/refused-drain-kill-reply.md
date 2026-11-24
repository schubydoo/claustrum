---
default: patch
---

On Linux and macOS a refused group SIGKILL of a `process.kill` `KILL` to an ended child now answers the `-32603` error, as 87375cc7 does.
