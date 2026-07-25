---
default: patch
---

Report a build time from `-version` on `go install pkg@version` builds

Such builds compile the tagged source from the module cache: they embed no
`vcs.time` setting, receive no `-ldflags`, and `debug.BuildInfo` has no timestamp
field — so `-version` printed `built unknown`. The release pipeline now bakes the
version and a UTC timestamp into the source it tags (`buildstamp.go`, written by
`knope prepare-release`), and the daemon reports it when the resolved module
version matches that release exactly. A pseudo-version install (`@main`,
`@<sha>`) still prints `unknown` rather than inheriting the previous release's
timestamp. `-ldflags` and `vcs.time` continue to take precedence, so release and
local-git builds are unaffected.
