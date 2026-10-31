package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/asciimoth/ghrepo/contract"
	"github.com/asciimoth/ghrepo/inspect"
	"github.com/asciimoth/ghrepo/rpm"
	"gopkg.in/yaml.v3"
)

func command(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = testCommandEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %v: %s", args, err, out)
	}
}

func testCommandEnv() []string {
	var env []string
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "GIT_INDEX_FILE=") {
			env = append(env, item)
		}
	}
	return env
}
func prepareFlow(t *testing.T) (Runner, string) {
	t.Helper()
	for _, name := range []string{"gpg", "gpgv", "git"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip(name + " is unavailable")
		}
	}
	dir := t.TempDir()
	home := filepath.Join(dir, "gnupg")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GNUPGHOME", home)
	key := "Flow Test <flow@example.org>"
	command(t, dir, "gpg", "--batch", "--pinentry-mode", "loopback", "--passphrase", "", "--quick-generate-key", key, "rsa2048", "sign", "0")
	deb := mustRead(t, "../contract/testdata/packages/hello_1.2.0-1_amd64.deb")
	rpm := mustRead(t, "../contract/testdata/packages/empty-0.1-1.x86_64.rpm")
	fetch := fixtureFetcher{map[string][]byte{"https://example.com/hello.deb": deb, "https://example.com/empty.rpm": rpm}}
	runner := Runner{Dir: dir, Fetcher: fetch}
	ctx := context.Background()
	if err := runner.Run(ctx, []string{"init", "--repo", "https://github.com/example/repo", "--site", "https://example.github.io/repo", "--source-hosts", "example.com", "--redirect-hosts", "", "--key-id", key}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx, []string{"add", "https://example.com/hello.deb", "https://example.com/empty.rpm"}); err != nil {
		t.Fatal(err)
	}
	return runner, dir
}
func TestRebuildCheckDryRunAndPublish(t *testing.T) {
	runner, dir := prepareFlow(t)
	ctx := context.Background()
	if err := runner.Run(ctx, []string{"rebuild"}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx, []string{"check"}); err != nil {
		t.Fatal(err)
	}
	before := mustRead(t, filepath.Join(dir, "deb", "InRelease"))
	for _, args := range [][]string{{"rebuild", "--dry-run"}, {"check", "--dry-run"}, {"site-build", "--dry-run"}} {
		if err := runner.Run(ctx, args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if !bytes.Equal(before, mustRead(t, filepath.Join(dir, "deb", "InRelease"))) {
			t.Fatalf("%v changed signed metadata", args)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "build", "site")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry site-build changed repository: %v", err)
	}
	// A bad source leaves the signed files intact.
	r, m, err := runner.load()
	if err != nil {
		t.Fatal(err)
	}
	old := m.Packages[0].URL
	m.Packages[0].URL = "https://example.com/missing"
	if err := contract.WriteManifest(filepath.Join(dir, "packages.yaml"), m, r); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "local-cache", "sha256", m.Packages[0].SHA256)); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx, []string{"rebuild"}); err == nil {
		t.Fatal("rebuild accepted missing source")
	}
	if !bytes.Equal(before, mustRead(t, filepath.Join(dir, "deb", "InRelease"))) {
		t.Fatal("failed rebuild changed signed metadata")
	}
	m.Packages[0].URL = old
	if err := contract.WriteManifest(filepath.Join(dir, "packages.yaml"), m, r); err != nil {
		t.Fatal(err)
	}
	remote := filepath.Join(t.TempDir(), "remote.git")
	command(t, dir, "git", "init", "--bare", remote)
	command(t, dir, "git", "remote", "add", "origin", remote)
	command(t, dir, "git", "config", "user.name", "Flow Test")
	command(t, dir, "git", "config", "user.email", "flow@example.org")
	// An ignored secret must not enter the intended change set.
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("GH_TOKEN=secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	runner.Out = &output
	if err := runner.Run(ctx, []string{"publish", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "local-cache", "sha256", m.Packages[0].SHA256)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry publish changed cache: %v", err)
	}
	if !bytes.Equal(before, mustRead(t, filepath.Join(dir, "deb", "InRelease"))) {
		t.Fatal("dry publish changed signed metadata")
	}
	text := output.String()
	if !strings.Contains(text, "packages.yaml") || !strings.Contains(text, "deb/InRelease") || strings.Contains(text, ".env") || strings.Contains(text, "local-cache") {
		t.Fatalf("wrong dry-run: %s", text)
	}
	if err := runner.Run(ctx, []string{"publish", "--message", "chore(repo): publish fixtures"}); err != nil {
		t.Fatal(err)
	}
	command(t, dir, "git", "--git-dir", remote, "show", "main:packages.yaml")
	staged, err := runner.gitOutput("ls-tree", "-r", "--name-only", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(staged), "\n") {
		if line != "" && !runner.permittedGitPath(line) {
			t.Fatalf("committed forbidden path %s", line)
		}
		if line != "" && !strings.Contains(text, "A "+line+"\n") {
			t.Fatalf("dry-run omitted committed path %s: %s", line, text)
		}
	}
	if len(strings.Split(strings.TrimSpace(text), "\n")) != len(strings.Split(strings.TrimSpace(string(staged)), "\n")) {
		t.Fatalf("dry-run path count differs from commit: %s", text)
	}
}

