#!/usr/bin/env bash
set -euo pipefail

version=${1:?usage: just release VERSION}
tag="v${version#v}"
if [[ ! $tag =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$ ]]; then
  printf 'Release version must be semantic, for example 1.2.3.\n' >&2
  exit 1
fi

for tool in git just goreleaser gpg; do
  command -v "$tool" >/dev/null || { printf '%s is required.\n' "$tool" >&2; exit 1; }
done

: "${GPG_KEY_ID:?Set GPG_KEY_ID to the fingerprint of the release signing key.}"
gpg --list-secret-keys "$GPG_KEY_ID" >/dev/null

if [[ -z ${GITHUB_TOKEN:-} ]]; then
  if [[ -n ${GH_TOKEN:-} ]]; then
    export GITHUB_TOKEN=$GH_TOKEN
  elif command -v gh >/dev/null; then
    GITHUB_TOKEN=$(gh auth token)
    export GITHUB_TOKEN
  else
    printf 'Set GITHUB_TOKEN or GH_TOKEN with GitHub contents:write access.\n' >&2
    exit 1
  fi
fi

if [[ -n $(git status --porcelain) ]]; then
  printf 'Commit all changes before releasing.\n' >&2
  exit 1
fi

if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
  printf 'Tag %s already exists.\n' "$tag" >&2
  exit 1
fi
if [[ -n $(git ls-remote --tags origin "refs/tags/$tag") ]]; then
  printf 'Tag %s already exists on origin.\n' "$tag" >&2
  exit 1
fi

just check

if [[ -n $(git status --porcelain) ]]; then
  printf 'The checks changed files. Commit them before releasing.\n' >&2
  exit 1
fi

# Build and sign every asset before the tag is pushed.
goreleaser release --snapshot --clean --skip=publish

git push origin HEAD
git tag -a "$tag" -m "Release $tag"
git push origin "$tag"
goreleaser release --clean
