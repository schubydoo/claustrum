---
default: minor
---

process.spawn now takes the hostCreds member as build 13047a51 does: a pipe at fd 99 on Linux, and a refusal on macOS and Windows.
