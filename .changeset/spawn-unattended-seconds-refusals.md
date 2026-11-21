---
default: patch
---

process.spawn now refuses an unattendedSeconds that is not a whole number, is negative, or comes with a pty object, a linkBound object, gitRead, cliTempRootEnd or a Claude Code start without --no-session-persistence, as the reference build 87375cc7 does; an accepted value does not end the child yet (claustrum's own).
