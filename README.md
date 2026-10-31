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
```

Run these commands from the repository directory. `add` updates only
`packages.yaml` and the ignored verified cache. `source set` verifies the new
URL against the stored size and SHA-256 before it changes the manifest. `remove`
marks an entry as retained. These commands do not publish or push. The generated
Pages workflow runs `ghrepo site-build` from a clean checkout. This command
downloads all active and retained packages from their HTTPS source URLs, checks
their size and SHA-256, verifies the signed APT and RPM metadata with `KEY.gpg`,
and writes `build/site/`. It needs `gpgv` for public-key verification. It does
not need a private key or package-management tools. A failed build keeps the
previous `build/site/` directory. The artifact also contains the rendered
landing page, setup notes, download script, and APT and RPM client files from
the committed templates.
