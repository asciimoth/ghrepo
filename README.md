# ghrepo

Tool for hosting DEB and RPM repositories on GitHub Pages.

## Install with Nix

Install the CLI from this repository:

```sh
nix profile install github:asciimoth/ghrepo
```

The flake also provides `packages.<system>.ghrepo`, so you can run it without
installing it with `nix run github:asciimoth/ghrepo -- --help`.

## Make a release

The release command builds Linux `amd64` and `arm64` binaries. GoReleaser makes
`tar.gz`, DEB, and RPM files for each architecture. It adds a detached GPG
signature for each archive and package, signs the checksum file, and publishes
them in a GitHub Release. The DEB and RPM packages do not contain embedded
signatures; verify their `.sig` files before installation.

Install the release tools with `nix develop`. Configure GitHub authentication
with `GITHUB_TOKEN` or `GH_TOKEN` (the token needs `contents: write`), or sign
in with `gh auth login`. Set `GPG_KEY_ID` to the fingerprint of a local GPG
secret key. Do not commit the token or secret key.

Commit all changes, then run the command from the branch you want to release:

```sh
export GPG_KEY_ID=YOUR_KEY_FINGERPRINT
just release 1.2.3
```

The command runs `just check` and a local signed snapshot build before it
creates and pushes the `v1.2.3` tag. GoReleaser then uploads the signed files.
The release stops if the working tree is not clean or the tag already exists.
Verify a downloaded asset and its detached signature with
`gpg --verify FILE.sig FILE`; import and trust the release public key first.

## Set up a new GitHub repository

Before you run `ghrepo init` for a repository you created on GitHub:

1. Create a **public** repository. `ghrepo init` uses the checked out branch by
   default. Pass `--branch` to select a different branch.
