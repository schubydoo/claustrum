---
default: minor
---

On Linux and macOS, a spawn on a connection that adopted with no `noLoginShell` reads the login-shell PATH again after 10 minutes. Later spawns get the new PATH.
