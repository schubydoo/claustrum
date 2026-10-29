---
default: minor
---

A refused `process.spawn` start now carries an error `data` member `{"reason":"..."}` with `cwd_missing`, `cwd_unusable`, `launcher`, `not_found`, `not_executable` or `other`, and a failed start tests the `cwd` again.
