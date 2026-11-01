package site

import (
	"html"
	"net/url"
	"strings"
)

// SourceTemplate is a file installed by init and rendered into the site.
type SourceTemplate struct{ Name, Text string }

// TemplatesFor adds the repository owner name to the default page.
func TemplatesFor(repositoryURL string) ([]SourceTemplate, error) {
	u, err := url.Parse(repositoryURL)
	if err != nil {
		return nil, err
	}
	owner := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")[0]
	items := make([]SourceTemplate, len(DefaultTemplates))
	copy(items, DefaultTemplates)
	for i := range items {
		items[i].Text = strings.ReplaceAll(items[i].Text, "__GHREPO_OWNER__", html.EscapeString(owner))
	}
	return items, nil
}

// DefaultTemplates are source templates installed by init. Existing files are
// never replaced, so a repository can customize any page or client file.
// The owner link uses /.. to point from the repository to its GitHub owner.
var DefaultTemplates = []SourceTemplate{
	{"index.html", `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="color-scheme" content="dark">
  <title>{{.Repository.RPM.Name}} · Packages</title>
  <style>
    :root {
      color-scheme: dark;
      font-family: Arial, Helvetica, "Liberation Sans", sans-serif;
      font-synthesis: none;
      line-height: 1.5;
      background: #1e1e2e;
      color: #cdd6f4;
    }
    *, *::before, *::after { box-sizing: border-box; }
    body { margin: 0; min-width: 320px; }
    a { color: #89b4fa; text-underline-offset: 0.2em; }
    a:hover { color: #b4befe; }
    a:focus-visible { outline: 2px solid #f5c2e7; outline-offset: 3px; }
    h1, h2, h3, p { margin-top: 0; }
    h1 { margin-bottom: 0.5rem; font-size: 2.5rem; font-size: clamp(2rem, 5vw, 3.25rem); line-height: 1.15; }
    h2 { margin-bottom: 0.5rem; font-size: 1.5rem; }
    h3 { margin-bottom: 0.5rem; font-size: 1.1rem; }
    .wrap { width: calc(100% - 2rem); max-width: 68rem; margin: 0 auto; }
    .topbar { border-bottom: 1px solid #45475a; background: #181825; }
    .topbar .wrap { display: flex; align-items: center; justify-content: space-between; gap: 1rem; padding-block: 0.85rem; }
    .brand { color: #cba6f7; font-weight: 700; text-decoration: none; }
    .topbar nav { display: flex; flex-wrap: wrap; gap: 0.5rem 1.25rem; }
    main { padding-block: 3rem 4rem; }
    .eyebrow { color: #a6e3a1; font-size: 0.8rem; font-weight: 700; letter-spacing: 0.09em; text-transform: uppercase; }
    .intro { max-width: 42rem; color: #bac2de; font-size: 1.1rem; }
    .links { display: flex; flex-wrap: wrap; gap: 0.75rem; margin: 1.5rem 0 3rem; }
    .button { display: inline-block; border: 1px solid #89b4fa; border-radius: 0.5rem; padding: 0.6rem 0.9rem; font-weight: 600; text-decoration: none; }
    .button.primary { background: #89b4fa; color: #1e1e2e; }
    .button.primary:hover { background: #b4befe; }
    .section { margin-top: 3rem; }
    .muted { color: #a6adc8; }
    .setup-grid { display: grid; gap: 1rem; margin-top: 1.25rem; }
    .card { min-width: 0; border: 1px solid #45475a; border-radius: 0.75rem; background: #313244; padding: 1.25rem; }
    .card p { color: #bac2de; }
    pre { overflow-x: auto; margin: 1rem 0 0; border: 1px solid #45475a; border-radius: 0.5rem; background: #181825; padding: 1rem; color: #cdd6f4; font-size: 0.85rem; line-height: 1.65; tab-size: 2; }
    code { font-family: ui-monospace, SFMono-Regular, Consolas, "Liberation Mono", monospace; }
    .packages { display: grid; gap: 0.75rem; list-style: none; margin: 1.25rem 0 0; padding: 0; }
    .package { display: flex; flex-wrap: wrap; align-items: baseline; justify-content: space-between; gap: 0.25rem 1rem; border: 1px solid #45475a; border-radius: 0.5rem; background: #313244; padding: 0.9rem 1rem; }
    .package a { font-weight: 600; overflow-wrap: break-word; overflow-wrap: anywhere; }
    .package span { color: #a6adc8; font-size: 0.9rem; }
    footer { border-top: 1px solid #45475a; padding-block: 1.5rem; color: #a6adc8; font-size: 0.9rem; }
    footer p { margin: 0; }
    @media (max-width: 40rem) {
      .topbar .wrap { align-items: flex-start; flex-direction: column; }
      main { padding-top: 2rem; }
    }
  </style>
</head>
<body>
  <header class="topbar">
    <div class="wrap">
      <a class="brand" href="{{.Repository.Repository.URL}}">{{.Repository.RPM.Name}}</a>
      <nav aria-label="Page links">
        <a href="#setup">Set up</a>
        <a href="#packages">Packages</a>
        <a href="{{.Repository.Repository.URL}}">GitHub repository</a>
      </nav>
    </div>
  </header>
  <main class="wrap">
    <p class="eyebrow">APT and RPM repository</p>
    <h1>{{.Repository.RPM.Name}}</h1>
    <p class="intro">Packages published by <a href="{{.Repository.Repository.URL}}/..">__GHREPO_OWNER__</a>. Add this repository to your system to install and update packages with APT or DNF.</p>
    <div class="links">
      <a class="button primary" href="KEY.gpg" download>Download PGP public key</a>
      <a class="button" href="{{.Repository.Repository.URL}}">View source on GitHub</a>
    </div>

    <section class="section" id="setup" aria-labelledby="setup-title">
      <h2 id="setup-title">Add this repository</h2>
      <p class="muted">Choose the commands for your system. Replace <code>PACKAGE_NAME</code> with a name from the package list below.</p>
      <div class="setup-grid">
        <div class="card">
          <h3>Debian and Ubuntu (APT)</h3>
          <p>Install the <a href="KEY.gpg">public key</a> and <a href="deb.list">APT source file</a>, then update APT and install a package.</p>
          <pre><code>curl -fsSLo KEY.gpg "{{.BaseURL}}/KEY.gpg"
sudo install -m 0644 KEY.gpg /usr/share/keyrings/{{.Repository.RPM.ID}}-archive-keyring.gpg
curl -fsSLo deb.list "{{.BaseURL}}/deb.list"
sudo install -m 0644 deb.list /etc/apt/sources.list.d/{{.Repository.RPM.ID}}.list
sudo apt update
sudo apt install PACKAGE_NAME</code></pre>
        </div>
        <div class="card">
          <h3>Fedora and other DNF systems (RPM)</h3>
          <p>Install the <a href="rpm.repo">DNF repository file</a>, then refresh metadata and install a package. DNF uses the repository's public key to verify its metadata.</p>
          <pre><code>curl -fsSLo rpm.repo "{{.BaseURL}}/rpm.repo"
sudo install -m 0644 rpm.repo /etc/yum.repos.d/{{.Repository.RPM.ID}}.repo
sudo dnf makecache
sudo dnf install PACKAGE_NAME</code></pre>
        </div>
      </div>
      <p class="muted">You can also download the <a href="setup.sh">setup files</a>.</p>
    </section>

    <section class="section" id="packages" aria-labelledby="packages-title">
      <h2 id="packages-title">Available packages</h2>
      <ul class="packages">{{$active := false}}{{range .Packages}}{{if eq .State "active"}}{{$active = true}}
        <li class="package"><a href="{{.Path}}">{{.Identity.Name}} {{.Identity.Version}}</a><span>{{.Format}} · {{.Identity.Architecture}}</span></li>{{end}}{{end}}{{if not $active}}
        <li class="package">No packages are available yet.</li>{{end}}
      </ul>
    </section>
  </main>
  <footer><div class="wrap"><p>Created with <a href="https://github.com/asciimoth/ghrepo">ghrepo</a> · <a href="{{.Repository.Repository.URL}}">Source repository</a> · Owner: <a href="{{.Repository.Repository.URL}}/..">__GHREPO_OWNER__</a></p></div></footer>
</body>
</html>
`},
	{"setup.sh", `#!/bin/sh
set -eu
curl -fsSLo KEY.gpg {{shellQuote (print .BaseURL "/KEY.gpg")}}
curl -fsSLo deb.list {{shellQuote (print .BaseURL "/deb.list")}}
curl -fsSLo rpm.repo {{shellQuote (print .BaseURL "/rpm.repo")}}
printf '%s\n' 'Downloaded KEY.gpg, deb.list, and rpm.repo.'
printf '%s\n' 'Review these files before installing them with system privileges.'
`},
	{"deb.list", `deb [signed-by=/usr/share/keyrings/{{.Repository.RPM.ID}}-archive-keyring.gpg] {{.BaseURL}}/deb/ ./
`},
	{"rpm.repo", `[{{.Repository.RPM.ID}}]
name={{.Repository.RPM.Name}}
baseurl={{.BaseURL}}/rpm/
enabled=1
repo_gpgcheck=1
gpgcheck={{.GPGCheck}}
gpgkey={{.RPMKeyURLs}}
`},
}
