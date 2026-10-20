---
default: patch
---

A git call now holds the settings-copy folder of its repository only while it runs. So the 128-folder limit can remove a folder between two calls of one request, as on `5fd08069` (Linux VM). A later call makes it again, and a call whose folder went right before its start fails with the text that `5fd08069` gives in `git.status`.
