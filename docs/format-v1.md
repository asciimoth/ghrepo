# Version 1 file format

This document fixes the format for stage 0. All three YAML files use `schema: 1`.
Unknown fields and duplicate YAML keys are errors. Required strings must be
nonempty. The committed files are `ghrepo.yaml` and `packages.yaml`.
`.ghrepo.local.yaml` is private and must stay out of Git and the Pages site.
The Go types are in `contract/types.go`.

## Shared configuration: `ghrepo.yaml`

```yaml
schema: 1
repository:
  url: https://github.com/example/packages
  branch: main
  layout: flat-v1
apt:
  suite: stable
  component: main
  architectures: [amd64, arm64]
rpm:
  id: example
  name: Example packages
  architectures: [x86_64, aarch64]
  package_signature: metadata-only
downloads:
  source_hosts: [github.com]
  redirect_hosts: [release-assets.githubusercontent.com]
  max_bytes: 2147483647
site:
  base_url: https://example.github.io/packages
  max_bytes: 1073741824
  retention_days: 30
storage:
  backend: none
```

`repository.url` is the public HTTPS Git repository URL. `branch` selects the
Pages workflow trigger. `layout` must be `flat-v1`. `site.base_url` is the
public HTTPS URL without a trailing slash. `site.cname` is optional. Site size
is the sum of regular file sizes in the final Pages artifact and must not exceed
`site.max_bytes` or 1 GiB. `downloads.max_bytes` is the per-package limit and
must be positive and at most 2 GiB minus one byte. Host lists contain exact,
lowercase DNS names, without ports or wildcards. The first request host must
be in `source_hosts`. Every redirect host must be in `source_hosts` or
`redirect_hosts`. All requests and redirects use HTTPS, no URL userinfo or
token-bearing query parameters, and no credentials. A redirect to a private
address is also rejected.

APT uses one flat repository at `<base_url>/deb/`. The client source uses
`deb [signed-by=...] <base_url>/deb/ ./` with no component token. The chosen
`suite` and `component` appear in `Release` as `Suite` and `Components`; they
do not change paths. `architectures` lists permitted package architectures.
The `all` architecture is always permitted. RPM uses one repository at
`<base_url>/rpm/`; `id` is the `.repo` section name, `name` is its display
name, and `architectures` lists permitted package architectures. `noarch` is
always permitted. IDs, suites, components, architectures, and package names
use ASCII letters, digits, `.`, `_`, `+`, and `-`, with a letter or digit first.
Versions may also use `~`, `:`, and `^`. Values containing `/`, `..`, whitespace,
or control characters are errors.

`rpm.package_signature` is `metadata-only` or `trusted-packages`.
`metadata-only` emits `repo_gpgcheck=1` and `gpgcheck=0`.
`trusted-packages` emits both checks as `1`, requires at least one committed
public key path in `trusted_key_paths`, and requires each RPM to have a valid
package signature from one of those keys. Repository metadata uses `KEY.gpg`.
The two key sets can differ. No private key path belongs in this file.

Optional `storage.backend` is `none` or `github-releases`. For GitHub Releases,
`release_prefix` is a safe tag prefix and `bucket_size` is 1 through 1000.
Storage target account and repository are local settings. Upload mode uses
SHA-256-derived asset names and numbered release buckets. The default is
`none` and no upload occurs for `add URL`.

## Manifest: `packages.yaml`

```yaml
schema: 1
packages:
  - path: deb/pool/hello_1.2.0-1_amd64_07faed36d7e1.deb
    url: https://github.com/example/project/releases/download/v1/hello.deb
    sha256: 07faed36d7e121afcaa708d82b97d4367fa1d84df10c4630311f78303048d947
    size: 506
    source: upstream
    state: active
    format: deb
    identity:
      name: hello
      version: 1.2.0-1
      architecture: amd64
```

`sha256` has exactly 64 lowercase hexadecimal digits. `size` is positive and
does not exceed `downloads.max_bytes`. `source` is `upstream` or `owned` and
comes from the add/upload operation, never from the URL host. `state` is
`active` or `retained`. A retained entry must have `retained_at`, an RFC 3339
UTC timestamp set when it leaves the active index. A retained package remains
at its path for at least `site.retention_days` after that time. The minimum
configurable retention period is 30 days. Prune must also preserve packages
referenced by any retained signed index or rollback state.

`format` is `deb` or `rpm`. Identity fields come from inspected package
metadata. DEB identity is `(name, version, architecture)`; its version includes
the optional epoch and revision exactly as the control file gives them. RPM
identity is `(name, epoch, version, release, architecture)`, with absent epoch
normalized to `0`. `Identity.epoch` is used only for RPM. Selectors are either
the exact served `path`, `deb/<name>/<architecture>/<version>`, or
`rpm/<name>/<architecture>/<epoch>/<version>/<release>`. A selector must match exactly
one entry; zero or multiple matches are errors. Repeating an add with the same
identity and same bytes is a no-op. The same identity with different bytes is
a conflict until an explicit replacement operation is implemented.

