---
default: patch
---

The `git version` call after a failed config listing now keeps the daemon's `LC_ALL` and `LANGUAGE`, and the one after a failed `--attr-source` probe keeps the environment of the probe.
