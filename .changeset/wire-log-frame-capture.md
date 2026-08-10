---
default: minor
---

The daemon can now append every JSON-RPC frame in both directions to a JSONL capture file for diagnostics, opt-in via `-wire-log` (with `-wire-log-max-string` bounding how much of each string value is kept, 0 for whole payloads) or the matching `wire-log` / `wire-log-max-string` keys in `claustrum.conf`, off by default and with the auth token always redacted; it is a pure side channel, so frames on the wire are byte-identical whether or not it is on.
