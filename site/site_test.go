package site

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/asciimoth/ghrepo/apt"
	"github.com/asciimoth/ghrepo/cache"
	"github.com/asciimoth/ghrepo/contract"
	"github.com/asciimoth/ghrepo/inspect"
	"github.com/asciimoth/ghrepo/rpm"
)

type fixture struct {
	root, checkout, output string
	repo                   contract.Repository
	manifest               contract.Manifest
	data                   map[string][]byte
	server                 *httptest.Server
	fetcher                cache.Fetcher
}

func makeFixture(t *testing.T) fixture {
	t.Helper()
	for _, tool := range []string{"gpg", "gpgv"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " is unavailable")
		}
	}
	root := t.TempDir()
	checkout := filepath.Join(root, "checkout")
	if err := os.Mkdir(checkout, 0755); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "gnupg")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	keyID := "Site Fixture <site@example.org>"
	cmd := exec.Command("gpg", "--batch", "--pinentry-mode", "loopback", "--passphrase", "", "--quick-generate-key", keyID, "rsa2048", "sign", "0")
	cmd.Env = append(os.Environ(), "GNUPGHOME="+home)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("test key: %v: %s", err, out)
	}
	cmd = exec.Command("gpg", "--batch", "--export", keyID)
	cmd.Env = append(os.Environ(), "GNUPGHOME="+home)
	public, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "KEY.gpg"), public, 0644); err != nil {
		t.Fatal(err)
	}
	r := contract.Repository{Schema: 1, Repository: contract.Repo{URL: "https://github.com/example/repo", Branch: "main", Layout: "flat-v1"}, APT: contract.APT{Suite: "stable", Component: "main", Architectures: []string{"amd64"}}, RPM: contract.RPM{ID: "example", Name: "Example", Architectures: []string{"x86_64"}, PackageSignature: "metadata-only"}, Downloads: contract.Download{SourceHosts: []string{"example.com"}, MaxBytes: 1 << 20}, Site: contract.Site{BaseURL: "https://example.github.io/repo", MaxBytes: 1 << 20, RetentionDays: 30}}
	m := contract.Manifest{Schema: 1, Packages: []contract.Package{}}
	data := map[string][]byte{}
	for _, item := range []struct{ file, url, state string }{
		{"hello_1.2.0-1_amd64.deb", "/hello.deb", "active"},
		{"empty-0.1-1.x86_64.rpm", "/empty.rpm", "active"},
	} {
		b, err := os.ReadFile(filepath.Join("..", "contract", "testdata", "packages", item.file))
		if err != nil {
			t.Fatal(err)
		}
		meta, err := inspect.Bytes(b)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		p := contract.Package{URL: "https://example.com" + item.url, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(b)), Source: "upstream", State: item.state, Format: meta.Format, Identity: meta.Identity}
		p.Path, err = contract.ServedPath(p)
		if err != nil {
			t.Fatal(err)
		}
		m.Packages = append(m.Packages, p)
		data[item.url] = b
	}
	if err := contract.WriteRepository(filepath.Join(checkout, "ghrepo.yaml"), r); err != nil {
		t.Fatal(err)
	}
	if err := contract.WriteManifest(filepath.Join(checkout, "packages.yaml"), m, r); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(checkout, "templates"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, item := range DefaultTemplates {
		if err := os.WriteFile(filepath.Join(checkout, "templates", item.Name), []byte(item.Text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	cacheRoot := filepath.Join(root, "signing-cache", "sha256")
	if err := os.MkdirAll(cacheRoot, 0700); err != nil {
		t.Fatal(err)
	}
	for _, p := range m.Packages {
		if err := os.WriteFile(filepath.Join(cacheRoot, p.SHA256), data[strings.TrimPrefix(p.URL, "https://example.com")], 0600); err != nil {
			t.Fatal(err)
		}
	}
	local := contract.Local{Schema: 1, GPG: contract.LocalGPG{KeyID: keyID, Home: home}}
	if err := apt.Build(filepath.Join(checkout, "deb"), filepath.Dir(cacheRoot), r, m, local); err != nil {
		t.Fatal(err)
	}
	if err := rpm.Build(filepath.Join(checkout, "rpm"), filepath.Dir(cacheRoot), r, m, local); err != nil {
		t.Fatal(err)
	}
	// The checkout contains no keyring, local config, or cached package bytes.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if b, ok := data[req.URL.Path]; ok {
			_, _ = w.Write(b)
			return
		}
		http.NotFound(w, req)
	}))
	t.Cleanup(srv.Close)
	client := srv.Client()
	transport := client.Transport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	client.Transport = transport
	return fixture{root, checkout, filepath.Join(checkout, "build", "site"), r, m, data, srv, cache.HTTPFetcher{Client: client}}
}

