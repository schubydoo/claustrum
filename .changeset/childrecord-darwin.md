---
default: minor
---

On darwin the daemon now records each spawned child to `<runDir>/children/<pid>.json`, which was linux-only before. It uses the darwin identity from reference build `19f30c46`: the node is the boot-session UUID, the host is the machine hostname, and the start-times are the process start as a UTC ANSIC timestamp. The on-disk record format stays byte-identical to linux, validated against the reference on a macOS VM.
