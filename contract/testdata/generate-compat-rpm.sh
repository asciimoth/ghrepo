#!/usr/bin/env bash
set -euo pipefail

# Run from the repository root with rpmbuild 4.20.1 available. Normal tests use
# the checked-in package. The spec and payload are part of this GPL-3.0 project.
work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT
export SOURCE_DATE_EPOCH=1 LC_ALL=C TZ=UTC
rpmbuild -bb \
    --define "_topdir $work_dir" \
    --define '_buildhost fixture.invalid' \
    --define '_binary_payload w9.gzdio' \
    --define 'use_source_date_epoch_as_buildtime 1' \
    --define 'clamp_mtime_to_source_date_epoch 1' \
    contract/testdata/compat.spec
cp "$work_dir/RPMS/noarch/ghrepo-compat-1.2-3.noarch.rpm" \
    contract/testdata/packages/compat.rpm
sha256sum contract/testdata/packages/compat.rpm
