# Version 1 file format

This document fixes the format for stage 0. All three YAML files use
`schema: 1`. Unknown fields and duplicate YAML keys are errors. Required strings
must be nonempty. The committed files are `ghrepo.yaml` and `packages.yaml`.
`.ghrepo.local.yaml` is private and must stay out of Git and the Pages site. The
Go types are in `contract/types.go`.

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
  redirect_hosts: [release-assets.githubusercontent.com, objects.githubusercontent.com]
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
lowercase DNS names, without ports or wildcards. The first request host must be
in `source_hosts`. Every redirect host must be in `source_hosts` or
`redirect_hosts`. All requests and redirects use HTTPS. Version 1 source URLs do
not allow userinfo, query parameters, fragments, or credentials. A redirect to a
private address is also rejected. Redirect targets can have query parameters, as
used by signed GitHub release asset URLs.

APT uses one flat repository at `<base_url>/deb/`. The client source uses
`deb [signed-by=...] <base_url>/deb/ ./` with no component token. The chosen
`suite` and `component` appear in `Release` as `Suite` and `Components`; they do
not change paths. `architectures` lists permitted package architectures. The
`all` architecture is always permitted. RPM uses one repository at
`<base_url>/rpm/`; `id` is the `.repo` section name, `name` is its display name,
and `architectures` lists permitted package architectures. `noarch` is always
permitted. IDs, suites, components, architectures, and package names use ASCII
letters, digits, `.`, `_`, `+`, and `-`, with a letter or digit first. Versions
may also use `~`, `:`, and `^`. Values containing `/`, `..`, whitespace, or
control characters are errors.

`rpm.package_signature` is `metadata-only` or `trusted-packages`.
`metadata-only` emits `repo_gpgcheck=1` and `gpgcheck=0`. `trusted-packages`
emits both checks as `1`, requires at least one committed public key path in
`trusted_key_paths`, and requires each RPM to have a valid package signature
from one of those keys. Repository metadata uses `KEY.gpg`. The two key sets can
differ. No private key path belongs in this file.

Optional `storage.backend` is `none` or `github-releases`. For GitHub Releases,
`release_prefix` is a safe tag prefix and `bucket_size` is 1 through 1000.
Storage target account and repository are local settings. Upload mode uses
SHA-256-derived asset names and numbered release buckets. The default is `none`
and no upload occurs for `add URL`.

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
    comment: Example package
    source_url: https://github.com/example/project
    tags: [example, cli]
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
UTC timestamp set when it leaves the active index. A retained package remains at
its path for at least `site.retention_days` after that time. The minimum
configurable retention period is 30 days. Prune must also preserve packages
referenced by any retained signed index or rollback state.

`comment`, `source_url`, and `tags` are optional display metadata. `source_url`
is an HTTPS page URL. Tags are unique short identifiers. Use
`ghrepo add --comment TEXT --source-url URL --tags tag1,tag2 PACKAGE_URL` to set
these fields for new packages. Use
`ghrepo metadata set [--comment TEXT] [--source-url URL] [--tags tag1,tag2] SELECTOR`
to change an existing entry. An empty value clears a field. A new version from
`update` or `replace` keeps its predecessor's metadata. For a direct GitHub
release asset URL, `add` sets `source_url` to the GitHub repository and tries to
fetch its description and topics. A failed lookup is reported and does not stop
the package operation.

`format` is `deb` or `rpm`. Identity fields come from inspected package
metadata. DEB identity is `(name, version, architecture)`; its version includes
the optional epoch and revision exactly as the control file gives them. RPM
identity is `(name, epoch, version, release, architecture)`, with absent epoch
normalized to `0`. `Identity.epoch` is used only for RPM. Selectors are either
the exact served `path`, `deb/<name>/<architecture>/<version>`, or
`rpm/<name>/<architecture>/<epoch>/<version>/<release>`. A selector must match
exactly one entry; zero or multiple matches are errors. Use the full served path
when an active and a retained entry share an identity. Repeating `add URL` with
the same identity and bytes is a no-op. Explicit `add --upload` changes an
existing upstream entry with the same bytes to an owned source URL. It keeps the
entry's active or retained state. `ghrepo replace SELECTOR URL` downloads and
inspects new bytes with the same identity, retains the former active path, and
adds the new content-qualified path as active. Two active entries cannot share
an identity. `ghrepo prune` reports eligible retained paths. The
`ghrepo prune --apply` command removes them after the retention period, provided
the current signed indexes and other Git branch or tag tip manifests do not
reference them. Pruning leaves package bytes in the local cache.

