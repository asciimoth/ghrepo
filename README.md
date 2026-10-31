# ghrepo

Tool for hosting DEB and RPM repositories on GitHub Pages.

## Local manifest commands

After building the `ghrepo` binary with `go build -o ghrepo .`, initialize a
repository with public URLs, then add packages by HTTPS URL:

```sh
ghrepo init --repo example/repo --source-hosts example.com
ghrepo add https://example.com/releases/hello.deb
ghrepo sync
ghrepo source set deb/hello/amd64/1.2.0-1 https://example.com/releases/hello-moved.deb
ghrepo replace deb/hello/amd64/1.2.0-1 https://example.com/releases/hello-rebuilt.deb
ghrepo prune
ghrepo prune --apply
ghrepo rebuild
ghrepo check
ghrepo publish --dry-run
ghrepo publish --message 'chore(repo): publish signed metadata'
```

Run these commands from the repository directory. `add` updates only
`packages.yaml` and the ignored verified cache. `source set` verifies the new
URL against the stored size and SHA-256 before it changes the manifest. `remove`
marks an entry as retained. These commands do not publish or push. The `rebuild`
command verifies cached package bytes, signs new APT and RPM indexes, verifies
both indexes with `KEY.gpg`, and replaces the prior metadata as one recoverable
transaction. `replace` checks new bytes with the same package identity and
retains the old served path. `prune` previews retained paths that have passed
the configured retention period and have no signed index or rollback reference;
`--apply` removes these paths from the manifest. `check` checks source URLs,
signatures, indexed package bytes, and the rendered site size.
`publish --dry-run` runs `sync`, `rebuild`, and `check`, then prints the exact
Git path changes without committing or pushing. `publish` stages only the
configuration, templates, public keys, manifest, workflow, and signed metadata.
It commits and pushes to `origin`. The ignored `.ghrepo-transaction/` directory
records interrupted metadata swaps and is restored on the next `rebuild` or
`check`.

`init` accepts `user/repo` or a full GitHub HTTPS URL for `--repo`. It stores
the full URL in `ghrepo.yaml`. If `--site` is absent, it uses
`https://user.github.io/repo`. For a `user.github.io` repository, it uses
`https://user.github.io`. Set `--site` to use a custom domain.

## Optional GitHub storage

Set `storage.backend: github-releases`, `storage.release_prefix`, and
`storage.bucket_size` (1–1000) in `ghrepo.yaml`. Set `GH_TOKEN` in the
environment or ignored local settings. Release uploads need repository Contents
write access. Include `github.com` in `downloads.source_hosts` for uploads.

```sh
ghrepo init --repo example/repo --create-remote --configure-pages
ghrepo add --upload ./hello.deb ./tool.rpm
ghrepo add --upload --sign-rpm ./tool.rpm
ghrepo gc
```

`--create-remote` creates a public repository and adds `origin`.
`--configure-pages` selects GitHub Actions for Pages. Both options use the
GitHub API. `add --upload` puts SHA-256-named assets in numbered releases,
checks their public bytes, and then adds their URLs to the manifest. If the same
package has an upstream URL, it changes that entry to the owned URL after
verification. With the trusted RPM package policy, it signs a copy when the
original signature is not trusted and a local signing key is configured.
`--sign-rpm` requests a signed copy explicitly. Repeat an interrupted upload
command to resume it; an existing asset is checked before use. `gc` reports
unreferenced owned assets but does not delete them. It protects references in
the current manifest and branch or tag tip manifests. Upload progress stays in
ignored `.ghrepo-upload/` until the manifest is saved.

This bucket policy requires mutable GitHub Releases. The adapter reuses a
verified asset in an existing immutable release but cannot add new assets to
one.

Normal tests use local TLS servers. A manual check in a disposable public
repository can confirm GitHub Pages deployment behavior.

The generated Pages workflow runs `ghrepo site-build` from a clean checkout.
This command downloads all active and retained packages from their HTTPS source
URLs, checks their size and SHA-256, verifies the signed APT and RPM metadata
with `KEY.gpg`, and writes `build/site/`. It needs `gpgv` for public-key
verification. It does not need a private key or package-management tools. A
failed build keeps the previous `build/site/` directory. The artifact also
contains the rendered landing page, setup notes, download script, and APT and
RPM client files from the committed templates.

## Docker compatibility tests

Run `just test-e2e` with Docker available. The test builds Linux test binaries
and runs the generated repositories through APT and `dpkg` in Debian, and DNF
and `rpm` in AlmaLinux. It checks repository refresh, package download, and
installation. The RPM test also checks both package signature policies. Docker
downloads the images and their test tools on the first run. The normal
`just test` command does not require Docker.
