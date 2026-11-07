---
default: minor
---

On Linux and macOS, after a `server.adoptEnv` with no `"noLoginShell":true`, the next spawn reads the login-shell PATH again in the background. It reads at most once a minute and stops for the day after 24 good reads, and later spawns get the new PATH.
