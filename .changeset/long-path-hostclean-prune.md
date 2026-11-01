---
default: minor
---

On Linux `plugins.prune` and the host cleaner reach a socket path over 107 bytes. A dead daemon there no longer stops the legacy sweep, and its old run dir can go. A long sibling that cannot be reached gives the limit text in place of `connect: invalid argument`.
