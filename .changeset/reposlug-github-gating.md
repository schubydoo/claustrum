---
default: patch
---

`git.info` emits `repoSlug` only for a canonical `github.com` remote whose owner and repo pass the reference's charset rules, not for any remote URL with two path segments, so GitLab, Bitbucket, self-hosted, and `www.github.com` remotes report `""`.
