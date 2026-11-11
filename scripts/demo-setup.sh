#!/usr/bin/env bash
# Source this file in the VHS shell. All demo state is removed on shell exit.
demo_project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
demo_root="$(mktemp -d)"
trap 'rm -rf "$demo_root"' EXIT
export GHREPO_DEMO_FIXTURES="$demo_project_root/contract/testdata/packages"
export GHREPO_REPO_DIR="$demo_root/repository"
export GNUPGHOME="$demo_root/gnupg"
mkdir -p "$demo_root/bin" "$GHREPO_REPO_DIR" "$GNUPGHOME"
chmod 700 "$GNUPGHOME"
go build -o "$demo_root/bin/ghrepo" "$demo_project_root/scripts/demo/ghrepo"
export PATH="$demo_root/bin:$PATH"
gpg --batch --pinentry-mode loopback --passphrase '' \
  --quick-generate-key 'Demo Repository <demo@example.org>' ed25519 sign 0 >/dev/null 2>&1
git init -q -b main "$GHREPO_REPO_DIR"
git init -q --bare "$demo_root/remote.git"
git -C "$GHREPO_REPO_DIR" remote add origin "$demo_root/remote.git"
git -C "$GHREPO_REPO_DIR" config user.name 'Demo Repository'
git -C "$GHREPO_REPO_DIR" config user.email 'demo@example.org'
cd "$GHREPO_REPO_DIR"
