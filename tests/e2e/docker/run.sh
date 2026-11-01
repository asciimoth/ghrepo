#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT

cd "$repo_root"
docker info >/dev/null

# Build static Linux test binaries so the images need only reference tools.
GOOS=linux CGO_ENABLED=0 go test -c -o "$work_dir/apt.test" ./apt
GOOS=linux CGO_ENABLED=0 go test -c -o "$work_dir/rpm.test" ./rpm

docker build -q -f tests/e2e/docker/Dockerfile.debian -t ghrepo-e2e-debian tests/e2e/docker
docker build -q -f tests/e2e/docker/Dockerfile.rpm -t ghrepo-e2e-rpm tests/e2e/docker

for algorithm in rsa2048 ed25519; do
    docker run --rm \
        --mount "type=bind,src=$repo_root,dst=/src,readonly" \
        --mount "type=bind,src=$work_dir,dst=/tests,readonly" \
        --workdir /src/apt \
        --env GHREPO_APT_INSTALL=1 \
        --env "GHREPO_TEST_KEY_ALGORITHM=$algorithm" \
        ghrepo-e2e-debian /tests/apt.test -test.v -test.run '^TestAPTClientCompatibility$'

    docker run --rm \
        --mount "type=bind,src=$repo_root,dst=/src,readonly" \
        --mount "type=bind,src=$work_dir,dst=/tests,readonly" \
        --workdir /src/rpm \
        --env GHREPO_RPM_INSTALL=1 \
        --env "GHREPO_TEST_KEY_ALGORITHM=$algorithm" \
        ghrepo-e2e-rpm /tests/rpm.test -test.v -test.run '^TestCompatibility$'
done
