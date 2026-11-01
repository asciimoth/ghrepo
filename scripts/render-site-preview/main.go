// Render the default packages page with fictional data:
// go run ./scripts/render-site-preview /tmp/ghrepo-preview.html
// chromium --headless --no-sandbox --disable-gpu --hide-scrollbars --disable-dev-shm-usage --screenshot=/tmp/ghrepo-preview.png --window-size=1280,1800 file:///tmp/ghrepo-preview.html
// magick /tmp/ghrepo-preview.png -crop 1280x1530+0+0 +repage -colors 64 PNG8:docs/packages-page-preview.png
package main

import (
	"fmt"
	"html/template"
	"os"

	"github.com/asciimoth/ghrepo/contract"
	"github.com/asciimoth/ghrepo/site"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./scripts/render-site-preview OUTPUT.html")
		os.Exit(2)
	}

	const repositoryURL = "https://example.invalid/sample-publisher/sample-repo"
	items, err := site.TemplatesFor(repositoryURL)
	if err != nil {
		fail(err)
	}

	data := struct {
		Repository contract.Repository
		Packages   []contract.Package
		BaseURL    string
		SetupName  string
		GPGCheck   int
		RPMKeyURLs string
	}{
		Repository: contract.Repository{
			Repository: contract.Repo{URL: repositoryURL},
			RPM:        contract.RPM{Name: "Sample Packages"},
		},
		Packages: []contract.Package{
			{
				State: "active", Format: "deb", Path: "deb/sample-tool_1.2.0_amd64.deb",
				Identity: contract.Identity{Name: "sample-tool", Version: "1.2.0", Architecture: "amd64"},
				Comment:  "Example command-line utility for Debian and Ubuntu.",
				Tags:     []string{"cli", "utilities"},
			},
			{
				State: "active", Format: "rpm", Path: "rpm/sample-tool-1.2.0-1.x86_64.rpm",
				Identity: contract.Identity{Name: "sample-tool", Version: "1.2.0", Architecture: "x86_64"},
				Comment:  "Example command-line utility for Fedora and other DNF systems.",
				Tags:     []string{"cli", "utilities"},
			},
		},
		BaseURL:   "https://packages.example.invalid",
		SetupName: "sample-publisher-sample-repo",
	}

	output, err := os.Create(os.Args[1])
	if err != nil {
		fail(err)
	}
	found := false
	for _, item := range items {
		if item.Name == "index.html" {
			found = true
			var page *template.Template
			page, err = template.New(item.Name).Option("missingkey=error").Parse(item.Text)
			if err == nil {
				err = page.Execute(output, data)
			}
			break
		}
	}
	if !found {
		fail(fmt.Errorf("default index template not found"))
	}
	if err != nil {
		fail(err)
	}
	if err := output.Close(); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
