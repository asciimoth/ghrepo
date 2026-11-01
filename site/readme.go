package site

import (
	"bytes"
	"fmt"
	"path/filepath"
	"text/template"

	"github.com/asciimoth/ghrepo/contract"
)

// ReadmeTemplate is installed by init and rendered into the repository root.
const ReadmeTemplate = `# {{.Repository.RPM.Name}}

APT and RPM packages published at {{.BaseURL}}.

## Set up this repository

Download the [public key]({{.BaseURL}}/KEY.gpg) and use the
[APT source file]({{.BaseURL}}/deb.list) or
[DNF repository file]({{.BaseURL}}/rpm.repo) for your system.

## Available packages
{{$active := false}}{{range .Packages}}{{if eq .State "active"}}{{$active = true}}
- [{{.Identity.Name}} {{.Identity.Version}}]({{$.BaseURL}}/{{.Path}}) ({{.Format}}, {{.Identity.Architecture}}){{end}}{{end}}{{if not $active}}
No packages are available yet.{{end}}

Repository: {{.Repository.Repository.URL}}
`

// RenderReadme writes the rendered README from repository data.
func RenderReadme(checkout string) error {
	name := filepath.Join(checkout, "templates", "README.md")
	data, err := readOptionalTemplate(name)
	if err != nil {
		return fmt.Errorf("read README template: %w", err)
	}
	r, err := contract.ReadRepository(filepath.Join(checkout, "ghrepo.yaml"))
	if err != nil {
		return err
	}
	m, err := contract.ReadManifest(filepath.Join(checkout, "packages.yaml"), r)
	if err != nil {
		return err
	}
	t, err := template.New("README.md").Option("missingkey=error").Parse(string(data))
	if err != nil {
		return fmt.Errorf("render README.md: %w", err)
	}
	var out bytes.Buffer
	if err := t.Execute(&out, struct {
		Repository contract.Repository
		Packages   []contract.Package
		BaseURL    string
	}{r, m.Packages, r.Site.BaseURL}); err != nil {
		return fmt.Errorf("render README.md: %w", err)
	}
	return contract.WriteAtomic(filepath.Join(checkout, "README.md"), out.Bytes(), 0644)
}
