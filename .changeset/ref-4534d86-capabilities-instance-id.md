---
default: minor
---

`server.capabilities` now advertises a per-boot `instanceId` and `startedAt` (unix milliseconds) between `methods` and `features`, and appends the `server.instance_id` feature (always last), matching reference build 4534d86's instanceId, startedAt and server.instance_id additions on every OS.
