---
default: patch
---

A predecessor's process group that outlives the start-up SIGKILL now logs `[process.Registry] WARNING group <pgid> survived SIGKILL; keeping its record for the next start`.