func TestCopyPreviewRepositoryKeepsSymlinksInsideCopy(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "template.txt"), []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("template.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	preview, err := copyPreviewRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(preview) }()
	if err := os.WriteFile(filepath.Join(preview, "link.txt"), []byte("after"), 0600); err != nil {
		t.Fatal(err)
	}
	if string(mustRead(t, filepath.Join(root, "template.txt"))) != "before" {
		t.Fatal("preview symlink changed source")
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), filepath.Join(root, "outside-link")); err != nil {
		t.Fatal(err)
	}
	if _, err := copyPreviewRepository(root); err == nil {
		t.Fatal("accepted symlink outside repository")
	}
}
func TestRecoverInterruptedMetadataSwap(t *testing.T) {
	runner, dir := prepareFlow(t)
	ctx := context.Background()
	if err := runner.Run(ctx, []string{"rebuild"}); err != nil {
		t.Fatal(err)
	}
	before := mustRead(t, filepath.Join(dir, "deb", "Release"))
	tx := runner.transaction()
	if err := os.Mkdir(tx, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tx, "pending.json"), []byte(`{"had_deb":true,"had_rpm":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "deb"), filepath.Join(tx, "old-deb")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "deb"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := runner.recoverTransaction(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, mustRead(t, filepath.Join(dir, "deb", "Release"))) {
		t.Fatal("recovery did not restore metadata")
	}
}

func TestFailedMetadataBuildPreservesPreviousFiles(t *testing.T) {
	runner, dir := prepareFlow(t)
	ctx := context.Background()
	if err := runner.Run(ctx, []string{"rebuild"}); err != nil {
		t.Fatal(err)
	}
	before := mustRead(t, filepath.Join(dir, "deb", "InRelease"))
	bad := filepath.Join(dir, "rpm", "repodata", strings.Repeat("a", 64)+"-primary.xml.gz")
	if err := os.WriteFile(bad, []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx, []string{"rebuild"}); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected metadata build failure, got %v", err)
	}
	if !bytes.Equal(before, mustRead(t, filepath.Join(dir, "deb", "InRelease"))) {
		t.Fatal("failed build changed APT signature")
	}
}

func TestCheckRejectsMissingSourceAndOversizeSite(t *testing.T) {
	runner, dir := prepareFlow(t)
	ctx := context.Background()
	if err := runner.Run(ctx, []string{"rebuild"}); err != nil {
		t.Fatal(err)
	}
	r, m, err := runner.load()
	if err != nil {
		t.Fatal(err)
	}
	missing := m.Packages[0].URL
	delete(runner.Fetcher.(fixtureFetcher).data, missing)
	if err := runner.Run(ctx, []string{"check"}); err == nil {
		t.Fatal("check accepted missing source with a cached copy")
	}
	runner.Fetcher.(fixtureFetcher).data[missing] = mustRead(t, "../contract/testdata/packages/hello_1.2.0-1_amd64.deb")
	r.Site.MaxBytes = 1
	if err := contract.WriteRepository(filepath.Join(dir, "ghrepo.yaml"), r); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx, []string{"check"}); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("expected site size rejection, got %v", err)
	}
}
func TestWorkflowConfiguration(t *testing.T) {
	r := contract.Repository{Repository: contract.Repo{Branch: "release"}}
	s := workflowFor(r)
	for _, want := range []string{"branches: [\"release\"]", "group: pages-release", "cancel-in-progress: false", "path: build/site", "needs: build", "pages: write", "id-token: write", "environment:\n      name: github-pages"} {
		if !strings.Contains(s, want) {
			t.Fatalf("workflow lacks %q", want)
		}
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(s), &doc); err != nil {
		t.Fatal(err)
	}
	jobs, ok := doc["jobs"].(map[string]any)
	if !ok || len(jobs) != 2 {
		t.Fatalf("expected two workflow jobs, got %v", doc["jobs"])
	}
	build, ok := jobs["build"].(map[string]any)
	if !ok {
		t.Fatal("missing build job")
	}
	deploy, ok := jobs["deploy"].(map[string]any)
	if !ok || deploy["needs"] != "build" {
		t.Fatal("deploy does not depend on build")
	}
	permissions, ok := deploy["permissions"].(map[string]any)
	if !ok || permissions["pages"] != "write" || permissions["id-token"] != "write" {
		t.Fatal("deploy permissions are incomplete")
	}
	steps, ok := build["steps"].([]any)
	if !ok || len(steps) != 4 {
		t.Fatalf("unexpected build steps: %v", build["steps"])
	}
	upload, ok := steps[3].(map[string]any)
	if !ok || upload["uses"] != "actions/upload-pages-artifact@v3" {
		t.Fatal("missing artifact upload")
	}
	with, ok := upload["with"].(map[string]any)
	if !ok || with["path"] != "build/site" {
		t.Fatal("artifact path is not build/site")
	}
	if !strings.Contains(s, "go run github.com/asciimoth/ghrepo@"+siteBuilderRevision+" site-build") || !strings.Contains(s, "go run . site-build") {
		t.Fatal("workflow cannot run from a data-only checkout")
	}
	if strings.Contains(s, "ghrepo@main") {
		t.Fatal("workflow uses a moving builder revision")
	}
}

func TestPinnedWorkflowBuilderSupportsReplacement(t *testing.T) {
	if _, err := os.Stat("../.git"); err != nil {
		t.Skip("source Git history is unavailable")
	}
	cmd := exec.Command("git", "show", siteBuilderRevision+":contract/validate.go")
	cmd.Dir = ".."
	source, err := cmd.Output()
	if err != nil {
		t.Fatalf("pinned builder revision is unavailable: %v", err)
	}
	if !bytes.Contains(source, []byte("activeIdentities")) {
		t.Fatal("pinned builder rejects retained replacement packages")
	}
}

func TestPinnedBuilderRerunAfterMainMoves(t *testing.T) {
	if _, err := os.Stat("../.git"); err != nil {
		t.Skip("source Git history is unavailable")
	}
	c, dataDir := prepareFlow(t)
	_, original, err := c.load()
	if err != nil {
		t.Fatal(err)
	}
	newDEB := mustRead(t, "../contract/testdata/packages/hello_changed.deb")
	newRPM := append(mustRead(t, "../contract/testdata/packages/empty-0.1-1.x86_64.rpm"), 0)
	fetcher := c.Fetcher.(fixtureFetcher)
	fetcher.data["https://example.com/new.deb"] = newDEB
	fetcher.data["https://example.com/new.rpm"] = newRPM
	c.Fetcher = fetcher
	for _, p := range original.Packages {
		url := "https://example.com/new.deb"
		if p.Format == "rpm" {
			url = "https://example.com/new.rpm"
		}
		if err := c.Run(context.Background(), []string{"replace", p.Path, url}); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Run(context.Background(), []string{"rebuild"}); err != nil {
		t.Fatal(err)
	}
	command(t, dataDir, "git", "init")
	command(t, dataDir, "git", "add", "ghrepo.yaml", "packages.yaml", "KEY.gpg", "deb", "rpm", ".github", "templates")
	command(t, dataDir, "git", "-c", "commit.gpgsign=false", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "site inputs")
	dataCommit := strings.TrimSpace(string(mustGitOutput(t, dataDir, "rev-parse", "HEAD")))
	fixtures := filepath.Join(t.TempDir(), "fixtures")
	if err := os.Mkdir(fixtures, 0755); err != nil {
		t.Fatal(err)
	}
	for source, name := range map[string]string{"../contract/testdata/packages/hello_1.2.0-1_amd64.deb": "hello.deb", "../contract/testdata/packages/empty-0.1-1.x86_64.rpm": "empty.rpm", "../contract/testdata/packages/hello_changed.deb": "new.deb"} {
		if err := os.WriteFile(filepath.Join(fixtures, name), mustRead(t, source), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(fixtures, "new.rpm"), newRPM, 0644); err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(t.TempDir(), "source")
	command(t, "..", "git", "clone", "--no-hardlinks", "--quiet", ".", clone)
	pinned := filepath.Join(t.TempDir(), "pinned")
	command(t, clone, "git", "worktree", "add", "--quiet", "--detach", pinned, siteBuilderRevision)
	harness := filepath.Join(pinned, "fixture-builder")
	if err := os.Mkdir(harness, 0755); err != nil {
		t.Fatal(err)
	}
	const source = `package main
import (
 "bytes"
 "context"
 "io"
 "net/url"
 "os"
 "path/filepath"
 "github.com/asciimoth/ghrepo/site"
)
type fetcher struct{}
func (fetcher) Fetch(_ context.Context, raw string, _, _ []string) (io.ReadCloser, error) {
 u, err := url.Parse(raw)
 if err != nil { return nil, err }
 data, err := os.ReadFile(filepath.Join(os.Getenv("GHREPO_TEST_FIXTURES"), filepath.Base(u.Path)))
 if err != nil { return nil, err }
 return io.NopCloser(bytes.NewReader(data)), nil
}
func main() {
 if err := site.Build(context.Background(), os.Args[1], os.Args[2], fetcher{}); err != nil { panic(err) }
}
`
	if err := os.WriteFile(filepath.Join(harness, "main.go"), []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
	build := func(output string) map[string]string {
		t.Helper()
		cmd := exec.Command("go", "run", "./fixture-builder", dataDir, output)
		cmd.Dir = pinned
		cmd.Env = append(os.Environ(), "GHREPO_TEST_FIXTURES="+fixtures)
		if text, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("pinned builder failed: %v: %s", err, text)
		}
		return siteChecksums(t, output)
	}
	first := build(filepath.Join(t.TempDir(), "site"))
	if err := os.WriteFile(filepath.Join(clone, "main-changed.txt"), []byte("new main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	command(t, clone, "git", "add", "main-changed.txt")
	command(t, clone, "git", "-c", "commit.gpgsign=false", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "move main")
	if got := strings.TrimSpace(string(mustGitOutput(t, dataDir, "rev-parse", "HEAD"))); got != dataCommit {
		t.Fatal("site input commit changed")
	}
	second := build(filepath.Join(t.TempDir(), "site"))
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("pinned builder changed site after main moved: %v %v", first, second)
	}
}

func mustGitOutput(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = testCommandEnv()
	data, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func siteChecksums(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[rel] = fmt.Sprintf("%x", sum)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestPublishRejectsStagedLocalFile(t *testing.T) {
	runner, dir := prepareFlow(t)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("GH_TOKEN=secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command(t, dir, "git", "add", "-f", ".env")
	if err := runner.Run(context.Background(), []string{"publish", "--dry-run"}); err == nil || !strings.Contains(err.Error(), "staged path is not allowed") {
		t.Fatalf("expected staging rejection, got %v", err)
	}
}

func TestPublishRejectsRenameFromLocalFile(t *testing.T) {
	runner, dir := prepareFlow(t)
	command(t, dir, "git", "config", "user.name", "Flow Test")
	command(t, dir, "git", "config", "user.email", "flow@example.org")
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("GH_TOKEN=secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command(t, dir, "git", "add", "-f", ".env")
	command(t, dir, "git", "commit", "-m", "test baseline")
	command(t, dir, "git", "mv", "-f", ".env", "KEY.gpg")
	if err := runner.verifyIndexAllowlist(); err == nil || !strings.Contains(err.Error(), ".env") {
		t.Fatalf("expected rename source rejection, got %v", err)
	}
}

func TestIntendedChangesIncludeTrackedMetadataDeletion(t *testing.T) {
	runner, dir := prepareFlow(t)
	command(t, dir, "git", "config", "user.name", "Flow Test")
	command(t, dir, "git", "config", "user.email", "flow@example.org")
	if err := runner.Run(context.Background(), []string{"rebuild"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "rpm", "repodata"))
	if err != nil {
		t.Fatal(err)
	}
	var path string
	for _, entry := range entries {
		if oldRPMName.MatchString(entry.Name()) {
			path = "rpm/repodata/" + entry.Name()
			break
		}
	}
	if path == "" {
		t.Fatal("missing RPM metadata")
	}
	command(t, dir, "git", "add", "--", path)
	command(t, dir, "git", "commit", "-m", "test baseline")
	if err := os.Remove(filepath.Join(dir, filepath.FromSlash(path))); err != nil {
		t.Fatal(err)
	}
	paths, err := runner.allowedGitPaths()
	if err != nil {
		t.Fatal(err)
	}
	changes, _, err := runner.intendedChanges(paths)
	if err != nil {
		t.Fatal(err)
	}
	want := false
	for _, change := range changes {
		if change == "D "+path {
			want = true
		}
	}
	if !want {
		t.Fatalf("missing tracked deletion: %v", changes)
	}
}

func TestTrustedRPMPackageFlow(t *testing.T) {
	runner, dir := prepareFlow(t)
	r, m, err := runner.load()
	if err != nil {
		t.Fatal(err)
	}
	local, err := runner.local()
	if err != nil {
		t.Fatal(err)
	}
	rpmIndex := -1
	for i, p := range m.Packages {
		if p.Format == "rpm" {
			rpmIndex = i
			break
		}
	}
	if rpmIndex < 0 {
		t.Fatal("missing RPM fixture")
	}
	old := m.Packages[rpmIndex]
	source := filepath.Join(dir, "local-cache", "sha256", old.SHA256)
	signed := filepath.Join(dir, "signed.rpm")
	if err := rpm.SignCopy(source, signed, local, []string{filepath.Join(dir, "KEY.gpg")}); err != nil {
		t.Fatal(err)
	}
	data := mustRead(t, signed)
	meta, err := inspect.Bytes(data)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	p := old
	p.URL = "https://example.com/signed.rpm"
	p.Source = "owned"
	p.SHA256 = hex.EncodeToString(sum[:])
	p.Size = int64(len(data))
	p.Identity = meta.Identity
	p.Path, err = contract.ServedPath(p)
	if err != nil {
		t.Fatal(err)
	}
	m.Packages[rpmIndex] = p
	r.RPM.PackageSignature = "trusted-packages"
	r.RPM.TrustedKeyPaths = []string{"KEY.gpg"}
	if err := contract.WriteRepository(filepath.Join(dir, "ghrepo.yaml"), r); err != nil {
		t.Fatal(err)
	}
	if err := contract.WriteManifest(filepath.Join(dir, "packages.yaml"), m, r); err != nil {
		t.Fatal(err)
	}
	runner.Fetcher.(fixtureFetcher).data[p.URL] = data
	if err := runner.Run(context.Background(), []string{"rebuild"}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), []string{"check"}); err != nil {
		t.Fatal(err)
	}
}

func TestFailedPushLeavesRemoteUnchanged(t *testing.T) {
	runner, dir := prepareFlow(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	command(t, dir, "git", "init", "--bare", remote)
	command(t, dir, "git", "remote", "add", "origin", remote)
	command(t, dir, "git", "config", "user.name", "Flow Test")
	command(t, dir, "git", "config", "user.email", "flow@example.org")
	ctx := context.Background()
	if err := runner.Run(ctx, []string{"publish"}); err != nil {
		t.Fatal(err)
	}
	before := exec.Command("git", "--git-dir", remote, "rev-parse", "refs/heads/main")
	old, err := before.Output()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "hooks", "pre-receive"), []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	r, m, err := runner.load()
	if err != nil {
		t.Fatal(err)
	}
	m.Packages[0].State = "retained"
	m.Packages[0].RetainedAt = "2026-11-01T00:00:00Z"
	if err := contract.WriteManifest(filepath.Join(dir, "packages.yaml"), m, r); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx, []string{"publish"}); err == nil || !strings.Contains(err.Error(), "git push failed") {
		t.Fatalf("expected rejected push, got %v", err)
	}
	after := exec.Command("git", "--git-dir", remote, "rev-parse", "refs/heads/main")
	newHead, err := after.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(old, newHead) {
		t.Fatal("failed push changed remote")
	}
	if err := os.Remove(filepath.Join(remote, "hooks", "pre-receive")); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx, []string{"publish"}); err != nil {
		t.Fatal(err)
	}
	remoteManifest := exec.Command("git", "--git-dir", remote, "show", "main:packages.yaml")
	published, err := remoteManifest.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(published, []byte("state: retained")) {
		t.Fatal("retry did not publish local state")
	}
}

func TestChangeSiteOnExistingRepository(t *testing.T) {
	runner, dir := prepareFlow(t)
	ctx := context.Background()
	if err := runner.Run(ctx, []string{"rebuild"}); err != nil {
		t.Fatal(err)
	}
	before, _, err := runner.load()
	if err != nil {
		t.Fatal(err)
	}
	preserved := map[string][]byte{}
	for _, name := range []string{"packages.yaml", ".ghrepo.local.yaml", "KEY.gpg", "deb/InRelease", "rpm/repodata/repomd.xml", "rpm/repodata/repomd.xml.asc", "templates/setup.sh", ".github/workflows/pages.yml"} {
		preserved[name] = mustRead(t, filepath.Join(dir, name))
	}
	for _, tt := range []struct{ site, cname string }{
		{"https://repo.example.com", "repo.example.com"},
		{"https://other.example.com/packages", "other.example.com"},
		{"https://example.github.io/repo", ""},
	} {
		t.Run(tt.site, func(t *testing.T) {
			if err := runner.Run(ctx, []string{"init", dir, "--site", tt.site}); err != nil {
				t.Fatal(err)
			}
			r, m, err := runner.load()
			if err != nil {
				t.Fatal(err)
			}
			want := before
			want.Site.BaseURL, want.Site.CNAME = tt.site, tt.cname
			if !reflect.DeepEqual(r, want) {
				t.Fatalf("unexpected repository settings: %+v", r)
			}
			for name, data := range preserved {
				if !bytes.Equal(data, mustRead(t, filepath.Join(dir, name))) {
					t.Fatalf("site change modified %s", name)
				}
			}
			config := mustRead(t, filepath.Join(dir, "ghrepo.yaml"))
			if err := runner.Run(ctx, []string{"init"}); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(config, mustRead(t, filepath.Join(dir, "ghrepo.yaml"))) {
				t.Fatal("init without --site changed configuration")
			}
			if err := runner.Run(ctx, []string{"site-build"}); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(dir, "build", "site")
			for name, fragment := range map[string]string{
				"deb.list":   tt.site + "/deb/ ./",
				"rpm.repo":   "baseurl=" + tt.site + "/rpm/",
				"setup.sh":   tt.site + "/KEY.gpg",
				"README.md":  tt.site + "/" + m.Packages[0].Path,
				"index.html": `href="` + m.Packages[0].Path + `"`,
			} {
				if !strings.Contains(string(mustRead(t, filepath.Join(output, name))), fragment) {
					t.Fatalf("%s does not contain %q", name, fragment)
				}
			}
			if tt.cname != "" {
				if got := string(mustRead(t, filepath.Join(output, "CNAME"))); got != tt.cname+"\n" {
					t.Fatalf("unexpected CNAME: %q", got)
				}
			} else if _, err := os.Stat(filepath.Join(output, "CNAME")); !os.IsNotExist(err) {
				t.Fatalf("CNAME remains after returning to GitHub Pages: %v", err)
			}
		})
	}
	config := mustRead(t, filepath.Join(dir, "ghrepo.yaml"))
	for _, invalid := range []string{"", "http://repo.example.com", "https://repo.example.com/", "https://user:secret@repo.example.com"} {
		if err := runner.Run(ctx, []string{"init", "--site", invalid}); err == nil {
			t.Fatalf("accepted invalid site %q", invalid)
		}
		if !bytes.Equal(config, mustRead(t, filepath.Join(dir, "ghrepo.yaml"))) {
			t.Fatal("invalid site changed configuration")
		}
	}
	localPath := filepath.Join(dir, ".ghrepo.local.yaml")
	local := mustRead(t, localPath)
	if err := os.WriteFile(localPath, []byte("schema: 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx, []string{"init", "--site", "https://repo.example.com"}); err == nil {
		t.Fatal("init accepted invalid local settings")
	}
	if !bytes.Equal(config, mustRead(t, filepath.Join(dir, "ghrepo.yaml"))) {
		t.Fatal("failed init changed the site URL")
	}
	if err := os.WriteFile(localPath, local, 0600); err != nil {
		t.Fatal(err)
	}
}
