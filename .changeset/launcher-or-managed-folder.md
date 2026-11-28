---
default: minor
---

`launcher.resolve` takes the `orManagedFolder` member of `launcherCheck` as 13047a51 does. It refuses that member with a `sha256` digest. On Linux it lists `launcher.in_managed_folder`, and with `orManagedFolder` true, a `path` of `/etc/claude-desktop-ssh/launcher` and nothing there, it checks the launcher in `/etc/claude-code`.
