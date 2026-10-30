---
default: minor
---

On Windows each spawned child now runs in a job. So `process.kill`, `process.killAndWait`, `server.shutdown` and the exit of the child end the child and the processes in its job at once. `CLAUDE_SSH_NO_JOBS=1` turns the jobs off, and the daemon removes that variable from its own environment on all systems, so no child gets the daemon value.
