---
default: minor
---

`-install` no longer bounds the linux `ldd --version` libc probe at 5 seconds by default, so a slow-but-honest `ldd` is waited for and its answer stands instead of being killed into a fallback classification where the reference daemon showed no such deadline at the durations probed; the deadline is now opt-in via `-libc-probe-timeout` or the `libc-probe-timeout` key in `claustrum.conf`.
