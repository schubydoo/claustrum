---
default: minor
---

On Linux and macOS, `process.spawn` now passes the login shell's `SSH_AUTH_SOCK` to a child that has none, as reference build `f6010b97` does. The new `disableShellAgentSocket` param turns this off.
