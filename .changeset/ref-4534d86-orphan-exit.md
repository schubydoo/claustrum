---
default: minor
---

If a newer daemon takes over its socket path and no client is connected, a `-serve` daemon now shuts itself down. This matches reference build 4534d86. The daemon checks every 60 seconds and makes two self-probes after a 10-minute wait. Without orphan-exit the daemon stays up indefinitely, because the idle timeout closes only an idle connection and never the daemon.
