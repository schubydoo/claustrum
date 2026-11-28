---
default: minor
---

On Linux the daemon reads its unattended limit from `/etc/claude-desktop-ssh/settings.json` at the start, as 13047a51 does. A Claude Code spawn gets `-32010` while that file cannot be used.
