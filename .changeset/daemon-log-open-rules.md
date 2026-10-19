---
default: patch
---

If the `-serve` launcher cannot rotate `remote-server.log`, it now truncates an own regular log, or the own file behind an own link, as `5fd08069` does. A folder, or a failed create with nothing at the path, fails the start as there. Any other link, a FIFO, and on Linux and macOS another user's file, fail the start with `permission denied` (D8). `5fd08069` writes through a link or another user's file and blocks on a FIFO, on macOS and on Linux with the `fs.protected` sysctls off.
