---
default: minor
---

`-install` prunes older CLI version files before the fetch of a cache miss, also when the install then fails. After a full disk it keeps one version file and reports the removes of that prune as `cliRemoved`.