Served paths use `/` separators and no leading slash, empty segment, `.`, `..`,
backslash, percent escape, query, fragment, or control character. DEBs use
`deb/pool/<name>_<version>_<arch>_<first-12-sha256>.deb`; RPMs use
`rpm/packages/<name>-<epoch>_<version>-<release>.<arch>.<first-12-sha256>.rpm`.
Each field is encoded as its literal safe ASCII component. Version fields may
use the extra version characters above. A field outside these alphabets is
rejected for version 1. A path collision with different bytes is an error; the
full hash in the manifest is still authoritative. These content-qualified paths
never change when a source URL moves. `source set` changes only `url` after byte
verification.

APT `Filename` is relative to the APT repository root at `<base_url>/deb/`. For
a manifest path `deb/pool/example.deb`, the field is `pool/example.deb`. The APT
reader prepends `deb/` before comparing the path, size, and SHA-256 with each
active manifest entry. RPM `primary.xml` `location href` is relative to the RPM
repository root at `<base_url>/rpm/`. For a manifest path
`rpm/packages/example.rpm`, the href is `packages/example.rpm`. The RPM reader
prepends `rpm/` before comparison. No leading slash or `../` is permitted.
Retained entries must not appear in new indexes.

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

`gpg.keyring` may name a local public keyring. `github.token` is allowed only in
this ignored file, which must have mode 0600 when it contains a token.
`GH_TOKEN` in the process environment takes priority over a selected `.env`,
then `github.token`. A selected `.env` is parsed as data. It is never run as
shell code. `ghrepo.yaml` has no credential fields.

The offline core accepts `GH_TOKEN`, `GHREPO_GITHUB_ACCOUNT`,
`GHREPO_GPG_KEY_ID`, `GHREPO_GPG_HOME`, `GHREPO_GPG_KEYRING`, and
`GHREPO_RELEASE_REPOSITORY` in the process environment or selected `.env`. For
each setting, the process value takes priority, then the `.env` value, then the
local YAML value. An empty process value is still an explicit value. The `.env`
reader accepts one `KEY=value` assignment per line and quoted values. It does
not expand variables, run commands, or accept duplicate keys. Errors do not
print URLs or credential values.

The `contract` package parses and writes each version 1 file. It rejects YAML
aliases, merges, duplicate keys, multiple documents, and unknown fields.
`MarshalManifest` sorts packages by served path. `WriteAtomic` writes a complete
file in the same directory before replacement. The `cache` package uses the same
manifest validator and SHA-256 verifier for cache hits and new downloads.
`cache.Ensure` stores verified files under `local-cache/sha256/`.
`cache.FetchVerified` checks the source again even if a valid cache file exists;
source changes and site builds use this operation. The default HTTP transport
resolves source hosts and connects only to public addresses, including after
redirects. Its injected client option is for local TLS tests. The
`storage.Backend` has an offline in-memory implementation and a GitHub Releases
adapter. The GitHub adapter uses numbered release buckets, uploads only
SHA-256-named assets, and reads the public URL back before it returns. Its tests
use local TLS servers. `ghrepo gc` reports unreferenced owned assets in the
configured buckets; it preserves URLs in the current manifest and branch or tag
tip manifests. It does not delete assets. Numbered buckets require mutable
releases. Existing immutable assets can be verified, but new assets cannot be
added to an immutable release.

## Signed output and support limits

