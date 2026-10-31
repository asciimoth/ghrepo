package site

// DefaultTemplates are source templates installed by init. Existing files are
// never replaced, so a repository can customize any page or client file.
var DefaultTemplates = []struct{ Name, Text string }{
	{"index.html", `<!doctype html>
<html lang="en"><meta charset="utf-8"><title>Packages</title>
<h1>Packages</h1>
<p><a href="deb.list">APT source</a> · <a href="rpm.repo">RPM source</a> · <a href="README.md">Setup notes</a></p>
<ul>{{range .Packages}}{{if eq .State "active"}}<li><a href="{{.Path}}">{{.Identity.Name}} {{.Identity.Version}} ({{.Identity.Architecture}})</a></li>{{end}}{{end}}</ul>
</html>
`},
	{"README.md", `# Packages

APT source: {{.BaseURL}}/deb/

RPM source: {{.BaseURL}}/rpm/

Download [KEY.gpg]({{.BaseURL}}/KEY.gpg) before enabling either source.
Use [deb.list]({{.BaseURL}}/deb.list) or [rpm.repo]({{.BaseURL}}/rpm.repo)
for the client configuration. The APT keyring path in deb.list is
/usr/share/keyrings/{{.Repository.RPM.ID}}-archive-keyring.gpg.

## Active packages

{{range .Packages}}{{if eq .State "active"}}- [{{.Identity.Name}} {{.Identity.Version}}]({{$.BaseURL}}/{{.Path}})
{{end}}{{end}}`},
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
