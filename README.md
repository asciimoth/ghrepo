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

Run these commands from the repository directory, or set `GHREPO_REPO_DIR` to
its local path. Use `--repo-dir DIR` to select a repository for one command;
this option takes priority over the environment variable. For example, from a
project directory:

```sh
export GHREPO_REPO_DIR=/home/user/myrepo
ghrepo add --upload ./myproject.deb
ghrepo --repo-dir /home/user/otherrepo add --upload ./myproject.deb
```

Package file paths stay relative to the current directory. Repository files,
cache, metadata, and Git operations use the selected repository directory.
`init --repo` still specifies the GitHub repository URL. `add` updates only
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

To change the site URL of an existing repository, run this command from its
local directory, then publish the change:

```sh
ghrepo init --site https://repo.example.com
ghrepo publish
```

Use an HTTPS URL without a trailing slash. The URL is used as supplied: a custom
domain has no repository-name path unless you include one. An explicit `--site`
updates `site.base_url` and sets `site.cname` to the custom hostname. A GitHub
Pages hostname clears `site.cname`. Running `init` without `--site` keeps the
saved site settings. Existing packages, signed metadata, local settings, and
templates are preserved. The next site build renders the client configuration
and links with the new URL. Configure DNS and the Pages custom domain
separately, and update installed clients to use the new URL.

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

## Packages page and setup notes

`ghrepo init` creates `templates/index.html` and `templates/README.md`. The
first file becomes the site's `index.html` packages page. By default, it links
to `deb.list`, `rpm.repo`, and `README.md`, then lists active packages with
their names, versions, architectures, and download links. The second file
becomes the site's `README.md` setup notes. By default, it gives the APT and RPM
source URLs, links to `KEY.gpg` and the client configuration files, shows the
APT keyring path, and lists active package downloads.

Edit these source files to change the text, HTML, Markdown, links, or package
layout. `init` does not overwrite edited templates. The site builder reads the
source templates on each build. Run `ghrepo check` to check the rendered site,
then `ghrepo publish` to commit and deploy your changes.

Both files use Go template expressions in `{{` and `}}`. The available data is:

| Expression    | Value                                                                                      |
| ------------- | ------------------------------------------------------------------------------------------ |
| `.BaseURL`    | `site.base_url` from `ghrepo.yaml`, without a trailing slash.                              |
| `.Repository` | The validated `ghrepo.yaml` data, such as `.Repository.RPM.ID` and `.Repository.RPM.Name`. |
| `.Packages`   | All entries from `packages.yaml`, including active and retained packages.                  |
| `.GPGCheck`   | `1` for `trusted-packages`; `0` for `metadata-only`.                                       |
| `.RPMKeyURLs` | Space-separated public URLs for the repository key and trusted RPM keys.                   |

Each item in `.Packages` has `.State`, `.Path`, `.Format`, `.Identity`, and the
other fields defined in `packages.yaml`. For example, `.Identity.Name`,
`.Identity.Version`, and `.Identity.Architecture` identify a package. Filter on
`.State` if a list must contain only active packages. Use `$.BaseURL` inside a
`range` to refer to the site URL; `.` then refers to the current package.

For example, this HTML in `templates/index.html` makes a list of active DEB
packages:

```html
<h1>{{.Repository.RPM.Name}}</h1>
<ul>
{{range .Packages}}{{if and (eq .State "active") (eq .Format "deb")}}
  <li><a href="{{$.BaseURL}}/{{.Path}}">{{.Identity.Name}} {{.Identity.Version}}</a></li>
{{end}}{{end}}
</ul>
```

`index.html` uses Go's `html/template`, which escapes package values for HTML.
`README.md` uses `text/template` and produces a Markdown file. For example, this
line in `templates/README.md` links to the public key:

```markdown
Download [the repository key]({{.BaseURL}}/KEY.gpg).
```

The builder requires `templates/index.html`. It omits the rendered `README.md`
if `templates/README.md` is absent. Template parse errors or unknown fields fail
the site build.

## Docker compatibility tests

Run `just test-e2e` with Docker available. The test builds Linux test binaries
and runs the generated repositories through APT and `dpkg` in Debian, and DNF
and `rpm` in AlmaLinux. It checks repository refresh, package download, and
installation. The RPM test also checks both package signature policies. Docker
downloads the images and their test tools on the first run. The normal
`just test` command does not require Docker.
