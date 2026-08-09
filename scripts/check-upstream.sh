#!/usr/bin/env bash
# check-upstream.sh — detect drift between claustrum and a reference claude-ssh
# build, identified by git SHA. Static check only (no daemon is started): it
# diffs method names, CLI flags, -version format, and the app-facing string set.
#
# Usage:  scripts/check-upstream.sh [<sha>]
#   <sha>  reference build SHA. Defaults to scripts/UPSTREAM_SHA.
#
# Requires: curl, zstd, go, strings (binutils). Network access to the CDN.
# Exit code: 0 = no drift in the checked surface, 1 = drift, 2 = setup error.
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
BASE_SHA="$(tr -d '[:space:]' < "$HERE/UPSTREAM_SHA" 2>/dev/null || true)"
SHA="${1:-$BASE_SHA}"
CDN="https://downloads.claude.ai/claude-ssh-releases"
PLAT="linux-amd64"

[ -n "$SHA" ] || { echo "no SHA given and scripts/UPSTREAM_SHA is empty" >&2; exit 2; }
for t in curl zstd go strings; do command -v "$t" >/dev/null || { echo "missing tool: $t" >&2; exit 2; }; done

echo "== reference SHA: $SHA  (pinned baseline: ${BASE_SHA:-none}) =="
if [ -n "$BASE_SHA" ] && [ "$SHA" != "$BASE_SHA" ]; then
  echo "NOTE: SHA differs from the pinned baseline — this is a NEW reference build."
fi

WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT

# 1) manifest + verified download
man="$WORK/manifest.json"
if ! curl -fsS --max-time 30 "$CDN/$SHA/manifest.json" -o "$man"; then
  echo "DRIFT/UNKNOWN: no manifest for $SHA (404?) — bad SHA or withdrawn build." >&2; exit 1
fi
want="$(grep -oE "\"$PLAT\"[^}]*\"checksum\":\"[0-9a-f]+\"" "$man" | grep -oE '[0-9a-f]{64}' | head -1)"
curl -fsS --max-time 120 "$CDN/$SHA/$PLAT/claude-ssh.zst" -o "$WORK/ref.zst" || { echo "download failed" >&2; exit 2; }
got="$(sha256sum "$WORK/ref.zst" | awk '{print $1}')"
if [ -n "$want" ] && [ "$got" != "$want" ]; then
  echo "CHECKSUM MISMATCH: manifest=$want actual=$got" >&2; exit 1
fi
zstd -dq -f "$WORK/ref.zst" -o "$WORK/ref" || { echo "zstd failed" >&2; exit 2; }
chmod +x "$WORK/ref"
echo "reference downloaded + checksum-verified ($(stat -c%s "$WORK/ref") bytes)"

# 2) build claustrum
( cd "$ROOT" && go build -o "$WORK/claustrum" . ) || { echo "claustrum build failed" >&2; exit 2; }

drift=0
note() { echo "  DRIFT: $*"; drift=1; }

# 3a) the 19 canonical methods must be present in BOTH binaries (membership check;
# `strings` can't reliably enumerate NEW methods because Go concatenates the string
# table — authoritative add/remove detection is the server.capabilities probe in
# the scratch/ battery).
CANON='server.ping server.version server.capabilities server.shutdown
files.list files.validate files.stat files.read files.extract_tar
git.info git.status git.list_branches git.worktree_create git.worktree_remove
process.spawn process.stdin process.kill process.killAndWait process.reattach'
refstr="$(strings -n 4 "$WORK/ref")"; ourstr="$(strings -n 4 "$WORK/claustrum")"
for m in $CANON; do
  grep -qF "$m" <<<"$refstr"  || note "canonical method missing from REFERENCE: $m (renamed/removed upstream?)"
  grep -qF "$m" <<<"$ourstr"  || note "canonical method missing from claustrum: $m"
done

# 3b) CLI flag set from -help (safe; no daemon)
flags() { "$1" -help 2>&1 | grep -oaE '^\s+-[a-z-]+' | tr -d ' ' | sort -u; }
if ! diff <(flags "$WORK/ref") <(flags "$WORK/claustrum") >/dev/null; then
  note "flag set differs:"
  diff <(flags "$WORK/ref") <(flags "$WORK/claustrum") | sed 's/^/    /'
fi

# 3c) -version format (token count / shape), ignoring the id value
vshape() { "$1" -version 2>&1 | sed -E 's/[0-9a-f]{40}/<SHA>/; s/[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:]+Z/<TS>/'; }
echo "  ref -version : $(vshape "$WORK/ref"   | sed 's/^[^ ]* /<NAME> /')"
echo "  ours-version : $(vshape "$WORK/claustrum" | sed 's/^[^ ]* /<NAME> /')"

# 3d) app-facing strings claustrum is known to match
missing=0
while IFS= read -r s; do
  [ -z "$s" ] && continue
  if strings -n 4 "$WORK/ref" | grep -qF "$s" && ! strings -n 4 "$WORK/claustrum" | grep -qF "$s"; then
    note "reference string not in claustrum: $s"; missing=$((missing+1))
  fi
done <<'EOF'
Unauthorized: invalid or missing auth token
Invalid JSON-RPC version
archivePath and destDir are required
destDir must be an absolute, non-root path: %q
branchName is required
files.read: path is a directory
files.read: file exceeds maxBytes
checksum mismatch: expected=%s, actual=%s
stdin offset gap: offset ahead of applied bytes
process.stdin.offset
[Server] Unauthorized request: method=%s, id=%v
[process.Manager] Process %s exited with code %d
[frameSink] replay write failed, detaching: %v
[shellenv] Extracted shell PATH (%d chars)
EOF

echo
if [ "$drift" -eq 0 ]; then
  echo "PASS: no drift in methods / flags / tracked strings for $SHA"
  echo "      (run the scratch/ battery for authoritative byte-for-byte parity)"
  exit 0
else
  echo "DRIFT DETECTED for $SHA — see notes above. Reconcile per docs/UPSTREAM-TRACKING.md."
  exit 1
fi
