---
default: patch
---

Fix `isSocketDead` so a daemon-handoff dial that fails with a wrapped `ENOENT` (the socket unlinked between the launcher's stat and its dial) is classified as a dead predecessor, closing a race that could stall the launcher until the daemon-start deadline.
