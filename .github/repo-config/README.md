# Repo config baselines (advisory drift check)

Declarative baselines for the repository's **labels** and a few **basic
settings**, plus an advisory CI job that warns when the live GitHub config
drifts from what is committed here.

- `labels.json` — every label's `name`, `color`, and `description`. The file's
  on-disk order/formatting is not significant: the drift check normalises both
  the committed file and the live list (sort by name, sort keys) before
  comparing, so either compact or pretty JSON works.
- `settings.json` — `description`, `homepage`, `topics`, `has_issues`,
  `has_wiki`, `has_projects`, the three `allow_*_merge` flags, and
  `delete_branch_on_merge`.

## What runs

`.github/workflows/repo-config-drift.yml` on every PR (and on demand via
`workflow_dispatch`). It fetches the live labels + settings with `gh api` and
diffs them against the JSON here, printing any drift to the job summary.

## It is READ-ONLY and ADVISORY

- **Read-only.** The workflow only performs `gh api` GETs with the default
  `GITHUB_TOKEN`. It uses no secrets and writes nothing — not labels, not
  settings, not branch protection or rulesets (those are owned by the repo
  ruleset, see [`../rulesets/`](../rulesets/), and are intentionally out of
  scope).
- **Fork-safe.** Plain `pull_request` trigger (never `pull_request_target`) with
  `permissions: contents: read` (+ `issues: read` for the label API). A fork PR
  cannot exfiltrate or mutate anything.
- **Non-blocking.** The job always exits `0` (drift only prints a diff) and is
  additionally wrapped in `continue-on-error`. **Do not add it to the repo's
  required status checks** — it must never gate a merge.

The **apply / reconcile** half (writing labels and settings back to match these
baselines) is intentionally **not** built here: it needs a privileged App token
and is a maintainer-side action.

## Updating the baseline

When you intentionally change a label or setting on GitHub, refresh the JSON
here so the drift check goes quiet again:

```sh
# labels
gh label list --repo schubydoo/claustrum --json name,color,description \
  | jq 'sort_by(.name)' > .github/repo-config/labels.json

# settings (the exact field set the drift check compares)
gh api repos/schubydoo/claustrum --jq '{
  allow_merge_commit, allow_rebase_merge, allow_squash_merge,
  delete_branch_on_merge, description, has_issues, has_projects, has_wiki,
  homepage, topics
}' | jq -S '.' > .github/repo-config/settings.json
```

To push the committed baseline *to* GitHub (maintainer-side, needs a token with
`repo` scope):

```sh
# settings
gh api -X PATCH repos/schubydoo/claustrum \
  --input <(jq '{allow_merge_commit, allow_rebase_merge, allow_squash_merge,
                 delete_branch_on_merge, description, has_issues, has_projects,
                 has_wiki, homepage}' .github/repo-config/settings.json)
gh api -X PUT repos/schubydoo/claustrum/topics \
  --input <(jq '{names: .topics}' .github/repo-config/settings.json)

# labels (create-or-update each)
jq -c '.[]' .github/repo-config/labels.json | while read -r l; do
  name=$(jq -r .name <<<"$l"); color=$(jq -r .color <<<"$l")
  desc=$(jq -r .description <<<"$l")
  gh label create "$name" --repo schubydoo/claustrum --color "$color" \
    --description "$desc" --force
done
```
