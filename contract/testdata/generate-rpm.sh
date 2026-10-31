#!/usr/bin/env bash
set -euo pipefail

# Copy pinned Apache-2.0 fixtures from the go-rpmutils module. Run from the
# repository root. Normal tests read the checked-in copies and do not run this.
module_dir=$(go list -m -f '{{.Dir}}' github.com/sassoftware/go-rpmutils)
testdata_dir="$module_dir/testdata"
cp --remove-destination "$testdata_dir/empty-0.1-1.x86_64.rpm" contract/testdata/packages/empty-0.1-1.x86_64.rpm
cp --remove-destination "$testdata_dir/payload-test-0.1-w9.gzdio.x86_64.rpm" contract/testdata/packages/payload-gzip.rpm
cp --remove-destination "$testdata_dir/payload-test-0.1-w6.xzdio.x86_64.rpm" contract/testdata/packages/payload-xz.rpm

sha256sum -c <<'HASHES'
ad3c97398532c99bdbcba519b3c317dc7789b214f5d49f8c3db5db77529b4518  contract/testdata/packages/empty-0.1-1.x86_64.rpm
780cd989fb75e7d13b0cecdbd25cdb5ae04fd7c185b7af2060c8a50f7f05ee64  contract/testdata/packages/payload-gzip.rpm
3cf38463fe6230b8ce31fd2c72d87a0ad91a460c489669094da09aa10f474a40  contract/testdata/packages/payload-xz.rpm
HASHES
