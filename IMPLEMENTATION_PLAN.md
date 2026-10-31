# Implementation plan

This plan implements the local design in `local/GHREPO_DESIGN.md` in stages.
Complete the local format, trust, and build work first. Tests in stages 0–6 must
use local files, temporary Git repositories, local TLS test servers, and test
keys. They must not call the GitHub API or depend on a live GitHub repository.

## Rules for every stage

- Keep private signing keys and credentials on the maintainer's machine. The
  site builder receives only the committed public key and signed metadata.
- Do not require `dpkg`, `apt-ftparchive`, `rpm`, `rpmsign`, or `createrepo_c`
  as host commands. Use Go for package inspection and metadata generation. Local
  `gpg` is allowed for signing and verification.
- Treat `packages.yaml` as build input. Verify downloaded bytes against its size
  and SHA-256 before use. Verify signed indexes against the manifest before
  producing a site.
- Write new metadata and site output to temporary directories, then replace
  completed output. A failed operation must leave the last good output intact.
- Keep package bytes, local configuration, credentials, and build output out of
  Git commits. The site builder copies only an explicit allowlist.

## 0. Fix the format contract and test fixtures

Define Go types and document the version 1 schema for `ghrepo.yaml`,
`packages.yaml`, and `.ghrepo.local.yaml`. Include the repository URL and
layout, APT suite/component/architecture values, RPM repository identity and
package-signature policy, allowed download and redirect hosts, site size limit,
retention period, and optional storage settings. Define how selectors identify
packages, how package identity is formed from inspected metadata, and how
versioned served paths are assigned. Paths in signed indexes must refer to the
same site paths as manifest entries.

Choose and document the first supported `.deb` control compression methods, RPM
package versions, RPM metadata types, and signature algorithms after checking
the candidate Go libraries against fixtures. Reject other inputs with specific
errors. Define whether `Release.gpg` is emitted in version 1. Define the initial
policy for APT by-hash paths and old RPM metadata retention. Set the minimum
retention period before implementing prune.

Create small fixture packages and expected metadata, including changed-byte,
missing-file, malformed-archive, unsafe-path, duplicate-identity, and invalid
signature cases. Keep fixture generation reproducible and do not require package
tools in normal tests.

**Done when:** schemas, path rules, support matrix, and fixture licenses are
documented; tests can load each fixture without network access.

## 1. Build the offline core

Implement strict config and manifest parsing, validation, and stable YAML
writing. Reject duplicate keys and paths, unknown states, invalid SHA-256 or
size, unsafe path components, non-HTTPS sources, unexpected source and redirect
hosts, and token-bearing URLs. Keep `source` ownership explicit. Implement
precedence for process environment, selected `.env`, local config, and shared
config. Parse `.env` as data; never execute it. Redact credentials in errors.

Add a verified cache with a fetch interface. Use an `httptest` TLS server for
redirects, failed responses, size limits, hash mismatches, and interrupted
downloads. Write to a temporary file, verify it, then rename it into the ignored
cache. The same verifier must serve `add`, `sync`, `source set`, and the site
builder. Add a storage interface now, with an in-memory fake; the GitHub adapter
comes later.

**Done when:** `go test ./...` covers schema round trips, all reject cases,
fetch and cache behavior, and secret redaction with no external network.

## 2. Inspect packages and implement local manifest commands

Read `.deb` archive members and control fields in Go. Read RPM headers and
signature information in Go. Extract canonical identity, architecture, version,
dependencies needed for indexes, and any other required index fields from the
archive, not the input filename. Record the final bytes' size and SHA-256.
Reject unsupported formats using the support matrix from stage 0.

Implement local `init`, `add URL...`, `sync`, `source set`, and `remove`. `init`
creates configuration, templates, workflow source, `.gitignore` additions, and
public-key export when a key is configured; it must not destroy existing files.
`add` fetches public HTTPS URLs into cache and updates only the local manifest.
`source set` accepts a new URL only when its bytes match the stored hash and
size. `remove` changes `active` to `retained`. All writes should use a complete
validated manifest snapshot and an atomic file replacement.

**Done when:** fixture-driven CLI tests show stable paths and identity, safe
repeat runs, unchanged bytes after a source URL move, and no remote writes or
pushes.

## 3. Generate and verify APT metadata locally

Generate `deb/Packages.gz` from active `.deb` fixtures in Go. Use deterministic
field order and compression where practical. `Filename`, `Size`, and hashes must
match manifest paths and bytes. Generate `deb/Release` with index hashes. Use
the local `gpg` keyring or agent to create `deb/InRelease` and, if selected in
stage 0, `deb/Release.gpg`. Export and use the public key for verification. Do
not treat an unsigned `.deb` as an error.

