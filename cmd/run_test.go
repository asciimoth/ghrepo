package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/ghrepo/contract"
)

type fixtureFetcher struct{ data map[string][]byte }

func TestRepositoryDirectorySelection(t *testing.T) {
	ctx := context.Background()
	envDir := t.TempDir()
	flagDir := t.TempDir()
	t.Setenv("GHREPO_REPO_DIR", envDir)
	runner := Runner{}
	if err := runner.Run(ctx, []string{"init", "--repo", "example/env"}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx, []string{"--repo-dir", flagDir, "init", "--repo", "example/flag"}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx, []string{"sync"}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx, []string{"sync", "--repo-dir=" + flagDir}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ dir, want string }{{envDir, "https://github.com/example/env"}, {flagDir, "https://github.com/example/flag"}} {
		repo, err := contract.ReadRepository(filepath.Join(tc.dir, "ghrepo.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if repo.Repository.URL != tc.want {
			t.Fatalf("repository in %s is %s; want %s", tc.dir, repo.Repository.URL, tc.want)
		}
	}
	if err := runner.Run(ctx, []string{"--repo-dir"}); err == nil {
		t.Fatal("accepted missing repository directory")
	}
}

func TestInitRepoAndSiteURLs(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		repo  string
		site  string
		cname string
	}{
		{"short repo", []string{"--repo", "example/packages"}, "https://github.com/example/packages", "https://example.github.io/packages", ""},
		{"full repo", []string{"--repo", "https://github.com/example/packages"}, "https://github.com/example/packages", "https://example.github.io/packages", ""},
		{"custom site", []string{"--repo", "example/packages", "--site", "https://packages.example.com"}, "https://github.com/example/packages", "https://packages.example.com", "packages.example.com"},
		{"user site", []string{"--repo", "Example/Example.github.io"}, "https://github.com/Example/Example.github.io", "https://example.github.io", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			runner := Runner{Dir: dir}
			if err := runner.Run(context.Background(), append([]string{"init"}, tt.args...)); err != nil {
				t.Fatal(err)
			}
			r, err := contract.ReadRepository(filepath.Join(dir, "ghrepo.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if r.Repository.URL != tt.repo || r.Site.BaseURL != tt.site || r.Site.CNAME != tt.cname {
				t.Fatalf("got repo %q, site %q, and CNAME %q; want %q, %q, and %q", r.Repository.URL, r.Site.BaseURL, r.Site.CNAME, tt.repo, tt.site, tt.cname)
			}
			owner := strings.Split(strings.TrimPrefix(tt.repo, "https://github.com/"), "/")[0]
			page := string(mustRead(t, filepath.Join(dir, "templates", "index.html")))
			if !strings.Contains(page, ">"+owner+"</a>") || strings.Contains(page, "__GHREPO_OWNER__") {
				t.Fatal("default page does not show the repository owner")
			}
		})
	}
}

func TestInitBranch(t *testing.T) {
	for _, tt := range []struct {
		name    string
		initGit bool
		branch  string
		want    string
	}{
		{name: "current branch", initGit: true, want: "release"},
		{name: "explicit branch", initGit: true, branch: "publish", want: "publish"},
		{name: "new repository", want: "main"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.initGit {
				command(t, dir, "git", "init", "-b", "release")
			}
			args := []string{"init", "--repo", "example/packages"}
			if tt.branch != "" {
				args = append(args, "--branch", tt.branch)
			}
			if err := (Runner{Dir: dir}).Run(context.Background(), args); err != nil {
				t.Fatal(err)
			}
			r, err := contract.ReadRepository(filepath.Join(dir, "ghrepo.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if r.Repository.Branch != tt.want {
				t.Fatalf("branch = %q; want %q", r.Repository.Branch, tt.want)
			}
		})
	}
}

func TestInitRejectsInvalidShortRepo(t *testing.T) {
	runner := Runner{Dir: t.TempDir()}
	if err := runner.Run(context.Background(), []string{"init", "--repo", "example/../packages"}); err == nil {
		t.Fatal("accepted invalid short repository")
	}
}

