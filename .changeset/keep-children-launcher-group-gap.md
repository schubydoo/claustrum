---
default: patch
---

On Linux with `-keep-children`, the end of a launcher's group sends no SIGKILL after a shutdown that began before its last test.
