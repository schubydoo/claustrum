---
default: minor
---

On Linux and macOS, a spawn on a connection whose `server.adoptEnv` sent no `"noLoginShell":true` reads the login-shell PATH again in the background when the last read is 10 minutes old. Later spawns get the new PATH.
