#!/usr/bin/env bash
# release-notes.sh <tag> — print the GitHub release body for <tag>, built from
# the annotated tag's own message.
#
# Why the tag message: it is the changelog someone actually wrote, and GitHub
# never copies it into a release (the API takes a release's body from a
# different field), so without this every release went out either blank or
# with only generated "What's Changed" notes — which in this repo list just
# the two PRs ever merged, not the hundreds of commits pushed to main.
#
# Output shape:
#
#   ## <tag subject>
#
#   <tag body, hard wraps joined>
#
#   **Full Changelog**: https://github.com/<repo>/compare/<prev>...<tag>
#
# Hard wraps are joined because a release body renders every newline as a
# line break: a tag message wrapped at 76 columns would otherwise show each
# bullet broken mid-sentence. A line starting a bullet ("- "), a heading, or
# following a blank line starts a new block; anything else continues the one
# before it.
#
# Prints nothing for a lightweight tag or an empty message. The workflow then
# falls back to GitHub's generated notes, so a quick tag still gets a body.
#
# GITHUB_REPOSITORY is set on Actions. Locally it is read from origin.
set -euo pipefail

tag="${1:?usage: release-notes.sh <tag>}"

# Only an annotated tag carries a message. For a lightweight tag the "contents"
# would be the commit's message, which is not a changelog.
if [ "$(git cat-file -t "refs/tags/$tag")" != "tag" ]; then
  exit 0
fi
subject="$(git for-each-ref "refs/tags/$tag" --format='%(contents:subject)')"
# contents:body excludes the subject; a signed tag's signature is cut off.
body="$(git for-each-ref "refs/tags/$tag" --format='%(contents:body)' | sed -e '/-----BEGIN PGP SIGNATURE-----/,$d')"
if [ -z "$subject" ]; then
  exit 0
fi

repo="${GITHUB_REPOSITORY:-$(git remote get-url origin | sed -E 's#^(git@|https://)github.com[:/]##; s#\.git$##')}"
# The previous tag reachable from this one. None means this is the first
# release, and there is nothing to compare against.
prev="$(git describe --tags --abbrev=0 "refs/tags/$tag^" 2>/dev/null || true)"

{
  printf '## %s\n\n' "$subject"
  printf '%s\n' "$body" | python3 -c '
import re, sys
out = []
for line in sys.stdin.read().splitlines():
    starts_block = not line.strip() or line.startswith(("- ", "* ", "#"))
    if out and out[-1].strip() and not starts_block:
        out[-1] = out[-1].rstrip() + " " + line.strip()
    else:
        out.append(line.rstrip())
print(re.sub(r"\n{3,}", "\n\n", "\n".join(out)).strip())
'
  if [ -n "$prev" ]; then
    printf '\n**Full Changelog**: https://github.com/%s/compare/%s...%s\n' "$repo" "$prev" "$tag"
  fi
}