| Item                    | Version 1 support                                                                                           | Other input                                              |
| ----------------------- | ----------------------------------------------------------------------------------------------------------- | -------------------------------------------------------- |
| DEB archive             | `ar` with `debian-binary` value `2.0`, `control.tar.gz`, and `data.tar.gz`                                  | `unsupported DEB archive member or compression`          |
| DEB control             | UTF-8 control file with required Package, Version, Architecture, Maintainer, Description fields             | `invalid DEB control field`                              |
| RPM package             | RPM lead major 3 with version 4 headers; gzip, xz, or zstd cpio payload                                     | `unsupported RPM package version or payload compression` |
| RPM repository metadata | gzip XML `primary`, `filelists`, and `other`, referenced by `repomd.xml`                                    | `unsupported RPM metadata type`                          |
| Repository signatures   | OpenPGP RSA or Ed25519 (EdDSA) with SHA-256, detached RPM `repomd.xml.asc` and clear-signed APT `InRelease` | `unsupported signature algorithm`                        |
| RPM package signatures  | Existing or locally copied OpenPGP RSA or Ed25519 (EdDSA) with SHA-256 from configured trusted keys         | `unsupported RPM package signature`                      |

`deb/Packages`, `deb/Packages.gz`, `deb/Release`, and `deb/InRelease` are
generated locally. Version 1 does not emit `Release.gpg`. `Acquire-By-Hash` is
disabled and no `deb/by-hash` paths are emitted. Clients can retry during a
Pages deployment transition. The builder records the first repository entry time
of each checksum-named RPM metadata file in `rpm/repodata/retention.json`. It
keeps files for at least `site.retention_days` and keeps files referenced by
`repomd.xml` in other branch or tag tip trees. Expired files leave the next
rebuilt metadata tree and Git staging includes their deletions. New `repomd.xml`
references only current metadata. Old files can protect package paths from
pruning while they remain in the repository. No SQLite, module, or delta RPM
metadata is emitted.

`apt.Build` writes these four APT files to a new directory from verified cache
bytes. It orders packages by served path, uses a fixed gzip header, and sets the
Release date to the build time. A failed build removes its new directory. The
caller replaces an older `deb/` directory only after `apt.Verify` checks the
complete new directory with `KEY.gpg`. Verification checks the RSA or Ed25519
SHA-256 clear signature, the signed Release content, the compressed index hash,
and each indexed package against the active manifest. Unsigned DEB files are
allowed. The local HTTP compatibility test runs when APT has the Debian
packaging backend. It checks refresh and package download. Set
`GHREPO_APT_INSTALL=1` only in a disposable root container to also check
installation.

The RPM reader candidate is `github.com/sassoftware/go-rpmutils` v0.4.0. Its
`ReadHeader`, `GetNEVRA`, and `PayloadReaderExtended` read the checked-in gzip,
xz, and zstd RPM fixtures in the offline test. Its `Verify` API checks package
signatures. The local copy signature passes independent `rpm --checksig` with an
imported test key. The DEB fixture uses Go's `archive/tar` and `compress/gzip`,
so no third-party DEB reader is needed for the first compression method. The
`rpm` package generates checksum-named gzip XML `primary`, `filelists`, and
`other` files. `repomd.xml` records each compressed and open SHA-256, size,
location, and timestamp. `repomd.xml.asc` is a detached RSA or Ed25519 SHA-256
GPG signature. `rpm.Verify` checks this signature, all metadata checksums and
package entries against the active manifest, and package bytes when given a site
root. Retained packages do not appear in new metadata. A failed build removes
its new output directory. The caller replaces existing metadata only after
verification.

`metadata-only` leaves upstream RPM bytes unchanged and requires
`repo_gpgcheck=1`, `gpgcheck=0` in generated client settings. For
`trusted-packages`, each package needs a full-payload RSA or Ed25519 SHA-256
signature that verifies against a configured public key. The metadata signing
key does not grant trust to package bytes. `rpm.SignCopy` uses the local GPG
keyring or agent to sign a general RPM header and its payload, writes a new
copy, and verifies the copy against configured trusted keys. It never exports a
private key. The caller must inspect, hash, and upload the final copy, then use
that copy's hash and served path in `packages.yaml`. The optional local HTTP
compatibility test uses RPM and DNF to check repository signature verification,
refresh, lookup, and download. DNF 4 passes this test in the local Nix
environment. It checks RPM installation in isolated RPM databases for both
signature policies. With `GHREPO_RPM_INSTALL=1`, DNF installs the unsigned
fixture under `gpgcheck=0`, rejects it under `gpgcheck=1`, and installs the
signed copy under `gpgcheck=1`. Run this option in a disposable root or user
namespace environment. Neither tool is a normal host requirement. APT
compatibility is checked by the local HTTP test where a working APT client is
available.

