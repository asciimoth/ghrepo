package site

import (
	"bytes"
	"html/template"
	"strings"
	"testing"

	"github.com/asciimoth/ghrepo/contract"
)

func TestDefaultIndexShowsPackageMetadata(t *testing.T) {
	items, err := TemplatesFor("https://github.com/example/repo")
	if err != nil {
		t.Fatal(err)
	}
	var page string
	for _, item := range items {
		if item.Name == "index.html" {
			page = item.Text
		}
	}
	tmpl, err := template.New("index.html").Parse(page)
	if err != nil {
		t.Fatal(err)
	}
	data := struct {
		Repository contract.Repository
		Packages   []contract.Package
		BaseURL    string
		GPGCheck   int
		RPMKeyURLs string
	}{Repository: contract.Repository{Repository: contract.Repo{URL: "https://github.com/example/repo"}, RPM: contract.RPM{Name: "Example", ID: "example"}}, Packages: []contract.Package{{State: "active", Path: "package.deb", Comment: "A <package>", SourceURL: "https://github.com/example/project", Tags: []string{"cli", "linux"}}}}
	var output bytes.Buffer
	if err := tmpl.Execute(&output, data); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"A &lt;package&gt;", "href=\"https://github.com/example/project\"", "Tags: cli, linux"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing %q", want)
		}
	}
}
