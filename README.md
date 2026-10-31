# ghrepo

Tool for hosting DEB and RPM repositories on GitHub Pages.

## Local manifest commands

After building the `ghrepo` binary with `go build -o ghrepo .`, initialize a
repository with public URLs, then add packages by HTTPS URL:

```sh
ghrepo init --repo https://github.com/example/repo --site https://example.github.io/repo --source-hosts example.com
ghrepo add https://example.com/releases/hello.deb
ghrepo sync
ghrepo source set deb/hello/amd64/1.2.0-1 https://example.com/releases/hello-moved.deb
ghrepo remove deb/hello/amd64/1.2.0-1
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
transaction. `check` checks source URLs, signatures, indexed package bytes, and
the rendered site size. `publish --dry-run` runs `sync`, `rebuild`, and `check`,
then prints the exact Git path changes without committing or pushing. `publish`
stages only the configuration, templates, public keys, manifest, workflow, and
signed metadata. It commits and pushes to `origin`. The ignored
`.ghrepo-transaction/` directory records interrupted metadata swaps and is
restored on the next `rebuild` or `check`.

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
