---
default: patch
---

git.worktree_create now reproduces the reference's behaviour when the checkout leaves a pipe-holding descendant: it caps the post-checkout drain at a fixed ~5s from git's own exit and SIGKILLs the process group, then answers success (worktree kept) when the caller `timeoutMs` exceeds that drain, else `errorCode:"timeout"` ("deadline expired after the checkout finished") with the worktree rolled back — instead of blocking for the descendant's whole lifetime; the no-deadline default stays unbounded, matching the reference.
