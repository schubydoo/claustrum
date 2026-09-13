---
default: minor
---

At startup the daemon now ends the process groups of children a since-exited predecessor daemon of the same run dir left orphaned. It first matches each live process to its record, so it does not end an unrelated process. This matches reference build `19f30c46`.
