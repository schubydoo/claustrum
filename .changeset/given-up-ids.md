---
default: minor
---

`process.spawn` refuses an id that `process.kill`, `process.killAndWait` or `process.reattach` asked for while no process had it, for about 15 minutes, and `server.capabilities` lists `process.spawn.refuses_given_up_id`.