func TestMutatingCommandsAcceptDryRunWithoutRepositoryWrites(t *testing.T) {
	ctx := context.Background()
	data, err := os.ReadFile("../contract/testdata/packages/hello_1.2.0-1_amd64.deb")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	url := "https://example.com/hello.deb"
	runner := Runner{Dir: dir, Fetcher: fixtureFetcher{map[string][]byte{url: data, "https://example.com/moved.deb": data}}}
	if err := runner.Run(ctx, []string{"init", "--dry-run", "--repo", "example/repo"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ghrepo.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry init wrote repository: %v", err)
	}
	if err := runner.Run(ctx, []string{"init", "--repo", "example/repo", "--source-hosts", "example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx, []string{"add", url}); err != nil {
		t.Fatal(err)
	}
	r, m, err := runner.load()
	if err != nil {
		t.Fatal(err)
	}
	selector := m.Packages[0].Path
	manifest := mustRead(t, filepath.Join(dir, "packages.yaml"))
	commands := [][]string{
		{"add", "--dry-run", url},
		{"source", "set", "--dry-run", selector, "https://example.com/moved.deb"},
		{"remove", selector, "--dry-run"},
		{"prune", "--apply", "--dry-run"},
		{"sync", "--dry-run"},
	}
	for _, args := range commands {
		if err := runner.Run(ctx, args); err != nil {
			t.Errorf("%v: %v", args, err)
		}
		if !bytes.Equal(manifest, mustRead(t, filepath.Join(dir, "packages.yaml"))) {
			t.Fatalf("%v changed manifest", args)
		}
	}
	if err := runner.Run(ctx, []string{"replace", selector, url, "--dry-run"}); err == nil {
		t.Fatal("dry replace accepted identical bytes")
	}
	if _, err := os.Stat(filepath.Join(dir, "build")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry build wrote files: %v", err)
	}
	if _, err := contract.ReadManifest(filepath.Join(dir, "packages.yaml"), r); err != nil {
		t.Fatal(err)
	}
}

func (f fixtureFetcher) Fetch(_ context.Context, raw string, _, _ []string) (io.ReadCloser, error) {
	b, ok := f.data[raw]
	if !ok {
		return nil, errors.New("missing fixture")
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}
func TestLocalCommands(t *testing.T) {
	data, err := os.ReadFile("../contract/testdata/packages/hello_1.2.0-1_amd64.deb")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	original := "https://example.com/a.deb"
	moved := "https://example.com/b.deb"
	runner := Runner{Dir: dir, Fetcher: fixtureFetcher{map[string][]byte{original: data, moved: data}}, Now: func() time.Time { return time.Date(2026, 10, 31, 1, 2, 3, 0, time.UTC) }}
	ctx := context.Background()
	if err := runner.Run(ctx, []string{"init", "--repo", "https://github.com/example/repo", "--site", "https://example.github.io/repo", "--source-hosts", "example.com", "--redirect-hosts", ""}); err != nil {
		t.Fatal(err)
	}
	ignore := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(ignore, append([]byte("custom\n"), mustRead(t, ignore)...), 0644); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx, []string{"init"}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(mustRead(t, ignore)), "custom\n") {
		t.Fatal("init removed existing ignore rules")
	}
	workflow := string(mustRead(t, filepath.Join(dir, ".github", "workflows", "pages.yml")))
	if !strings.Contains(workflow, "path: build/site") {
		t.Fatal("workflow does not limit the Pages artifact to build/site")
	}
	if err := runner.Run(ctx, []string{"add", original}); err != nil {
		t.Fatal(err)
	}
	rp := filepath.Join(dir, "ghrepo.yaml")
	mp := filepath.Join(dir, "packages.yaml")
	r, err := contract.ReadRepository(rp)
	if err != nil {
		t.Fatal(err)
	}
	m, err := contract.ReadManifest(mp, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Packages) != 1 {
		t.Fatal(m)
	}
	p := m.Packages[0]
	if p.Identity.Name != "hello" || p.Identity.Version != "1.2.0-1" || p.Identity.Architecture != "amd64" || !strings.HasPrefix(p.Path, "deb/pool/hello_1.2.0-1_amd64_") {
		t.Fatal(p)
	}
	before := mustRead(t, mp)
	if err := runner.Run(ctx, []string{"add", original}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, mustRead(t, mp)) {
		t.Fatal("repeat add changed manifest")
	}
	if err := runner.Run(ctx, []string{"source", "set", p.Path, moved}); err != nil {
		t.Fatal(err)
	}
	m, err = contract.ReadManifest(mp, r)
	if err != nil {
		t.Fatal(err)
	}
	if m.Packages[0].Path != p.Path || m.Packages[0].SHA256 != p.SHA256 || m.Packages[0].URL != moved {
		t.Fatal(m)
	}
	if err := runner.Run(ctx, []string{"sync"}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx, []string{"remove", "deb/hello/amd64/1.2.0-1"}); err != nil {
		t.Fatal(err)
	}
	m, err = contract.ReadManifest(mp, r)
	if err != nil {
		t.Fatal(err)
	}
	if m.Packages[0].State != "retained" || m.Packages[0].RetainedAt != "2026-10-31T01:02:03Z" {
		t.Fatal(m)
	}
}
func TestSourceMoveRejectsChangedBytes(t *testing.T) {
	data := mustRead(t, "../contract/testdata/packages/hello_1.2.0-1_amd64.deb")
	changed := append([]byte(nil), data...)
	changed[len(changed)-1] ^= 1
	dir := t.TempDir()
	a := "https://example.com/a"
	b := "https://example.com/b"
	runner := Runner{Dir: dir, Fetcher: fixtureFetcher{map[string][]byte{a: data, b: changed}}}
	ctx := context.Background()
	if err := runner.Run(ctx, []string{"init", "--repo", "https://github.com/example/repo", "--site", "https://example.github.io/repo", "--source-hosts", "example.com", "--redirect-hosts", ""}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx, []string{"add", a}); err != nil {
		t.Fatal(err)
	}
	mp := filepath.Join(dir, "packages.yaml")
	before := mustRead(t, mp)
	if err := runner.Run(ctx, []string{"source", "set", "deb/hello/amd64/1.2.0-1", b}); err == nil {
		t.Fatal("accepted changed source")
	}
	if !bytes.Equal(before, mustRead(t, mp)) {
		t.Fatal("failed move changed manifest")
	}
}

func TestAddBatchFailurePreservesManifestAndCleansTemporaryFiles(t *testing.T) {
	data := mustRead(t, "../contract/testdata/packages/hello_1.2.0-1_amd64.deb")
	dir := t.TempDir()
	a := "https://example.com/a"
	b := "https://example.com/missing"
	runner := Runner{Dir: dir, Fetcher: fixtureFetcher{map[string][]byte{a: data}}}
	ctx := context.Background()
	if err := runner.Run(ctx, []string{"init", "--repo", "https://github.com/example/repo", "--site", "https://example.github.io/repo", "--source-hosts", "example.com", "--redirect-hosts", ""}); err != nil {
		t.Fatal(err)
	}
	mp := filepath.Join(dir, "packages.yaml")
	before := mustRead(t, mp)
	if err := runner.Run(ctx, []string{"add", a, b}); err == nil {
		t.Fatal("accepted incomplete batch")
	}
	if !bytes.Equal(before, mustRead(t, mp)) {
		t.Fatal("failed batch changed manifest")
	}
	temps, err := filepath.Glob(filepath.Join(dir, "local-cache", "sha256", ".add-*"))
	if err != nil || len(temps) != 0 {
		t.Fatalf("temporary files remain: %v, %v", temps, err)
	}
}

func TestAddRejectsDuplicateIdentityWithDifferentBytes(t *testing.T) {
	a := "https://example.com/a"
	b := "https://example.com/b"
	runner := Runner{Dir: t.TempDir(), Fetcher: fixtureFetcher{map[string][]byte{
		a: mustRead(t, "../contract/testdata/packages/hello_1.2.0-1_amd64.deb"),
		b: mustRead(t, "../contract/testdata/packages/hello_changed.deb"),
	}}}
	ctx := context.Background()
	if err := runner.Run(ctx, []string{"init", "--repo", "https://github.com/example/repo", "--site", "https://example.github.io/repo", "--source-hosts", "example.com", "--redirect-hosts", ""}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx, []string{"add", a}); err != nil {
		t.Fatal(err)
	}
	mp := filepath.Join(runner.Dir, "packages.yaml")
	before := mustRead(t, mp)
	if err := runner.Run(ctx, []string{"add", b}); err == nil || !strings.Contains(err.Error(), "different bytes") {
		t.Fatalf("expected identity conflict, got %v", err)
	}
	if !bytes.Equal(before, mustRead(t, mp)) {
		t.Fatal("identity conflict changed manifest")
	}
}

func TestInitExportsConfiguredPublicKeyWithoutReplacingIt(t *testing.T) {
	tools := t.TempDir()
	program := filepath.Join(tools, "gpg")
	if err := os.WriteFile(program, []byte("#!/bin/sh\nprintf public-key-bytes\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	dir := t.TempDir()
	var output bytes.Buffer
	runner := Runner{Dir: dir, Out: &output}
	ctx := context.Background()
	if err := runner.Run(ctx, []string{"init", "--repo", "https://github.com/example/repo", "--site", "https://example.github.io/repo", "--key-id", "TESTKEY"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "GPG key: TESTKEY") {
		t.Fatalf("explicit key output: %q", output.String())
	}
	key := filepath.Join(dir, "KEY.gpg")
	if string(mustRead(t, key)) != "public-key-bytes" {
		t.Fatal("public key was not exported")
	}
	if err := os.WriteFile(key, []byte("existing-key"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx, []string{"init"}); err != nil {
		t.Fatal(err)
	}
	if string(mustRead(t, key)) != "existing-key" {
		t.Fatal("repeat init replaced public key")
	}
}

func TestInitSelectsFirstLocalSigningKey(t *testing.T) {
	tools := t.TempDir()
	program := filepath.Join(tools, "gpg")
	script := `#!/bin/sh
case "$*" in
  *--list-secret-keys*)
    printf 'sec:u:2048:1:ENCRYPT:0:0:::::e:\nfpr:::::::::ENCRYPTFINGERPRINT:\nsec:u:2048:1:SIGN:0:0:::::sc:\nfpr:::::::::SIGNFINGERPRINT:\n'
    ;;
  *--list-keys*)
    printf 'pub:u:2048:1:SIGN:0:0:::::sc:\nfpr:::::::::SIGNFINGERPRINT:\nuid:u::::::::Alice Example <alice@example.org>:\n'
    ;;
  *--export*) printf 'public-key-bytes' ;;
esac
`
	if err := os.WriteFile(program, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	clearTestEnv(t, "GHREPO_GPG_KEY_ID")
	dir := t.TempDir()
	var output bytes.Buffer
	runner := Runner{Dir: dir, Out: &output}
	if err := runner.Run(context.Background(), []string{"init", "--dry-run", "--repo", "example/repo"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "GPG key: SIGNFINGERPRINT (Alice Example <alice@example.org>)") {
		t.Fatalf("dry-run key output: %q", output.String())
	}
	if _, err := os.Stat(filepath.Join(dir, ".ghrepo.local.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry run wrote local configuration: %v", err)
	}
	output.Reset()
	if err := runner.Run(context.Background(), []string{"init", "--repo", "example/repo"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "GPG key: SIGNFINGERPRINT (Alice Example <alice@example.org>)") {
		t.Fatalf("key output: %q", output.String())
	}
	local, err := contract.ReadLocal(filepath.Join(dir, ".ghrepo.local.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if local.GPG.KeyID != "SIGNFINGERPRINT" {
		t.Fatalf("selected key: %q", local.GPG.KeyID)
	}
	if got := string(mustRead(t, filepath.Join(dir, "KEY.gpg"))); got != "public-key-bytes" {
		t.Fatalf("exported key: %q", got)
	}
}

func TestInitDryRunShowsExplicitKeyWithoutWriting(t *testing.T) {
	tools := t.TempDir()
	program := filepath.Join(tools, "gpg")
	script := `#!/bin/sh
case "$*" in
  *--list-keys*) printf 'pub:u:2048:1:EXPLICIT:0:0:::::sc:\nfpr:::::::::EXPLICITFINGERPRINT:\nuid:u::::::::Bob Example <bob@example.org>:\n' ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(program, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	clearTestEnv(t, "GHREPO_GPG_KEY_ID")
	dir := t.TempDir()
	var output bytes.Buffer
	runner := Runner{Dir: dir, Out: &output}
	if err := runner.Run(context.Background(), []string{"init", "--dry-run", "--repo", "example/repo", "--key-id", "EXPLICIT"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "GPG key: EXPLICITFINGERPRINT (Bob Example <bob@example.org>)") {
		t.Fatalf("dry-run key output: %q", output.String())
	}
	if _, err := os.Stat(filepath.Join(dir, ".ghrepo.local.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry run wrote local configuration: %v", err)
	}
}

func TestInitExportsEffectivePublicKey(t *testing.T) {
	tools := t.TempDir()
	program := filepath.Join(tools, "gpg")
	if err := os.WriteFile(program, []byte("#!/bin/sh\nprintf '%s|%s' \"$3\" \"$GNUPGHOME\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	dir := t.TempDir()
	var output bytes.Buffer
	runner := Runner{Dir: dir, Out: &output}
	ctx := context.Background()
	init := []string{"init", "--repo", "https://github.com/example/repo", "--site", "https://example.github.io/repo"}
	if err := runner.Run(ctx, init); err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(dir, ".env")
	if err := os.WriteFile(envFile, []byte("GHREPO_GPG_KEY_ID=ENVKEY\nGHREPO_GPG_HOME=/env-home\n"), 0600); err != nil {
		t.Fatal(err)
	}
	local := contract.Local{Schema: 1, GPG: contract.LocalGPG{KeyID: "LOCALKEY", Home: "/local-home"}, GitHub: contract.LocalGitHub{EnvFile: envFile}}
	if err := contract.WriteLocal(filepath.Join(dir, ".ghrepo.local.yaml"), local); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(dir, "KEY.gpg")
	if err := runner.Run(ctx, []string{"init"}); err != nil {
		t.Fatal(err)
	}
	if got := string(mustRead(t, key)); got != "ENVKEY|/env-home" {
		t.Fatalf(".env key selection: %q", got)
	}
	if !strings.Contains(output.String(), "GPG key: ENVKEY") {
		t.Fatalf(".env key output: %q", output.String())
	}
	if err := os.Remove(key); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GHREPO_GPG_KEY_ID", "PROCESSKEY")
	t.Setenv("GHREPO_GPG_HOME", "/process-home")
	output.Reset()
	if err := runner.Run(ctx, []string{"init"}); err != nil {
		t.Fatal(err)
	}
	if got := string(mustRead(t, key)); got != "PROCESSKEY|/process-home" {
		t.Fatalf("process key selection: %q", got)
	}
	if !strings.Contains(output.String(), "GPG key: PROCESSKEY") {
		t.Fatalf("process key output: %q", output.String())
	}
}

func mustRead(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func clearTestEnv(t *testing.T, name string) {
	t.Helper()
	value, exists := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if exists {
			_ = os.Setenv(name, value)
		} else {
			_ = os.Unsetenv(name)
		}
	})
}
