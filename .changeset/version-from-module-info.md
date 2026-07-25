---
default: patch
---

Report the module version from `-version` for `go install pkg@version` builds

Such builds carry no `vcs.*` build settings — they compile from the module cache,
which has no VCS context — so the version fallback found nothing and `-version`
reported the `claustrum-dev` sentinel even for a binary installed at a real tag.
It now falls back to `debug.BuildInfo.Main.Version`, keeping the precedence
`-ldflags` > `vcs.revision` > module version > sentinel. Downstream version
checks that parse `-version` can now confirm a `go install` build's version.
