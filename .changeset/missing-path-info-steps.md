---
default: patch
---

A `git.info` on a path that does not exist now takes the steps of a folder with no repository up to the start of its config listing. That start fails on the missing folder before any process starts, and the frames stay the same.