Served paths use `/` separators and no leading slash, empty segment, `.`,
`..`, backslash, percent escape, query, fragment, or control character. DEBs
use `deb/pool/<name>_<version>_<arch>_<first-12-sha256>.deb`; RPMs use
`rpm/packages/<name>-<epoch>_<version>-<release>.<arch>.<first-12-sha256>.rpm`.
Each field is encoded as its literal safe ASCII component. Version fields may
use the extra version characters above. A field outside these alphabets is
rejected for version 1. A path collision with different bytes
is an error; the full hash in the manifest is still authoritative. These
content-qualified paths never change when a source URL moves. `source set`
changes only `url` after byte verification.

APT `Filename` and RPM `primary.xml` `location href` use the exact manifest
`path`, relative to the site root. No leading slash or `../` is permitted.
The metadata reader compares the full path, size, and SHA-256 against each
active manifest entry. Retained entries must not appear in new indexes.

## Local settings: `.ghrepo.local.yaml`

```yaml
schema: 1
gpg:
  key_id: ABCD1234
  home: /home/user/.gnupg
github:
  account: example
  env_file: /home/user/.config/ghrepo.env
storage:
  release_repository: example/package-assets
```

`gpg.keyring` may name a local public keyring. `github.token` is allowed only
in this ignored file, which must have mode 0600 when it contains a token.
`GH_TOKEN` in the process environment takes priority over a selected `.env`,
then `github.token`. A selected `.env` is parsed as data. It is never run as
shell code. `ghrepo.yaml` has no credential fields.

## Signed output and support limits

| Item | Version 1 support | Other input |
| --- | --- | --- |
| DEB archive | `ar` with `debian-binary` value `2.0`, `control.tar.gz`, and `data.tar.gz` | `unsupported DEB archive member or compression` |
| DEB control | UTF-8 control file with required Package, Version, Architecture, Maintainer, Description fields | `invalid DEB control field` |
| RPM package | RPM lead major 3 with version 4 headers; gzip, xz, or zstd cpio payload | `unsupported RPM package version or payload compression` |
| RPM repository metadata | gzip XML `primary`, `filelists`, and `other`, referenced by `repomd.xml` | `unsupported RPM metadata type` |
| Repository signatures | OpenPGP RSA with SHA-256, detached RPM `repomd.xml.asc` and clear-signed APT `InRelease` | `unsupported signature algorithm` |
| RPM package signatures | Existing OpenPGP RSA/SHA-256 signatures from configured trusted keys; local copy signing is conditional on stage 4 compatibility checks | `unsupported RPM package signature` |

`deb/Packages.gz`, `deb/Release`, and `deb/InRelease` are generated locally.
Version 1 does not emit `Release.gpg`. `Acquire-By-Hash` is disabled and no
`deb/by-hash` paths are emitted. Clients can retry during a Pages deployment
transition. The builder keeps previous checksum-named RPM metadata files for
at least 30 days; new `repomd.xml` references only new metadata. Old RPM
metadata files do not create package references after their retention window.
The package retention rule above keeps old package paths available through
that window. No SQLite, module, or delta RPM metadata is emitted.

The RPM reader candidate is `github.com/sassoftware/go-rpmutils` v0.4.0. Its
`ReadHeader`, `GetNEVRA`, and `PayloadReaderExtended` read the checked-in gzip,
xz, and zstd RPM fixtures in the offline test. Its `Verify` API exists for
signature checks, but stage 4 must confirm signed output with independent RPM
tooling before enabling local copy signing. The DEB fixture uses Go's
`archive/tar` and `compress/gzip`, so no
third-party DEB reader is needed for the first compression method. Metadata
generation is implemented in Go in later stages; this contract does not claim
client compatibility before those tests run.

## Fixture source and license

`contract/testdata/generate/main.go` deterministically creates the four DEB
archives with `go run .` from that directory. The generator is part of this
GPL-3.0 licensed project. The RPM fixtures are copied unchanged from
`go-rpmutils` v0.4.0
`testdata/empty-0.1-1.x86_64.rpm` and its gzip and xz payload-test RPMs. They
are distributed under Apache-2.0; their source specs are `testdata/empty.spec`
and `testdata/payload-test.spec` in that project. The checked-in RPMs require
no RPM tools for tests. `contract/testdata/generate-rpm.sh` copies the pinned
RPMs and checks their hashes. `contract/testdata/cases.json` records the
expected positive and negative cases. `invalid-signature.asc` is intentionally
invalid test data, not a key or usable signature. Normal tests only read local
files and do not use the network.
