---
default: patch
---

On Windows with no user excludes file, `git.status` passes `NUL` as `core.excludesFile`, as the reference does. With Git for Windows 2.55 the reply is then `-32603 "exit status 128"`, and D16 is retired.
