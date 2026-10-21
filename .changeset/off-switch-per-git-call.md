---
default: patch
---

`git.info` now follows the off switch while it runs, as `5fd08069` does. A hold that starts during the request moves its later git commands to the git directory. A hold that ends lets a later command run the start test. `git.list_branches` does the same. A `git.info` with no repository, or on a missing path, waits for a running start test and answers with its state.
