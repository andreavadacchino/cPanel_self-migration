#!/usr/bin/env bash

# Shared helpers for the release pipeline (release-intake / release-guard /
# post-merge-release). This project derives its version purely from the git tag
# at build time via GoReleaser ldflags (internal/version.Version), so there is NO
# in-repo version file to bump and NO manifest assertions here.

# Keep this regex identical to the SemVer check in release.yml so the two never
# disagree (allows vX.Y.Z and prereleases like vX.Y.Z-rc1 / vX.Y.Z-beta1).
RELEASE_TAG_REGEX='^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'

die() {
  echo "::error::$*" >&2
  exit 1
}

notice() {
  echo "::notice::$*"
}

is_release_tag() {
  [[ "${1:-}" =~ ${RELEASE_TAG_REGEX} ]]
}

validate_release_tag() {
  local tag="${1:-}"

  if ! is_release_tag "${tag}"; then
    die "Invalid release tag '${tag}'. Allowed formats are vX.Y.Z and vX.Y.Z-rc1/-beta1."
  fi
}

# Informational only: GoReleaser's `release.prerelease: auto` already marks
# prereleases from the -suffix, so nothing branches on this.
is_prerelease_tag() {
  [[ "${1:-}" == *-* ]]
}

# Success if the given commit is contained in (ancestor of, or equal to)
# origin/main. The caller MUST `git fetch origin main` first.
tag_commit_on_main() {
  git merge-base --is-ancestor "${1:-}" origin/main
}

extract_pr_marker() {
  local marker="${1:-}"

  python3 - "${marker}" <<'PY'
import os
import re
import sys

marker = sys.argv[1]
body = os.environ.get("PR_BODY", "")
pattern = rf"^<!-- {re.escape(marker)}: ([^<\n]+) -->$"
match = re.search(pattern, body, re.MULTILINE)
if not match:
    sys.exit(1)
print(match.group(1).strip())
PY
}

delete_remote_tag() {
  local tag="${1:-}"

  git push origin ":refs/tags/${tag}" || true
}
