---
default: patch
---

On Linux and macOS a launcher that cannot create its socket folder now prints the mkdir error and exits 1, as 87375cc7 does.
