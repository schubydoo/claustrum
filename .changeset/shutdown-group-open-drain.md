---
default: patch
---

On Linux and macOS a shutdown now ends the process group of a plain child whose leader exited while a process still holds its output pipes, as the reference does.