1. In **Settings → Pages → Build and deployment**, set **Source** to **GitHub
   Actions**. See [GitHub's Pages instructions][pages-source]. Make sure GitHub
   Actions can run in the repository.
1. Clone the repository and enter its directory. Set up Git authentication so
   you can push to `origin`.
1. Create or select a local GPG signing key. If you omit `--key-id`,
   `ghrepo init` selects the first signing secret key listed by GPG and saves
   its fingerprint in `.ghrepo.local.yaml`. To select another key, find its ID
   with `gpg --list-secret-keys --keyid-format LONG` and pass `--key-id`.
   `ghrepo init` exports the selected public key, and `ghrepo publish` uses the
   selected key to sign repository metadata.

```sh
git clone https://github.com/OWNER/REPO.git
cd REPO
ghrepo init --repo OWNER/REPO
```

`init` prints the chosen key fingerprint and its first GPG user ID. It also
prints the key during `--dry-run`. If GPG cannot provide key details, it prints
the configured key ID.

Replace `OWNER` and `REPO` with your values. The generated Pages workflow starts
after you publish to the selected branch. For a repository that does not exist
yet, pass `--create-remote` and `--configure-pages` to `init` to create it and
select GitHub Actions for Pages. Set `GH_TOKEN` for those API calls.

The generated Pages workflow downloads the latest published ghrepo release for
Linux amd64. It verifies the archive signature with the release public key
embedded in the workflow before it runs the binary. If the release signing key
changes, update the generated workflow in each repository. Existing repositories
keep their committed workflow until you replace it with a newly generated one.

## Local manifest commands

Build the `ghrepo` binary with `just build`, or run a command from the source
tree with `just run <command> [args]`. To use the commands below, initialize a
repository with public URLs, then add packages by HTTPS URL:

Run `ghrepo --help` to list commands, or `ghrepo COMMAND --help` to see a
command's usage.

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
cache, metadata, and Git operations use the selected repository directory. Use
`--dry-run` with `init`, `add` (including `--upload`), `replace`, `sync`,
`source set`, `remove`, `prune --apply`, `rebuild`, `check`, or `site-build` to
preview the command without changing repository files or sending mutation
requests. For example, `ghrepo add --upload --dry-run ./myproject.deb` checks
the package file and reports the planned upload. `update` and `publish` also
accept `--dry-run` as described below. `init --repo` still specifies the GitHub
repository URL. `add` updates only `packages.yaml` and the ignored verified
cache. `source set` verifies the new URL against the stored size and SHA-256
before it changes the manifest. `remove` marks an entry as retained. These
commands do not publish or push. The `rebuild` command verifies cached package
bytes, signs new APT and RPM indexes, verifies both indexes with `KEY.gpg`, and
replaces the prior metadata as one recoverable transaction. `replace` checks new
bytes with the same package identity and retains the old served path. `prune`
previews retained paths that have passed the configured retention period and
have no signed index or rollback reference; `--apply` removes these paths from
the manifest. `check` checks source URLs, signatures, indexed package bytes, and
the rendered site size. `update` checks active upstream packages whose source is
a direct `https://github.com/OWNER/REPO/releases/download/TAG/ASSET` URL. It
uses the repository's latest published GitHub release and selects an asset with
the same name, or a name formed by replacing the old tag or package version. It
downloads matching assets, verifies the package format, name, and architecture,
and adds new versions to `packages.yaml`. Unsupported URLs and releases without
a matching asset are reported and skipped. `update --dry-run` downloads matching
assets to a temporary file and reports their package versions without changing
the repository or its cache. `update` does not publish or push.

```sh
ghrepo init --repo example/repo --source-hosts github.com
ghrepo add https://github.com/OWNER/REPO/releases/download/v1.0/app_1.0_amd64.deb
ghrepo add --comment "Example package" --tags cli,tools https://github.com/OWNER/REPO/releases/download/v1.0/app_1.0_amd64.deb
ghrepo metadata set --comment "Updated description" deb/APP/amd64/VERSION
ghrepo update --dry-run
ghrepo update
```

`publish --dry-run` runs `sync`, `rebuild`, and `check` in a temporary copy,
then prints the exact Git path changes without changing repository files,
committing, or pushing. `publish` stages only the configuration, templates,
public keys, manifest, workflow, and signed metadata. It commits and pushes to
`origin`. The ignored `.ghrepo-transaction/` directory records interrupted
metadata swaps and is restored on the next `rebuild` or `check`.

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
contains the rendered landing page, download script, and APT and RPM client
files from the committed templates.

## Packages page

`ghrepo init` creates `templates/index.html`, which becomes the site's
`index.html` packages page. By default, it uses Catppuccin Mocha colors and
shows APT and DNF setup commands, active packages, the GitHub owner and source
repository, the public PGP key, and a link to ghrepo. The setup commands use
`deb.list` and `rpm.repo` from the site. `init` writes the owner name into the
default template from the repository URL.

Edit the source template to change the text, HTML, links, or package layout.
`init` does not overwrite edited templates. The site builder reads the source
template on each build. Run `ghrepo check` to check the rendered site, then
`ghrepo publish` to commit and deploy your changes.

The template uses Go expressions in `{{` and `}}`. The available data is:

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
The builder requires `templates/index.html`. Template parse errors or unknown
fields fail the site build.

## Repository README

`ghrepo init` creates `templates/README.md` and renders it to the repository
root as `README.md`. Edit the template to change the README. Run
`ghrepo readme-build` to render it after package or template changes. The
template uses Go's `text/template` with `.Repository`, `.Packages`, and
`.BaseURL`. `ghrepo publish` renders the README after checking the repository
and includes the template and rendered file in its commit. A template error
leaves the previous README intact.

## Docker compatibility tests

Run `just test-e2e` with Docker available. The test builds Linux test binaries
and runs the generated repositories through APT and `dpkg` in Debian, and DNF
and `rpm` in AlmaLinux. It checks repository refresh, package download, and
installation. The RPM test also checks both package signature policies. Docker
downloads the images and their test tools on the first run. The normal
`just test` command does not require Docker.

[pages-source]: https://docs.github.com/en/pages/getting-started-with-github-pages/configuring-a-publishing-source-for-your-github-pages-site#publishing-with-a-custom-github-actions-workflow
