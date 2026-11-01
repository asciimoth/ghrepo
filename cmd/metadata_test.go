package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestPackageMetadataAndGitHubLookup(t *testing.T) {
	data, err := os.ReadFile("../contract/testdata/packages/hello_1.2.0-1_amd64.deb")
	if err != nil {
		t.Fatal(err)
	}
	asset := "https://github.com/example/project/releases/download/v1/hello.deb"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/example/project" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"description": "A package", "topics": []string{"cli", "linux"}})
	}))
	defer server.Close()
	var output bytes.Buffer
	c := Runner{Dir: t.TempDir(), Out: &output, Fetcher: fixtureFetcher{map[string][]byte{asset: data}}, GitHubAPIURL: server.URL, GitHubClient: server.Client()}
	ctx := context.Background()
	if err := c.Run(ctx, []string{"init", "--repo", "example/repo"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Run(ctx, []string{"add", asset}); err != nil {
		t.Fatal(err)
	}
	_, m, err := c.load()
	if err != nil {
		t.Fatal(err)
	}
	p := m.Packages[0]
	if p.Comment != "A package" || p.SourceURL != "https://github.com/example/project" || strings.Join(p.Tags, ",") != "cli,linux" {
		t.Fatalf("metadata: %+v", p)
	}
	if err := c.Run(ctx, []string{"metadata", "set", "--comment", "Custom", "--tags", "one,two", p.Path}); err != nil {
		t.Fatal(err)
	}
	_, m, err = c.load()
	if err != nil || m.Packages[0].Comment != "Custom" || strings.Join(m.Packages[0].Tags, ",") != "one,two" {
		t.Fatalf("edit: %v %+v", err, m.Packages)
	}
	if err := c.Run(ctx, []string{"metadata", "set", "--dry-run", "--comment", "Ignored", p.Path}); err != nil {
		t.Fatal(err)
	}
	_, m, err = c.load()
	if err != nil || m.Packages[0].Comment != "Custom" {
		t.Fatal("dry run changed metadata", err)
	}
}

func TestGitHubMetadataFailureDoesNotStopAdd(t *testing.T) {
	data, err := os.ReadFile("../contract/testdata/packages/hello_1.2.0-1_amd64.deb")
	if err != nil {
		t.Fatal(err)
	}
	asset := "https://github.com/example/project/releases/download/v1/hello.deb"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	var output bytes.Buffer
	c := Runner{Dir: t.TempDir(), Out: &output, Fetcher: fixtureFetcher{map[string][]byte{asset: data}}, GitHubAPIURL: server.URL, GitHubClient: server.Client()}
	if err := c.Run(context.Background(), []string{"init", "--repo", "example/repo"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Run(context.Background(), []string{"add", asset}); err != nil {
		t.Fatal(err)
	}
	_, m, err := c.load()
	if err != nil || len(m.Packages) != 1 || m.Packages[0].SourceURL != "https://github.com/example/project" {
		t.Fatal("package was not added", err)
	}
	if !strings.Contains(output.String(), "HTTP 503") {
		t.Fatal("lookup failure was not reported", output.String())
	}
}
