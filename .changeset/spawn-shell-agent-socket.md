---
default: minor
---

`process.spawn` now hands a child the SSH agent socket that the user's login shell exports, matching reference build `f6010b97`. If the child env has no `SSH_AUTH_SOCK` entry, the daemon asks the login shell for one, with a 4-second deadline. The first spawn that needs a socket runs the shell, not the daemon startup. A socket that does not accept a connection is not used. The daemon caches the answer, asks the shell again at most every 10 minutes, and stops after 3 failed runs in a row. The new `disableShellAgentSocket` param skips the hand-off, and so does an `SSH_AUTH_SOCK` entry in the spawn env, even an empty one. `server.capabilities` advertises the feature as `process.spawn.shellAgentSocket` on every OS. A non-bool `disableShellAgentSocket` answers `-32602 Invalid params` on every OS. On Windows spawn runs no login shell and adds no `SSH_AUTH_SOCK`.
