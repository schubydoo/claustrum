---
default: patch
---

On Windows a process.spawn with a sessionFoldersOff that is no bool, such as 1, now gets Invalid params, as the reference does.
