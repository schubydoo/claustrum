---
default: minor
---

On Linux the peer check refuses a caller with `out_of_resources` when a step runs out of files or memory or a user link is gone. `server.capabilities` lists `server.peer_check.out_of_resources` and `server.peer_check.absence_shown`.
