---
default: minor
---

`files.read` no longer refuses a non-regular path by default, so reading a character device such as `/dev/null` returns `{"content":"","exists":true}` as the reference daemon does instead of a claustrum-only `-32602 "files.read: not a regular file"`; the guard is now opt-in via `-files-read-regular-only` or the `files-read-regular-only` key in `claustrum.conf`, which restores the bound on a writerless FIFO and on unbounded device reads.
