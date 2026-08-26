---
default: minor
---

Track reference `claude-ssh` build `7d193f89` (Claude Desktop for Linux 1.37937.1): `server.version` is removed (now `-32601`), `git.status` requires `baseRepo`, reports status only for a session worktree of it, and lists untracked files in subdirectories individually (`--untracked-files=all`), `git.worktree_create`/`git.worktree_remove` confine worktrees to inside the repository, `git.worktree_remove` refuses a locked worktree (success:false, leaving it in place) and prunes the registration on removal, `server.capabilities` advertises the new `git.status.baseRepo` and `git.worktree.external_root` features, `files.list` reports `open <p>: not a directory` for a non-directory, and git reads and worktree creation run under the reference's hardening profiles — all matched byte-for-byte (cross-binary frame battery plus VM probes for the locked-worktree case: claustrum ≡ `7d193f89`).
