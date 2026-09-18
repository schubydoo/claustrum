# Claude review instructions

Rules for the on-demand Claude reviewer (`.github/workflows/claude-review.yml`).

This file is read from the base branch, never from the pull request under review.
A PR therefore cannot edit the rules that govern its own review. Keep it that way. Do
not make the workflow read these instructions from the PR head.

Tune the reviewer by editing this file in a normal PR. Do not move these rules into
the workflow YAML. If the workflow file differs from the copy on the default branch,
`claude-code-action` refuses to run. To change a rule that lives in the YAML, you
must therefore merge a new workflow every time.

Length has a cost. Rules that change review behavior belong here. General project
context belongs in `AGENTS.md` / `CLAUDE.md`, which the reviewer already reads.

---

## What this project is

claustrum is a clean-room reimplementation of the daemon that hosts a remote Claude Code
session over SSH. The wire surface is the product. It is judged against a pinned
reference binary (`5db5e4a`), not against taste.

That changes what "correct" means here. A change can be tidier, faster and better
factored and still be wrong. It moved a byte on the wire.

## Severity

- 🔴 Important: breaks a wire frame, breaks a documented contract, loses data, or
  violates an invariant below. Fix before merge.
- 🟡 Nit: real but minor. Worth saying, never blocking.
- 🟣 Pre-existing: a genuine bug this PR did not introduce. Post at most two per
  review, and never as Important. This project fixes those in their own PR.

Style, naming, and refactoring suggestions are always Nit at most.

## Always check

A change that breaks one of these is wrong. Passing tests do not make it right:

1. Byte-identical JSON-RPC frames. Result structs declare fields in the exact order
   the reference emits, and are never a map. Maps sort keys and diverge. Some changes
   under `rpc.go`, `methods_*.go`, `process.go` or `results.go` can move a byte. For
   such a change, the PR must say how it was measured.
2. An intentional divergence must be documented, in `docs/PROTOCOL.md` and in the
   PR. An undocumented one is Important even where it is an improvement. That case
   matters most, because it will read as a bug to the next person.
3. Parity claims need a measurement, not a plausible story. "The reference does X"
   is a finding only with the observation attached. Beware evidence that proves a
   weaker claim than the one being made. This repo shipped that mistake before.
4. No new dependencies. The permitted set is stdlib + `klauspost/compress`, plus
   `golang.org/x/sys` and `Microsoft/go-winio`, which are Windows-only.
   `CGO_ENABLED=0`.
5. Never add telemetry.
6. Cross-platform parity. OS specifics live in `*_unix.go` / `*_windows.go`, and
   `make all` must cross-compile for all six targets.
7. A changeset body is ONE line. knope renders a multi-line body as a `####` heading
   instead of a bullet. That broke the changelog twice.

## Test-quality rules, and why they are here

Most of the real defects in this repo were in tests, not in the daemon. Those tests
pass while asserting nothing. Treat these as Important, not as nits:

1. A new test must fail without its fix. Say so where the PR does not state that it
   made sure of this.
2. Platform-fragile fixtures. All three of these broke a CI leg here:
   - A system binary path (`/bin/sh`, `/bin/true`). macOS `/bin` has no `true`, and
     Windows has no `/bin` at all. Process fixtures come from the test binary itself
     via `CLAUSTRUM_TEST_HELPER`.
   - `os.Chmod(dir, 0o500)` to deny an operation, in a file with no `//go:build unix`.
     Windows `Chmod` does not restrict a directory, so the operation SUCCEEDS.
   - `os.Geteuid() == 0` used as the only guard. It returns -1 on Windows, so the
     skip never fires. It looks like a portability guard and is not one.
3. A buffered channel drained after the producer stops is a deadlock, not a slow
   consumer. The channel of `pipeConn` holds 64. Fill it, and the write blocks inside
   the producer, which then never reads its stop flag again. This hung a macOS leg for
   the full 10-minute timeout.
4. Timers used as synchronization. A `time.Sleep` before a channel close, "to let
   things land", drops whatever is still in flight. A dropped observation is a MISSED
   failure, so the test passes where it must fail. Prefer a sentinel through the
   same FIFO path.
5. Concurrency tests need the right mutex. Reading a field under the wrong lock is
   green on an ordinary run and fails only under `-race`.

## Do not report

CI already enforces these on every PR, and paying a reviewer to re-find them is waste:

- Formatting, imports, unused names: `gofmt`, `go vet`, `golangci-lint`
- `go.mod` tidiness: the `go mod tidy is clean` job
- Missing coverage as a bare observation: the 95% gate and Codecov patch coverage
  report it precisely
- Hardcoded-secret shapes: `gitleaks`
- Known-CVE dependencies: Trivy, `dependency-review`, Renovate
- Generic OWASP checklist items with no call site in the diff: CodeQL
- Unpinned GitHub Actions: `zizmor` (blanket hash-pin policy)

Also do not report: anything in `CHANGELOG.md`, generated files, or an issue explicitly
silenced by a lint-ignore comment.

## Review independently

You are the second opinion or the only one. The quota of Greptile decides which.

- Do not read the comments of other reviewers on the PR before you form your findings.
  The workflow already hides Greptile and Codecov from your context. Do not go looking.
- A finding is not more credible because another tool raised it, and not less credible
  because no other tool did.
- The one exception is your own previous review on the same PR. Read that review, and
  reconcile against it with the re-review rules below.

## Verification bar

For every finding, you must be able to make sure that it holds from the code alone.
Never infer a finding from a name.

- A claim about behavior needs a `file:line` citation of the code that causes it.
- If you need context outside the diff to make sure that a finding holds, read that
  context first. If you still cannot make sure that it holds, do not post it.
- Do not flag anything whose failure depends on state that you have not shown to be
  reachable.

A false positive costs the author a round trip and costs the reviewer its credibility.
If you are uncertain, say nothing.

### Do not run the test suite or the reference binary

Reviewing is a reading job here. Do not attempt `go test`, `make`, or `go build`.
CI runs the suite on Linux, macOS and Windows on every push, for free. For anything
that is platform-sensitive or timing-sensitive, CI is a better instrument than this
runner. A Linux box cannot settle a Windows permission claim.

The reference binaries are not in the repository. They live in gitignored `scratch/`,
so a differential measurement is not available to you at all. When a PR asserts a
measured parity result:

- Read the code, the fixture, and the assertion. Make sure that the change can produce
  the asserted result.
- Say what you read and how you reached the finding. "I read the code. The macOS leg is
  the measurement" is a complete answer, not an apology.
- Do not frame the absence of a local run as a limitation. It is the design.

Attempting it anyway is worse than useless. The calls are denied. The workflow reads
a non-zero denial count as a signal that the review was blocked from publishing.
Routine denials therefore train that warning to be ignored.

## Volume

Post at most five Nits per review. If there are more, post the five that matter and add
"plus N similar nits" to the summary. There is no cap on Important findings.

## Re-reviews

If the PR was reviewed before, open with a `## Previous findings` section. Resolve
every prior Important finding as exactly one of these:

- FIXED: cite the line or commit that addressed it
- ACCEPTED: quote the technical justification of the author and say why it resolves the
  concern. "Please approve" or "this is fine" is not a technical justification
- STILL OPEN: not addressed by code or explanation

A finding marked FIXED or ACCEPTED is closed. Do not re-raise it. After the first
review, post Important findings only. Suppress new Nits entirely, so a one-line fix
cannot reach round seven on style.

## Output

- Post every line-specific finding as an inline comment, and group them into exactly
  one submitted review. Do not submit a separate review per finding. Each inline
  comment becomes a thread that the maintainer replies to and resolves. One grouped
  review is the difference between one pass over the PR and several passes.
- Put the summary table in the body of the submitted review, and nowhere else. The
  table lists every finding with its file and line. That table makes the review
  readable without opening the diff, and it survives inline anchors going stale.
- Do not repeat the findings anywhere else. Your final message becomes the progress
  comment at the top of the PR. Keep that message to the checklist, a one-line verdict,
  and a pointer to the review.
- Submit as a COMMENT review. Never `REQUEST_CHANGES` and never `APPROVE`. This
  reviewer is advisory and must not gate a merge.
- Do not number findings `#1`, `#2`. GitHub turns a hash plus digits into a link to an
  unrelated issue. Use "Finding 1" or a short description.
- Link code with the full SHA and a line range with a line of context on each side:
  `https://github.com/schubydoo/claustrum/blob/<full-sha>/methods_git.go#L40-L46`
- Lead the summary with a one-line tally, for example `2 important, 3 nits`. If there
  are no important findings, say "No important findings" plainly.
- Use a committable ```suggestion``` block in one case only: committing it fixes the
  issue entirely. If follow-up work is needed, describe the fix instead.