Implement an independent metadata reader that checks the Release signature,
index hashes, and each indexed package path, size, and hash against active
manifest entries. Test key mismatch, tampering at each link, omitted active
packages, and retained packages appearing in new indexes. Use an isolated
temporary GPG home and test key for signing tests. Keep a small compatibility
test that serves the fixture repository over local HTTP and runs APT client
tools where available; this is a test-only dependency.

**Done when:** local verification passes for generated metadata and fails for
each tampered fixture; an APT client can refresh and install from the local HTTP
fixture in the compatibility test environment.

## 4. Generate and verify RPM metadata locally

Generate the RPM metadata files referenced by `rpm/repodata/repomd.xml` in Go
for active RPM fixtures. Validate the chosen Go library's output against the
documented metadata contract. Sign `repomd.xml` locally with `gpg` to produce
`repomd.xml.asc`. Verify the signature, metadata file checksums, and all indexed
package paths and hashes against the active manifest.

Implement the RPM package-signature policy separately from repository metadata
signing. An unchanged upstream RPM is valid with `gpgcheck=1` only when its
existing signature matches a configured trusted package key. Otherwise require
`gpgcheck=0` or an explicitly uploaded copy signed with the maintainer's key.
For signing a copy, use Go to select and update RPM signature data while asking
`gpg` to sign. Do not export a private key for the agent or hardware-token path.
Verify final copied bytes before they enter the manifest. Make package signing
conditional on the format and algorithm support matrix.

Use independent RPM and DNF tooling in compatibility tests, outside the normal
host requirements. Test metadata parsing, refresh, install, and both `gpgcheck`
policies against a local HTTP fixture. Do not claim RPM signing support until
independent tooling verifies those signatures.

**Done when:** tampered metadata and package bytes fail verification; the
documented RPM fixtures pass the independent compatibility tests.

## 5. Build the site without GitHub

Implement a separate site-builder entry point that needs no private key or
package-management executable. Starting from a clean checkout fixture, it
downloads active and retained packages into `build/site/`, verifies each
download, copies only signed indexes and the public key, verifies metadata
against the active manifest, and renders templates. Check site size and reject
symlinks and hard links in the final artifact. Rendering must not alter signed
bytes or package paths. Include retained package files but exclude them from new
indexes.

Test with local HTTPS sources and a fixture that has no private key. Assert a
byte-for-byte match of signed files, an explicit output allowlist, no local
config or cache in the site, and failure before output replacement when a URL
vanishes or changes bytes. Test that a previous successful site remains after
each failure.

**Done when:** a fresh offline fixture checkout can rebuild and verify the same
site from manifest, signed files, public key, and local HTTPS source URLs.

## 6. Finish local command flow and workflow generation

Implement `rebuild` as one manifest snapshot: verify cache, build APT and RPM
metadata, sign locally, verify all trust links, then replace prior metadata.
Implement `check` for config, sources, signatures, metadata links, and site
size. Implement `publish --dry-run` to run sync, rebuild, and check and show the
exact intended Git changes. Implement a strict Git staging allowlist that
rejects local config, `.env`, cache, and build files even if `.gitignore`
changes. Add local transaction records so an interrupted operation can resume.

Generate the two-job Pages workflow. Its build job runs the site builder and
uploads only `build/site/`; its deploy job uses the Pages environment and the
required deployment permissions. Use branch and concurrency settings from shared
config. Check the generated YAML and workflow commands in local tests. Test
commit and push logic against temporary bare Git remotes before using a hosted
repository. The normal local commands do not need GitHub API access.

**Done when:** dry-run output and staged paths are exact, a failed metadata
build preserves prior signed files, a failed push leaves the remote unchanged,
and a temp bare remote receives one complete manifest-and-metadata commit.

## 7. Add GitHub integration and live acceptance tests

Implement the optional GitHub backend behind the storage and publishing
interfaces. Read `GH_TOKEN` or local credentials without logging them. Add
remote creation and Pages setup to `init` as explicit options. Implement
`add --upload`: inspect and, if configured, sign an RPM copy locally; upload the
final bytes under a SHA-256-derived name; verify its public download URL; only
then add it to the manifest. Use numbered release buckets without overwriting an
asset. Record progress for resume. Implement `gc` as a report limited to
unreferenced maintainer-owned assets; account for retained and rollback
references and the chosen immutable-release policy.

Run live acceptance tests in a disposable public repository: configure Pages for
GitHub Actions, publish APT and RPM fixtures, verify APT and DNF clients, check
a changed or missing upstream URL blocks deployment, test retention and
rollback, and confirm the Pages artifact and Git history contain no local
secrets or package bytes. Record observed Pages build time and site size. Run
these tests only after the offline gates pass.

**Done when:** the live repository passes every acceptance check in the design
and the workflow leaves the last successful deployment in place after a failed
build.
