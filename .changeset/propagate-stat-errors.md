---
default: patch
---

Report a stat failure that is not "does not exist" — such as a path component that is a file — instead of answering `exists:false` from `files.stat`, `files.read` and `files.validate`.
