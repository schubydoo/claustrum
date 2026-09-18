# Repo config baselines (advisory drift check)

Declarative baselines for the labels of the repository and for a few basic
settings. An advisory CI job goes with them. If the live GitHub labels or
settings drift from what is committed here, that job warns.

- `labels.json`: the `name`, `color`, and `description` of every label. The
  on-disk order and formatting of the file are not significant. Before it
  compares, the drift check normalizes both the committed file and the live
  list. It sorts by name and sorts keys. Compact JSON and pretty JSON both work.
- `settings.json`: `description`, `homepage`, `topics`, `has_issues`,
  `has_wiki`, `has_projects`, the three `allow_*_merge` flags, and
  `delete_branch_on_merge`.

## What runs

`.github/workflows/repo-config-drift.yml` runs on every PR, and on demand
through `workflow_dispatch`. It fetches the live labels + settings with
`gh api`. It diffs them against the JSON here and prints any drift to the job
summary.

## It is READ-ONLY and ADVISORY

- Read-only. The workflow only performs `gh api` GETs with the default
  `GITHUB_TOKEN`. It uses no secrets and it writes nothing. It writes no labels,
  no settings, no branch protection and no rulesets. The repo ruleset owns
  branch protection and rulesets, see [`../rulesets/`](../rulesets/), and they
  are intentionally out of scope.
- Fork-safe. Plain `pull_request` trigger (never `pull_request_target`) with
  `permissions: contents: read` (+ `issues: read` for the label API). A fork PR
  cannot exfiltrate or mutate anything.
- Non-blocking. The job always exits `0`, because drift only prints a diff. The
  job is also wrapped in `continue-on-error`. Do not add it to the required
  status checks of the repo. It must never gate a merge.

The apply / reconcile half writes labels and settings back to match these
baselines. It is intentionally not built here. It needs a privileged App token,
and it is a maintainer-side action.

## Updating the baseline

When you intentionally change a label or setting on GitHub, refresh the JSON
here. The drift check then goes quiet again:

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

To push the committed baseline to GitHub (maintainer-side, needs a token with
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
