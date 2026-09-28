---
default: patch
---

The host cleaner now follows the reference as measured on Linux and macOS: it reads run-dir ages before dialling any sibling, and does not dial or end, as stranded, a sibling that has `daemon.lock` open. Its log prefix changes from `[process.HostClean]` to `[hostclean]`.
