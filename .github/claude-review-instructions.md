# Claude review instructions

Rules for the on-demand Claude reviewer (`.github/workflows/claude-review.yml`).

**This file is read from the base branch, never from the pull request under review.**
A PR therefore cannot edit the rules that govern its own review. Keep it that way: do
not make the workflow read these instructions from the PR head.

Tune the reviewer by editing **this file** — a normal PR. Do not move these rules into
the workflow YAML: `claude-code-action` refuses to run when the workflow file differs
from the copy on the default branch, so instructions living in the YAML could only be
changed by merging a new workflow every time.

Length has a cost. Rules that change review behaviour belong here; general project
context belongs in `AGENTS.md` / `CLAUDE.md`, which the reviewer already reads.

---

## What this project is

claustrum is a clean-room reimplementation of the daemon that hosts a remote Claude Code
session over SSH. **The wire surface is the product.** It is judged against a pinned
reference binary (`5db5e4a`), not against taste.

That changes what "correct" means here. A change can be tidier, faster and better
factored and still be wrong, because it moved a byte on the wire.

## Severity

- **🔴 Important** — breaks a wire frame, breaks a documented contract, loses data, or
  violates an invariant below. Fix before merge.
- **🟡 Nit** — real but minor. Worth saying, never blocking.
- **🟣 Pre-existing** — a genuine bug this PR did not introduce. At most two per review,
  never Important; this project fixes those in their own PR.

Style, naming, and refactoring suggestions are **Nit at most**, always.

## Always check

A change that breaks one of these is wrong even if every test passes:

1. **Byte-identical JSON-RPC frames.** Result structs declare fields in the exact order
   the reference emits, and are **never a map** (maps sort keys and diverge). Any change
   under `rpc.go`, `methods_*.go`, `process.go` or `results.go` that could move a byte
   needs the PR to say how it was measured.
2. **An intentional divergence must be documented**, in `docs/PROTOCOL.md` and in the
   PR. An undocumented one is Important even when it is an improvement — especially
   then, because it will read as a bug to the next person.
3. **Parity claims need a measurement, not a plausible story.** "The reference does X"
   is a finding only with the observation attached. Beware evidence that proves a
   *weaker* claim than the one being made — this repo has shipped that mistake.
4. **No new dependencies.** stdlib + `klauspost/compress`, plus `golang.org/x/sys` and
   `Microsoft/go-winio` which are Windows-only. `CGO_ENABLED=0`.
5. **No telemetry, ever.**
6. **Cross-platform parity.** OS specifics live in `*_unix.go` / `*_windows.go`, and
   `make all` must cross-compile for all six targets.
7. **A changeset body is ONE line.** knope renders a multi-line body as a `####` heading
   instead of a bullet, which has broken the changelog twice.

## Test-quality rules, and why they are here

Most of this repo's real defects have been in *tests*, not in the daemon — tests that
pass while asserting nothing. Treat these as Important, not as nits:

1. **A new test must fail without its fix.** If the PR does not say it checked, say so.
2. **Platform-fragile fixtures.** All three of these have broken a CI leg here:
   - a system binary path (`/bin/sh`, `/bin/true`) — macOS `/bin` has no `true`, and
     Windows has no `/bin` at all. Process fixtures come from the test binary itself
     via `CLAUSTRUM_TEST_HELPER`.
   - `os.Chmod(dir, 0o500)` to deny an operation, in a file with no `//go:build unix`.
     Windows `Chmod` does not restrict a directory, so the operation SUCCEEDS.
   - `os.Geteuid() == 0` used as the only guard. **It returns -1 on Windows**, so the
     skip never fires. It looks like a portability guard and is not one.
3. **A buffered channel drained after the producer stops is a deadlock**, not a slow
   consumer. `pipeConn`'s channel holds 64; fill it and the write blocks inside the
   producer, which then never re-checks its stop flag. This hung a macOS leg for the
   full 10-minute timeout.
4. **Timers used as synchronisation.** A `time.Sleep` before a channel close, "to let
   things land", drops whatever is still in flight — and a dropped observation is a
   MISSED failure, so the test passes when it should fail. Prefer a sentinel through the
   same FIFO path.
5. **Concurrency tests need the right mutex.** Reading a field under the wrong lock is
   green on an ordinary run and only fails under `-race`.

## Do not report

CI already enforces these on every PR, and paying a reviewer to re-find them is waste:

