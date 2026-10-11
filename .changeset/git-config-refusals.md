---
default: minor
---

The five git methods refuse a repository config that the daemon does not accept, as `5fd08069` does. Two examples are a format version other than `0` or `1`, and a config file over 8 MiB. `"shadowDirOff":true` in a request turns that off.
