---
default: patch
---

On Linux and macOS a process.kill with KILL in the exit drain of an ended child sends no group kill when a live process has its pid.
