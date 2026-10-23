---
default: patch
---

If the `-serve` launcher cannot rotate `remote-server.log`, it truncates its own log, as the reference does. Any link other than its own link to its own file, a FIFO, and on Linux and macOS another user's file, fail the start (D8). On Windows a second daemon truncates the held log.
