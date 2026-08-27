---
default: minor
---

Track reference `claude-ssh` build `7d193f89`: a daemon restart hands the listening socket off cleanly — the successor rebinds a fresh socket inode and a departing predecessor no longer deletes it, so reconnecting sessions are not left with a broken socket.
