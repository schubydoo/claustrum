---
default: minor
---

`-install` no longer bounds the linux `ldd --version` libc probe at 5 seconds by default, so a slow `ldd`'s answer stands instead of being killed into a fallback classification — the reference daemon showed no such deadline against a stalled `ldd` at the durations probed; the deadline is now opt-in via `-libc-probe-timeout` or the `libc-probe-timeout` key in `claustrum.conf`.