func TestBuildFromCleanCheckout(t *testing.T) {
	f := makeFixture(t)
	if err := os.WriteFile(filepath.Join(f.checkout, ".ghrepo.local.yaml"), []byte("private setting"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(f.checkout, "local-cache"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.checkout, "local-cache", "secret"), []byte("private cache"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Build(context.Background(), f.checkout, f.output, f.fetcher); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"KEY.gpg": true, "index.html": true, "README.md": true, "setup.sh": true, "deb.list": true, "rpm.repo": true, "deb/Packages": true, "deb/Packages.gz": true, "deb/Release": true, "deb/InRelease": true, "rpm/repodata/repomd.xml": true, "rpm/repodata/repomd.xml.asc": true}
	for _, p := range f.manifest.Packages {
		want[p.Path] = true
	}
	entries, err := os.ReadDir(filepath.Join(f.checkout, "rpm", "repodata"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if rpmIndexName.MatchString(e.Name()) {
			want["rpm/repodata/"+e.Name()] = true
		}
	}
	err = filepath.WalkDir(f.output, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, err := filepath.Rel(f.output, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !want[rel] {
			return errors.New("unexpected site file: " + rel)
		}
		delete(want, rel)
		if rel != "index.html" && rel != "README.md" && rel != "setup.sh" && rel != "deb.list" && rel != "rpm.repo" {
			origin := filepath.Join(f.checkout, rel)
			if strings.HasSuffix(rel, ".deb") || strings.HasSuffix(rel, ".rpm") {
				for _, p := range f.manifest.Packages {
					if p.Path == rel {
						origin = filepath.Join(f.root, "signing-cache", "sha256", p.SHA256)
					}
				}
			}
			b, readErr := os.ReadFile(origin)
			if readErr != nil || !bytes.Equal(b, mustRead(t, path)) {
				return errors.New("site bytes differ: " + rel)
			}
		}
		return nil
	})
	if err != nil || len(want) != 0 {
		t.Fatalf("site allowlist: %v, missing %v", err, want)
	}
	if err := apt.Verify(filepath.Join(f.output, "deb"), filepath.Join(f.output, "KEY.gpg"), f.repo, f.manifest); err != nil {
		t.Fatal(err)
	}
	if err := rpm.Verify(filepath.Join(f.output, "rpm"), filepath.Join(f.output, "KEY.gpg"), f.output, f.repo, f.manifest); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mustRead(t, filepath.Join(f.output, "rpm.repo"))), "gpgcheck=0") {
		t.Fatal("wrong RPM package signature policy")
	}
	if !strings.Contains(string(mustRead(t, filepath.Join(f.output, "deb.list"))), f.repo.Site.BaseURL+"/deb/ ./") {
		t.Fatal("wrong APT source URL")
	}
	if out, err := exec.Command("sh", "-n", filepath.Join(f.output, "setup.sh")).CombinedOutput(); err != nil {
		t.Fatalf("invalid setup script: %v: %s", err, out)
	}
}

func TestFailuresPreserveSite(t *testing.T) {
	f := makeFixture(t)
	ctx := context.Background()
	if err := Build(ctx, f.checkout, f.output, f.fetcher); err != nil {
		t.Fatal(err)
	}
	prior := snapshot(t, f.output)
	for _, bad := range []struct {
		name   string
		change func()
		undo   func()
	}{
		{"missing URL", func() { delete(f.data, "/hello.deb") }, func() {
			f.data["/hello.deb"] = mustRead(t, filepath.Join(f.root, "signing-cache", "sha256", f.manifest.Packages[0].SHA256))
		}},
		{"changed bytes", func() { f.data["/empty.rpm"] = []byte("changed") }, func() {
			f.data["/empty.rpm"] = mustRead(t, filepath.Join(f.root, "signing-cache", "sha256", f.manifest.Packages[1].SHA256))
		}},
		{"small site limit", func() {
			f.repo.Site.MaxBytes = 1
			_ = contract.WriteRepository(filepath.Join(f.checkout, "ghrepo.yaml"), f.repo)
		}, func() {
			f.repo.Site.MaxBytes = 1 << 20
			_ = contract.WriteRepository(filepath.Join(f.checkout, "ghrepo.yaml"), f.repo)
		}},
		{"tampered signed index", func() { _ = os.WriteFile(filepath.Join(f.checkout, "deb", "InRelease"), []byte("bad"), 0644) }, func() {}},
	} {
		t.Run(bad.name, func(t *testing.T) {
			bad.change()
			if err := Build(ctx, f.checkout, f.output, f.fetcher); err == nil {
				t.Fatal("build succeeded")
			}
			if !reflect.DeepEqual(prior, snapshot(t, f.output)) {
				t.Fatal("previous site changed")
			}
			bad.undo()
		})
	}
}

func TestRetainedFilesStayOutsideIndexes(t *testing.T) {
	f := makeFixture(t)
	// Rebuild signed metadata with retained DEB and RPM packages.
	for i := range f.manifest.Packages {
		f.manifest.Packages[i].State = "retained"
		f.manifest.Packages[i].RetainedAt = "2026-10-31T00:00:00Z"
	}
	if err := contract.WriteManifest(filepath.Join(f.checkout, "packages.yaml"), f.manifest, f.repo); err != nil {
		t.Fatal(err)
	}
	local := contract.Local{Schema: 1, GPG: contract.LocalGPG{KeyID: "Site Fixture <site@example.org>", Home: filepath.Join(f.root, "gnupg")}}
	if err := os.RemoveAll(filepath.Join(f.checkout, "deb")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(f.checkout, "rpm")); err != nil {
		t.Fatal(err)
	}
	cacheRoot := filepath.Join(f.root, "signing-cache")
	if err := apt.Build(filepath.Join(f.checkout, "deb"), cacheRoot, f.repo, f.manifest, local); err != nil {
		t.Fatal(err)
	}
	if err := rpm.Build(filepath.Join(f.checkout, "rpm"), cacheRoot, f.repo, f.manifest, local); err != nil {
		t.Fatal(err)
	}
	if err := Build(context.Background(), f.checkout, f.output, f.fetcher); err != nil {
		t.Fatal(err)
	}
	for _, p := range f.manifest.Packages {
		if _, err := os.Stat(filepath.Join(f.output, p.Path)); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Contains(string(mustRead(t, filepath.Join(f.output, "deb", "Packages"))), "hello") {
		t.Fatal("retained DEB appears in index")
	}
	if err := rpm.Verify(filepath.Join(f.output, "rpm"), filepath.Join(f.output, "KEY.gpg"), f.output, f.repo, f.manifest); err != nil {
		t.Fatal(err)
	}
}

func TestLinkedInputsPreserveSite(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "template directory"} {
		t.Run(kind, func(t *testing.T) {
			f := makeFixture(t)
			if err := Build(context.Background(), f.checkout, f.output, f.fetcher); err != nil {
				t.Fatal(err)
			}
			prior := snapshot(t, f.output)
			key := filepath.Join(f.checkout, "KEY.gpg")
			switch kind {
			case "symlink":
				if err := os.Remove(key); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(f.output, "KEY.gpg"), key); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(key, filepath.Join(f.checkout, "second-key.gpg")); err != nil {
					t.Fatal(err)
				}
			case "template directory":
				old := filepath.Join(f.checkout, "templates")
				moved := filepath.Join(f.checkout, "templates-real")
				if err := os.Rename(old, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(moved, old); err != nil {
					t.Fatal(err)
				}
			}
			if err := Build(context.Background(), f.checkout, f.output, f.fetcher); err == nil {
				t.Fatal("accepted linked input")
			}
			if !reflect.DeepEqual(prior, snapshot(t, f.output)) {
				t.Fatal("previous site changed")
			}
		})
	}
}

func TestRetainedRPMMetadataAndCNAME(t *testing.T) {
	f := makeFixture(t)
	f.repo.Site.CNAME = "packages.example.com"
	if err := contract.WriteRepository(filepath.Join(f.checkout, "ghrepo.yaml"), f.repo); err != nil {
		t.Fatal(err)
	}
	old := []byte("previous checksum named index")
	sum := sha256.Sum256(old)
	name := hex.EncodeToString(sum[:]) + "-primary.xml.gz"
	path := filepath.Join(f.checkout, "rpm", "repodata", name)
	if err := os.WriteFile(path, old, 0644); err != nil {
		t.Fatal(err)
	}
	if err := Build(context.Background(), f.checkout, f.output, f.fetcher); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(old, mustRead(t, filepath.Join(f.output, "rpm", "repodata", name))) {
		t.Fatal("retained RPM metadata changed")
	}
	if string(mustRead(t, filepath.Join(f.output, "CNAME"))) != "packages.example.com\n" {
		t.Fatal("wrong CNAME")
	}
	if err := os.WriteFile(path, []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := Build(context.Background(), f.checkout, f.output, f.fetcher); err == nil {
		t.Fatal("accepted RPM metadata with a false filename checksum")
	}
	if !bytes.Equal(old, mustRead(t, filepath.Join(f.output, "rpm", "repodata", name))) {
		t.Fatal("failed build replaced the prior site")
	}
}

func TestTrustedPackagePolicyInSite(t *testing.T) {
	f := makeFixture(t)
	t.Chdir(f.checkout)
	old := f.manifest.Packages[1]
	cacheRoot := filepath.Join(f.root, "signing-cache")
	signed := filepath.Join(f.root, "signed.rpm")
	local := contract.Local{Schema: 1, GPG: contract.LocalGPG{KeyID: "Site Fixture <site@example.org>", Home: filepath.Join(f.root, "gnupg")}}
	if err := rpm.SignCopy(filepath.Join(cacheRoot, "sha256", old.SHA256), signed, local, []string{filepath.Join(f.checkout, "KEY.gpg")}); err != nil {
		t.Fatal(err)
	}
	meta, size, digest, err := inspect.File(signed, f.repo.Downloads.MaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	p := old
	p.Identity, p.Size, p.SHA256 = meta.Identity, size, digest
	p.Path, err = contract.ServedPath(p)
	if err != nil {
		t.Fatal(err)
	}
	f.manifest.Packages[1] = p
	f.data["/empty.rpm"] = mustRead(t, signed)
	if err := os.WriteFile(filepath.Join(cacheRoot, "sha256", digest), f.data["/empty.rpm"], 0600); err != nil {
		t.Fatal(err)
	}
	f.repo.RPM.PackageSignature = "trusted-packages"
	f.repo.RPM.TrustedKeyPaths = []string{"KEY.gpg"}
	if err := contract.WriteRepository(filepath.Join(f.checkout, "ghrepo.yaml"), f.repo); err != nil {
		t.Fatal(err)
	}
	if err := contract.WriteManifest(filepath.Join(f.checkout, "packages.yaml"), f.manifest, f.repo); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(f.checkout, "rpm")); err != nil {
		t.Fatal(err)
	}
	if err := rpm.Build(filepath.Join(f.checkout, "rpm"), cacheRoot, f.repo, f.manifest, local); err != nil {
		t.Fatal(err)
	}
	if err := Build(context.Background(), f.checkout, f.output, f.fetcher); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mustRead(t, filepath.Join(f.output, "rpm.repo"))), "gpgcheck=1") {
		t.Fatal("trusted RPM policy did not enable package signature checks")
	}
}

func TestSuccessfulRebuildReplacesArtifact(t *testing.T) {
	f := makeFixture(t)
	ctx := context.Background()
	if err := Build(ctx, f.checkout, f.output, f.fetcher); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.output, "stale.txt"), []byte("old site file"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := Build(ctx, f.checkout, f.output, f.fetcher); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(f.output, "stale.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale file remained after replacement")
	}
	if _, err := os.Lstat(f.output + ".previous"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("site backup remained after replacement")
	}
	if err := apt.Verify(filepath.Join(f.output, "deb"), filepath.Join(f.output, "KEY.gpg"), f.repo, f.manifest); err != nil {
		t.Fatal(err)
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

func snapshot(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = mustRead(t, name)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
