---
default: patch
---

On macOS the host cleaner no longer sends SIGTERM to a sibling daemon when `lsof` does not start or writes an error, as on `89cb6289`. It now removes an idle run folder whose unheld `daemon.lock` holds no valid record.
