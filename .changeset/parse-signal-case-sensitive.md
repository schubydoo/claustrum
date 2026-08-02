---
default: patch
---

`process.kill` and `process.killAndWait` now match signal names case-sensitively and no longer map `QUIT` to `SIGQUIT`, so a request naming `quit` sends `SIGTERM` like the reference instead of a `SIGQUIT` that dumps core.
