---
default: patch
---

On Windows a daemon that is killed hard now ends the processes in the job of each child, and with `-keep-children` they stay alive.
