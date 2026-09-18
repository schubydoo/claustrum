# Repository rulesets

Declarative source of truth for the branch and tag protection of this repo. Each
file maps to one GitHub ruleset:

- `main.json` → ruleset `main` (branch). It protects the default branch. There
  is no deletion, no force-push, and linear history. Changes land through
  squash-only PRs with all review threads resolved. Three checks must pass:
  `ci required checks passed`, `security required checks passed`, and
  `conventional PR title`. Those checks are `strict`, so the branch must be up
  to date first.
- `tags.json` → ruleset `protect-version-tags` (tag). It makes `v*` release
  tags immutable: no deletion, no force-update.

These files are the baseline. The advisory
[`repo-config-drift`](../workflows/repo-config-drift.yml) workflow deliberately
does not read them and does not reconcile them. The maintainer applies rulesets
to GitHub through the API (below).

## ⚠️ Bootstrap order

The `main` ruleset requires PRs and linear history on the default branch. Apply
it only after the default branch already exists on GitHub, that is, after the
first `git push`. On an empty repo the ruleset blocks the very push that
creates `main`. Push first, then apply.

The required checks are three aggregator jobs. They are
`ci required checks passed` ([`ci.yml`](../workflows/ci.yml)),
`security required checks passed`
([`security.yml`](../workflows/security.yml)), and `conventional PR title`
([`pr-title.yml`](../workflows/pr-title.yml)). All three are integration
`15368` (GitHub Actions). Scorecard is deliberately not required, because it
never runs on `pull_request`. If you rename an aggregator job, update
`main.json` to match. Otherwise PRs can never go green.

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
