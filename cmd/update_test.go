package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateGitHubRelease(t *testing.T) {
	oldURL := "https://github.com/example/project/releases/download/v1.2.0-1/hello_1.2.0-1_amd64.deb"
	newURL := "https://github.com/example/project/releases/download/v2.0.0-1/hello_2.0.0-1_amd64.deb"
	oldBytes, err := os.ReadFile("../contract/testdata/packages/hello_1.2.0-1_amd64.deb")
	if err != nil {
		t.Fatal(err)
	}
	newBytes, err := os.ReadFile("../contract/testdata/packages/hello_2.0.0-1_amd64.deb")
	if err != nil {
		t.Fatal(err)
	}
	lookupCount := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lookupCount++
		if r.URL.Path != "/repos/example/project/releases/latest" && r.URL.Path != "/repos/example/project" {
			t.Errorf("wrong API path: %s", r.URL.Path)
		}
		if r.URL.Path == "/repos/example/project" {
			_ = json.NewEncoder(w).Encode(map[string]any{"description": "Hello package", "topics": []string{"tools"}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v2.0.0-1", "assets": []map[string]string{{"name": "hello_2.0.0-1_amd64.deb", "browser_download_url": newURL, "state": "uploaded"}}})
	}))
	defer server.Close()
	dir := t.TempDir()
	var output bytes.Buffer
	c := Runner{Dir: dir, Out: &output, GitHubAPIURL: server.URL, GitHubClient: server.Client(), Fetcher: fixtureFetcher{map[string][]byte{oldURL: oldBytes, newURL: newBytes}}}
	ctx := context.Background()
	if err := c.Run(ctx, []string{"init", "--repo", "example/repo"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Run(ctx, []string{"add", oldURL}); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	manifestPath := filepath.Join(dir, "packages.yaml")
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Run(ctx, []string{"update", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "latest 2.0.0-1 (release v2.0.0-1)") || !strings.Contains(output.String(), newURL) {
		t.Fatal(output.String())
	}
	afterDry, err := os.ReadFile(manifestPath)
	if err != nil || !bytes.Equal(before, afterDry) {
		t.Fatal("dry run changed manifest", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "local-cache", "sha256", "c3e3f260e1c52160f071cb402e1ff210ff8fbaf63f57421f25737abc7114d918")); !os.IsNotExist(err) {
		t.Fatal("dry run downloaded package")
	}
	output.Reset()
	if err := c.Run(ctx, []string{"update"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "added 2.0.0-1") {
		t.Fatal(output.String())
	}
	r, m, err := c.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Packages) != 2 || m.Packages[1].Identity.Version != "2.0.0-1" {
		t.Fatal(m.Packages)
	}
	if err := m.Validate(r); err != nil {
		t.Fatal(err)
	}
	if lookupCount < 2 {
		t.Fatal(lookupCount)
	}
	beforeRepeat, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := c.Run(ctx, []string{"update"}); err != nil {
		t.Fatal(err)
	}
	afterRepeat, err := os.ReadFile(manifestPath)
	if err != nil || !bytes.Equal(beforeRepeat, afterRepeat) || !strings.Contains(output.String(), "already added") {
		t.Fatal("repeat update changed manifest or omitted status", err, output.String())
	}
	if err := c.Run(ctx, []string{"update", "unexpected"}); err == nil {
		t.Fatal("accepted unexpected argument")
	}
}

func TestUpdateSkipsUnsupportedSources(t *testing.T) {
	cases := []string{
		"https://example.com/file.deb",
		"https://github.com/example/project/archive/v1.deb",
		"https://github.com/example/project/releases/download/v1/file.deb?token=secret",
		"https://github.com/example/project/releases/download/bad%2Ftag/file.deb",
	}
	for _, raw := range cases {
		if _, ok := parseReleaseSource(raw); ok {
			t.Fatal("accepted", raw)
		}
	}
}

func TestReleaseAssetNames(t *testing.T) {
	source := releaseSource{owner: "example", repo: "project", tag: "v1.0", asset: "app-v1.0-1.0-amd64.deb"}
	for _, name := range []string{
		"app-v2.0-2.0-amd64.deb",
		"app-v2.0-1.0-amd64.deb",
		"app-v1.0-2.0-amd64.deb",
		"app-v1.0-1.0-amd64.deb",
	} {
		release := latestRelease{Tag: "v2.0"}
		release.Assets = append(release.Assets, latestAsset{Name: name, URL: "https://github.com/example/project/releases/download/v2.0/" + name, State: "uploaded"})
		if got, ok := releaseAsset(source, release, "1.0"); !ok || !strings.HasSuffix(got, "/"+name) {
			t.Fatalf("did not match %s: %s, %t", name, got, ok)
		}
	}
	bad := latestRelease{Tag: "v2.0", Assets: []latestAsset{{Name: "app-v2.0-2.0-amd64.deb", URL: "https://example.com/file.deb", State: "uploaded"}}}
	if _, ok := releaseAsset(source, bad, "1.0"); ok {
		t.Fatal("accepted release asset from a different URL")
	}
}

func TestUpdateRejectsOtherPackage(t *testing.T) {
	oldURL := "https://github.com/example/project/releases/download/v1/hello.deb"
	newURL := "https://github.com/example/project/releases/download/v2/hello.deb"
	oldBytes, err := os.ReadFile("../contract/testdata/packages/hello_1.2.0-1_amd64.deb")
	if err != nil {
		t.Fatal(err)
	}
	wrongBytes, err := os.ReadFile("../contract/testdata/packages/empty-0.1-1.x86_64.rpm")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v2", "assets": []map[string]string{{"name": "hello.deb", "browser_download_url": newURL, "state": "uploaded"}}})
	}))
	defer server.Close()
	dir := t.TempDir()
	var output bytes.Buffer
	c := Runner{Dir: dir, Out: &output, GitHubAPIURL: server.URL, GitHubClient: server.Client(), Fetcher: fixtureFetcher{map[string][]byte{oldURL: oldBytes, newURL: wrongBytes}}}
	ctx := context.Background()
	if err := c.Run(ctx, []string{"init", "--repo", "example/repo"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Run(ctx, []string{"add", oldURL}); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	manifestPath := filepath.Join(dir, "packages.yaml")
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"update", "--dry-run"}, {"update"}} {
		if err := c.Run(ctx, args); err == nil || !strings.Contains(err.Error(), "different package identity") {
			t.Fatalf("%v: expected identity error, got %v", args, err)
		}
		after, err := os.ReadFile(manifestPath)
		if err != nil || !bytes.Equal(before, after) || output.Len() != 0 {
			t.Fatal("failed update changed manifest or reported success", err, output.String())
		}
	}
}
