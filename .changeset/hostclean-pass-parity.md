---
default: patch
---

The host cleaner now follows the reference as measured on Linux and macOS: it reads run-dir ages before it dials a sibling. Its log prefix changes from `[process.HostClean]` to `[hostclean]`.
