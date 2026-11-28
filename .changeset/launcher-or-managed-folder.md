---
default: minor
---

`launcher.resolve` takes the `orManagedFolder` member of `launcherCheck` as 13047a51 does. It refuses that member with a `sha256` digest. On Linux, if nothing lies at `/etc/claude-desktop-ssh/launcher`, it checks the launcher in `/etc/claude-code`, and it lists `launcher.in_managed_folder`.
