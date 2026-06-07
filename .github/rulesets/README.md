# Repository rulesets

Declarative source of truth for this repo's **branch and tag protection**. Each
file maps to one GitHub ruleset:

- `main.json` → ruleset **`main`** (branch). Protects the default branch:
  no deletion, no force-push, linear history, changes land via squash-only PRs
  with all review threads resolved, and three checks must pass (`strict` — the
  branch must be up to date first): `ci required checks passed`,
  `security required checks passed`, and `conventional PR title`.
- `tags.json` → ruleset **`protect-version-tags`** (tag). Makes `v*` release
  tags immutable: no deletion, no force-update.

These files are the **baseline**; the advisory
[`repo-config-drift`](../workflows/repo-config-drift.yml) workflow deliberately
does **not** read or reconcile them. Rulesets are applied to GitHub by the
maintainer via the API (below).

## ⚠️ Bootstrap order

The `main` ruleset requires PRs and linear history on the default branch. Apply
it **only after the default branch already exists on GitHub** (i.e. after the
first `git push`). Activating it on an empty repo blocks the very push that
would create `main`. Push first, then apply.

The required checks are the aggregator jobs `ci required checks passed`
([`ci.yml`](../workflows/ci.yml)) and `security required checks passed`
([`security.yml`](../workflows/security.yml)), plus `conventional PR title`
([`pr-title.yml`](../workflows/pr-title.yml)) — all integration `15368`
(GitHub Actions). Scorecard is deliberately not required (it never runs on
`pull_request`). If you rename an aggregator job, update `main.json` to match
or PRs can never go green.

## Applying

```sh
R=schubydoo/claustrum

# create (first time — AFTER the initial push)
gh api -X POST repos/$R/rulesets --input .github/rulesets/main.json
gh api -X POST repos/$R/rulesets --input .github/rulesets/tags.json

# update an existing ruleset (look up its id by name first)
id=$(gh api repos/$R/rulesets --jq '.[]|select(.name=="main")|.id')
gh api -X PUT repos/$R/rulesets/"$id" --input .github/rulesets/main.json
id=$(gh api repos/$R/rulesets --jq '.[]|select(.name=="protect-version-tags")|.id')
gh api -X PUT repos/$R/rulesets/"$id" --input .github/rulesets/tags.json

# verify: effective branch rules + every ruleset
gh api repos/$R/rules/branches/main
gh api repos/$R/rulesets --jq '.[]|{id,name,target,enforcement}'
```