- Formatting, imports, unused names — `gofmt`, `go vet`, `golangci-lint`
- `go.mod` tidiness — the `go mod tidy is clean` job
- Missing coverage as a bare observation — the **95%** gate and Codecov patch coverage
  report it precisely
- Hardcoded-secret shapes — `gitleaks`
- Known-CVE dependencies — Trivy, `dependency-review`, Renovate
- Generic OWASP checklist items with no call site in the diff — CodeQL
- Unpinned GitHub Actions — `zizmor` (blanket hash-pin policy)

Also do not report: anything in `CHANGELOG.md`, generated files, or an issue explicitly
silenced by a lint-ignore comment.

## Review independently

You may be the second opinion or the only one, depending on Greptile's quota.

- **Do not read other reviewers' comments on the PR** before forming your findings. The
  workflow already hides Greptile and Codecov from your context; do not go looking.
- A finding is not more credible because another tool raised it, nor less because it
  didn't.
- The one exception is your **own** previous review on the same PR — read that, and
  reconcile against it per the re-review rules below.

## Verification bar

Every finding must be checkable from the code, not inferred from a name.

- A claim about behaviour needs a `file:line` citation of the code that causes it.
- If confirming a finding needs context outside the diff, read that context first. If
  you still cannot confirm it, do not post it.
- Do not flag anything whose failure depends on state you have not shown to be
  reachable.

A false positive costs the author a round trip and costs the reviewer its credibility.
When uncertain, say nothing.

### Do not run the test suite or the reference binary

**Reviewing is a reading job here.** Do not attempt `go test`, `make`, or `go build`.
CI runs the suite on Linux, macOS and Windows on every push, for free, and for anything
platform- or timing-sensitive it is a *better* instrument than this runner — a Linux box
cannot settle a Windows permission claim.

The reference binaries are **not in the repository** (they live in gitignored `scratch/`),
so a differential measurement is not available to you at all. When a PR asserts a
measured parity result:

- Check that the change *could* produce it — read the code, the fixture, the assertion.
- Say what you verified and how. "Verified by reading; the macOS leg is the measurement"
  is a complete answer, not an apology.
- Do **not** frame the absence of a local run as a limitation. It is the design.

Attempting it anyway is worse than useless: the calls are denied, and the workflow reads
a non-zero denial count as a signal the review was blocked from *publishing* — so
routine denials train that warning to be ignored.

## Volume

At most **five Nits** per review. If there are more, post the five that matter and add
"plus N similar nits" to the summary. There is no cap on Important findings.

## Re-reviews

When the PR has been reviewed before, open with a `## Previous findings` section and
resolve every prior Important finding as exactly one of:

- **FIXED** — cite the line or commit that addressed it
- **ACCEPTED** — quote the author's technical justification and say why it resolves the
  concern. "Please approve" or "this is fine" is **not** a technical justification
- **STILL OPEN** — not addressed by code or explanation

A finding marked FIXED or ACCEPTED is closed. Do not re-raise it. After the first
review, post **Important findings only** — suppress new Nits entirely, so a one-line fix
cannot reach round seven on style.

## Output

- Post every line-specific finding as an **inline comment**, and group them into
  **exactly one submitted review**. Do not submit a separate review per finding: each
  inline comment becomes a thread the maintainer replies to and resolves, and one grouped
  review is the difference between one pass over the PR and several.
- Put the **summary table** — every finding with its file and line — in the **body of the
  submitted review**, and nowhere else. That table is what makes the review readable
  without opening the diff, and what survives inline anchors going stale.
- **Do not repeat the findings anywhere else.** Your final message becomes the progress
  comment at the top of the PR; keep it to the checklist, a one-line verdict, and a
  pointer to the review.
- Submit as a **COMMENT** review. Never `REQUEST_CHANGES`, never `APPROVE` — this
  reviewer is advisory and must not gate a merge.
- Do not number findings `#1`, `#2`. GitHub turns a hash plus digits into a link to an
  unrelated issue. Use "Finding 1" or a short description.
- Link code with the **full** SHA and a line range with a line of context either side:
  `https://github.com/schubydoo/claustrum/blob/<full-sha>/methods_git.go#L40-L46`
- Lead the summary with a one-line tally, e.g. `2 important, 3 nits`, and say "No
  important findings" plainly when that is the case.
- Use a committable ```suggestion``` block only when committing it fixes the issue
  **entirely**. If follow-up work is needed, describe the fix instead.
