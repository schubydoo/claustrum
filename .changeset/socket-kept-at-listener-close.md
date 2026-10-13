---
default: patch
---

On Linux and macOS a daemon that shuts down no longer removes the socket file that a new daemon bound at the same path.
