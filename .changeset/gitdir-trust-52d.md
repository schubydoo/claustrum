---
default: minor
---

The git-directory trust check now needs search access to `objects` and `refs` on Linux and macOS for a daemon that is not root, reads a `commondir` file as git does (only trailing CR and LF go, and a NUL ends the value), and names a damaged repository or an account that cannot open it in its refusal texts.
