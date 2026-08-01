---
default: patch
---

`git.info` now emits `repoSlug` only for a canonical `github.com` remote whose owner and repo pass the reference's charset rules, instead of returning a slug for any remote URL with two path segments — so GitLab, Bitbucket, self-hosted, and `www.github.com` remotes now correctly report `""`.
