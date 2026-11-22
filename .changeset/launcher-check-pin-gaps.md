---
default: patch
---

On Linux the launcherCheck of launcher.resolve and process.spawn now tests the write bits of an item before its owner, refuses the uid of the login as ownerUid, takes ownerUid 65534 only with a digest, and names a folder behind a link by its real path, as the reference does.
