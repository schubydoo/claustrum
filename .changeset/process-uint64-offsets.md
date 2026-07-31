---
default: patch
---

Reject a negative `offset` on `process.stdin` and a negative `fromSeq` on `process.reattach`, which before silently dropped the first bytes of the child's stdin.
