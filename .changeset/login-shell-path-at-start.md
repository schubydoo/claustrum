---
default: minor
---

On Linux and macOS the daemon now reads the login-shell `PATH` in the background at its start, unless `CLAUDE_SSH_NO_LOGIN_PATH_WARM=1`. A spawn waits for that read at most 4 s, and a later spawn tries a failed read again after 30 s and then after longer pauses. A spawn with `"disableShellAgentSocket":true` and the shutdown end a read that still runs. The read uses a fresh sentinel, caps the shell output and logs byte counts instead of shell output, as does the SSH agent hand-off. That hand-off runs no login shell while the read runs. On Windows the daemon now takes its own `PATH` at its start and removes that variable too (not measured against the reference).
