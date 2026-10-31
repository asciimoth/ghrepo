package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asciimoth/ghrepo/contract"
	"github.com/asciimoth/ghrepo/inspect"
)

type githubFixtureFetcher struct{ bodies map[string][]byte }

func (f githubFixtureFetcher) Fetch(_ context.Context, raw string, _, _ []string) (io.ReadCloser, error) {
	data, ok := f.bodies[raw]
	if !ok {
		return nil, fmt.Errorf("unknown public URL")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func TestGitHubCommandsOffline(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-secret")
	dir := t.TempDir()
	packageBytes, err := os.ReadFile("../contract/testdata/packages/hello_1.2.0-1_amd64.deb")
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	source := filepath.Join(project, "input.deb")
	if err := os.WriteFile(source, packageBytes, 0600); err != nil {
		t.Fatal(err)
	}
	bodies := map[string][]byte{}
	var uploadedName, publicURL string
	uploadCalls := 0
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Error("missing token")
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/example/repo":
			w.WriteHeader(404)
		case r.Method == "GET" && r.URL.Path == "/user":
			_, _ = io.WriteString(w, `{"login":"example"}`)
		case r.Method == "POST" && r.URL.Path == "/user/repos":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["private"] != false || body["name"] != "repo" {
				t.Errorf("wrong repository request")
			}
			w.WriteHeader(201)
		case r.Method == "GET" && r.URL.Path == "/repos/example/repo/pages":
			w.WriteHeader(404)
		case r.Method == "POST" && r.URL.Path == "/repos/example/repo/pages":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["build_type"] != "workflow" {
				t.Error("wrong Pages setting")
			}
			w.WriteHeader(201)
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/releases/tags/pkg-1"):
			w.WriteHeader(404)
		case r.Method == "POST" && r.URL.Path == "/repos/example/repo/releases":
			w.WriteHeader(201)
			_, _ = fmt.Fprintf(w, `{"id":1,"tag_name":"pkg-1","upload_url":%q}`, server.URL+"/repos/example/repo/releases/1/assets{?name,label}")
		case r.Method == "GET" && r.URL.Path == "/repos/example/repo/releases/1/assets":
			if uploadedName == "" {
				_, _ = io.WriteString(w, "[]")
			} else {
				orphanName := strings.Repeat("a", 64) + ".deb"
				orphanURL := "https://github.com/example/repo/releases/download/pkg-1/" + orphanName
				_, _ = fmt.Fprintf(w, `[{"id":1,"name":%q,"state":"uploaded","size":%d,"browser_download_url":%q},{"id":2,"name":%q,"state":"uploaded","size":1,"browser_download_url":%q}]`, uploadedName, len(packageBytes), publicURL, orphanName, orphanURL)
			}
		case r.Method == "POST" && r.URL.Path == "/repos/example/repo/releases/1/assets":
			uploadCalls++
			if r.ContentLength != int64(len(packageBytes)) {
				t.Errorf("upload content length: %d", r.ContentLength)
			}
			uploadedName = r.URL.Query().Get("name")
			raw, _ := io.ReadAll(r.Body)
			if !bytes.Equal(raw, packageBytes) {
				t.Error("wrong uploaded bytes")
			}
			publicURL = "https://github.com/example/repo/releases/download/pkg-1/" + uploadedName
			bodies[publicURL] = raw
			w.WriteHeader(201)
			_, _ = fmt.Fprintf(w, `{"id":1,"name":%q,"state":"uploaded","size":%d,"browser_download_url":%q}`, uploadedName, len(raw), publicURL)
		case r.Method == "GET" && r.URL.Path == "/repos/example/repo/releases":
			_, _ = io.WriteString(w, `[{"id":1,"tag_name":"pkg-1"}]`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	c := Runner{Dir: dir, GitHubAPIURL: server.URL, GitHubClient: server.Client(), Fetcher: githubFixtureFetcher{bodies: bodies}}
	ctx := context.Background()
	if err := c.Run(ctx, []string{"init", "--repo", "https://github.com/example/repo", "--site", "https://example.github.io/repo", "--create-remote", "--configure-pages"}); err != nil {
		t.Fatal(err)
	}
	repo, err := contract.ReadRepository(filepath.Join(dir, "ghrepo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	repo.Storage = contract.Storage{Backend: "github-releases", ReleasePrefix: "pkg", BucketSize: 1}
	if err := contract.WriteRepository(filepath.Join(dir, "ghrepo.yaml"), repo); err != nil {
		t.Fatal(err)
	}
	meta, size, digest, err := inspect.File(source, repo.Downloads.MaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	upstream := contract.Package{URL: "https://github.com/upstream/project/releases/download/v1/hello.deb", SHA256: digest, Size: size, Source: "upstream", State: "active", Format: meta.Format, Identity: meta.Identity}
	upstream.Path, err = contract.ServedPath(upstream)
	if err != nil {
		t.Fatal(err)
	}
	if err := contract.WriteManifest(filepath.Join(dir, "packages.yaml"), contract.Manifest{Schema: 1, Packages: []contract.Package{upstream}}, repo); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	relativeRepo, err := filepath.Rel(project, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GHREPO_REPO_DIR", relativeRepo)
	c.Dir = ""
	if err := c.Run(ctx, []string{"add", "--upload", "input.deb"}); err != nil {
		t.Fatal(err)
	}
	manifest, err := contract.ReadManifest(filepath.Join(dir, "packages.yaml"), repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Packages) != 1 || manifest.Packages[0].Source != "owned" || manifest.Packages[0].URL != publicURL || uploadedName == "" {
		t.Fatalf("wrong manifest: %+v", manifest)
	}
	for _, name := range []string{"ghrepo.yaml", "packages.yaml", ".github/workflows/pages.yml"} {
		data, e := os.ReadFile(filepath.Join(dir, name))
		if e != nil {
			t.Fatal(e)
		}
		if bytes.Contains(data, []byte("test-secret")) {
			t.Fatalf("token in %s", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".ghrepo-upload")); !os.IsNotExist(err) {
		t.Fatal("upload progress was not cleared")
	}
	if err := contract.WriteManifest(filepath.Join(dir, "packages.yaml"), contract.Manifest{Schema: 1, Packages: []contract.Package{upstream}}, repo); err != nil {
		t.Fatal(err)
	}
	progressDir := filepath.Join(dir, ".ghrepo-upload")
	if err := os.MkdirAll(progressDir, 0700); err != nil {
		t.Fatal(err)
	}
	record, _ := json.Marshal(uploadProgress{Name: uploadedName, SHA256: digest, URL: publicURL})
	if err := os.WriteFile(filepath.Join(progressDir, digest+".json"), record, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GHREPO_REPO_DIR", project)
	if err := c.Run(ctx, []string{"add", "--repo-dir", dir, "--upload", "input.deb"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GHREPO_REPO_DIR", relativeRepo)
	if uploadCalls != 1 {
		t.Fatalf("resume uploaded again: %d calls", uploadCalls)
	}
	var out bytes.Buffer
	c.Out = &out
	if err := c.Run(ctx, []string{"gc"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), uploadedName) || !strings.Contains(out.String(), strings.Repeat("a", 64)+".deb") {
		t.Fatalf("incorrect gc report: %s", out.String())
	}
	for _, args := range [][]string{{"add", "ghrepo.yaml", "packages.yaml"}, {"-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "save rollback manifest"}} {
		command := exec.Command("git", args...)
		command.Dir = dir
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git commit failed: %v %s", err, output)
		}
	}
	emptyTree := exec.Command("git", "hash-object", "-t", "tree", "--stdin")
	emptyTree.Dir = dir
	emptyTree.Stdin = strings.NewReader("")
	treeID, e := emptyTree.Output()
	if e != nil {
		t.Fatal(e)
	}
	orphan := exec.Command("git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit-tree", strings.TrimSpace(string(treeID)), "-m", "unrelated branch")
	orphan.Dir = dir
	orphanID, e := orphan.Output()
	if e != nil {
		t.Fatal(e)
	}
	update := exec.Command("git", "update-ref", "refs/heads/unrelated", strings.TrimSpace(string(orphanID)))
	update.Dir = dir
	if e := update.Run(); e != nil {
		t.Fatal(e)
	}
	if err := contract.WriteManifest(filepath.Join(dir, "packages.yaml"), contract.Manifest{Schema: 1, Packages: []contract.Package{}}, repo); err != nil {
		t.Fatal(err)
	}
	repo.Downloads.SourceHosts = []string{"example.com"}
	if err := contract.WriteRepository(filepath.Join(dir, "ghrepo.yaml"), repo); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := c.Run(ctx, []string{"gc"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), uploadedName) {
		t.Fatalf("rollback asset reported: %s", out.String())
	}
}

func TestGitHubRPMUploadOffline(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-secret")
	dir := t.TempDir()
	rpmPath := filepath.Join(dir, "input.rpm")
	raw, err := os.ReadFile("../contract/testdata/packages/empty-0.1-1.x86_64.rpm")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rpmPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	bodies := map[string][]byte{}
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/releases/tags/pkg-1"):
			w.WriteHeader(404)
		case r.Method == "POST" && r.URL.Path == "/repos/example/repo/releases":
			w.WriteHeader(201)
			_, _ = fmt.Fprintf(w, `{"id":1,"tag_name":"pkg-1","upload_url":%q}`, server.URL+"/repos/example/repo/releases/1/assets{?name,label}")
		case r.Method == "GET" && r.URL.Path == "/repos/example/repo/releases/1/assets":
			_, _ = io.WriteString(w, "[]")
		case r.Method == "POST" && r.URL.Path == "/repos/example/repo/releases/1/assets":
			if r.ContentLength != int64(len(raw)) {
				t.Errorf("wrong upload size: %d", r.ContentLength)
			}
			got, _ := io.ReadAll(r.Body)
			if !bytes.Equal(got, raw) {
				t.Error("RPM upload bytes changed")
			}
			name := r.URL.Query().Get("name")
			if !strings.HasSuffix(name, ".rpm") {
				t.Errorf("wrong asset name: %s", name)
			}
			public := "https://github.com/example/repo/releases/download/pkg-1/" + name
			bodies[public] = got
			w.WriteHeader(201)
			_, _ = fmt.Fprintf(w, `{"id":1,"name":%q,"state":"uploaded","size":%d,"browser_download_url":%q}`, name, len(got), public)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	c := Runner{Dir: dir, GitHubAPIURL: server.URL, GitHubClient: server.Client(), Fetcher: githubFixtureFetcher{bodies: bodies}}
	ctx := context.Background()
	if err := c.Run(ctx, []string{"init", "--repo", "https://github.com/example/repo", "--site", "https://example.github.io/repo"}); err != nil {
		t.Fatal(err)
	}
	repo, err := contract.ReadRepository(filepath.Join(dir, "ghrepo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	repo.Storage = contract.Storage{Backend: "github-releases", ReleasePrefix: "pkg", BucketSize: 1}
	if err := contract.WriteRepository(filepath.Join(dir, "ghrepo.yaml"), repo); err != nil {
		t.Fatal(err)
	}
	if err := c.Run(ctx, []string{"add", "--upload", rpmPath}); err != nil {
		t.Fatal(err)
	}
	manifest, err := contract.ReadManifest(filepath.Join(dir, "packages.yaml"), repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Packages) != 1 || manifest.Packages[0].Format != "rpm" || manifest.Packages[0].Source != "owned" {
		t.Fatalf("wrong RPM manifest: %+v", manifest)
	}
}
