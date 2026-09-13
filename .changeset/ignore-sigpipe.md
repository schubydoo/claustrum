---
default: minor
---

The `-probe-cli` and `-install` modes now ignore SIGPIPE, so a closed stdout no longer kills them mid-write, matching reference build `19f30c46`.