## Site artifact

`ghrepo site-build` reads only `ghrepo.yaml`, `packages.yaml`, `KEY.gpg`, signed
APT and RPM metadata, trusted public RPM keys, and committed files under
`templates/`. It downloads every active and retained package from its manifest
URL and checks size and SHA-256 again. The artifact contains only those package
paths, `KEY.gpg`, the four APT metadata files, RPM metadata with checksum-named
files, configured trusted public keys, and rendered `index.html`, `setup.sh`,
`deb.list`, `rpm.repo`, and optional `CNAME`. RPM metadata files kept for client
retention must match the checksum in their filenames. The current RPM indexes
are also verified against signed `repomd.xml`.

`init` installs the four source templates without replacing customized files.
The builder renders present templates into the artifact and requires
`templates/index.html`. The generated `rpm.repo` sets `repo_gpgcheck=1` and sets
`gpgcheck` from `rpm.package_signature`. The builder uses `gpgv` with the
committed public key. It does not read local configuration or signing keys. Only
a complete, verified site replaces `build/site/`.

`init` also installs `templates/README.md` and renders it to the repository
root. `ghrepo readme-build` renders this template on demand. `ghrepo publish`
renders it before committing both the template and `README.md`. This README is
repository documentation and is not part of the Pages artifact.

## Fixture source and license

`contract/testdata/generate/main.go` deterministically creates the four DEB
archives with `go run .` from that directory. The generator is part of this
GPL-3.0 licensed project. The RPM fixtures are copied unchanged from
`go-rpmutils` v0.4.0 `testdata/empty-0.1-1.x86_64.rpm` and its gzip and xz
payload-test RPMs. They are distributed under Apache-2.0; their source specs are
`testdata/empty.spec` and `testdata/payload-test.spec` in that project. The
checked-in RPMs require no RPM tools for tests.
`contract/testdata/generate-rpm.sh` copies the pinned RPMs and checks their
hashes. `contract/testdata/cases.json` records the expected positive and
negative cases. `invalid-signature.asc` is intentionally invalid test data, not
a key or usable signature. Normal tests only read local files and do not use the
network.

## Local publishing transaction

`rebuild` reads one validated manifest snapshot, verifies cached package bytes,
creates both signed metadata trees in `.ghrepo-transaction/`, and verifies them
with the committed public key. It copies checksum-named RPM metadata from the
prior tree for client retention. A `pending.json` record precedes the two
directory renames. The next `rebuild` or `check` restores both prior trees if
the swap was interrupted. A completed swap has a `complete` marker and only
needs transaction cleanup.

`check` fetches each manifest source again, verifies the APT and RPM signatures
and index links, and builds a temporary site to check the exact artifact size.
`publish --dry-run` runs `sync`, `rebuild`, and `check`, then stages the
explicit allowlist in a temporary Git index and reports the planned commit
paths. `publish` checks that its staged tree matches this preview before it
commits and pushes `HEAD` to `origin`. It rejects an index that already has a
staged path outside the allowlist. The allowlist contains shared configuration,
`packages.yaml`, public keys, the Pages workflow, the four templates, and
generated signed metadata. It excludes local settings, `.env`, cache,
transaction records, and build output even when ignore rules change.

The generated Pages workflow builds from the triggering commit when its checkout
contains the ghrepo Go module. A data-only checkout uses a fixed ghrepo commit
revision recorded in the workflow. Its build job uploads only `build/site/`. Its
deploy job uses the `github-pages` environment and grants `pages: write` and
`id-token: write` permissions.
