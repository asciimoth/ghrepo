package apt

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/ghrepo/contract"
	"github.com/asciimoth/ghrepo/inspect"
)

func fixture(t *testing.T) (string, string, contract.Repository, contract.Manifest, contract.Local, string) {
	return fixtureWithAlgorithm(t, "rsa2048")
}

func fixtureWithAlgorithm(t *testing.T, algorithm string) (string, string, contract.Repository, contract.Manifest, contract.Local, string) {
	t.Helper()
	if _, err := exec.LookPath("gpg"); err != nil {
		if os.Getenv("GHREPO_APT_INSTALL") == "1" {
			t.Fatal("gpg is required for the container compatibility test")
		}
		t.Skip("gpg is unavailable")
	}
	if _, err := exec.LookPath("gpgv"); err != nil {
		if os.Getenv("GHREPO_APT_INSTALL") == "1" {
			t.Fatal("gpgv is required for the container compatibility test")
		}
		t.Skip("gpgv is unavailable")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "gnupg")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	key := "Fixture <fixture@example.org>"
	cmd := exec.Command("gpg", "--batch", "--pinentry-mode", "loopback", "--passphrase", "", "--quick-generate-key", key, algorithm, "sign", "0")
	cmd.Env = append(os.Environ(), "GNUPGHOME="+home)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate test key: %v: %s", err, output)
	}
	public := filepath.Join(root, "KEY.gpg")
	cmd = exec.Command("gpg", "--batch", "--export", key)
	cmd.Env = append(os.Environ(), "GNUPGHOME="+home)
	pub, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(public, pub, 0644); err != nil {
		t.Fatal(err)
	}
	r := contract.Repository{Schema: 1, Repository: contract.Repo{URL: "https://github.com/example/repo", Branch: "main", Layout: "flat-v1"}, APT: contract.APT{Suite: "stable", Component: "main", Architectures: []string{"amd64"}}, RPM: contract.RPM{ID: "example", Name: "Example", Architectures: []string{"x86_64"}, PackageSignature: "metadata-only"}, Downloads: contract.Download{SourceHosts: []string{"example.com"}, MaxBytes: 1000000}, Site: contract.Site{BaseURL: "https://example.github.io/repo", MaxBytes: 1000000, RetentionDays: 30}}
	b, err := os.ReadFile("../contract/testdata/packages/hello_1.2.0-1_amd64.deb")
	if err != nil {
		t.Fatal(err)
	}
	meta, err := inspect.Bytes(b)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	p := contract.Package{URL: "https://example.com/hello.deb", SHA256: hex.EncodeToString(h[:]), Size: int64(len(b)), Source: "upstream", State: "active", Format: "deb", Identity: meta.Identity}
	p.Path, err = contract.ServedPath(p)
	if err != nil {
		t.Fatal(err)
	}
	m := contract.Manifest{Schema: 1, Packages: []contract.Package{p}}
	cacheDir := filepath.Join(root, "cache", "sha256")
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, p.SHA256), b, 0600); err != nil {
		t.Fatal(err)
	}
	return root, public, r, m, contract.Local{Schema: 1, GPG: contract.LocalGPG{KeyID: key, Home: home}}, filepath.Join(root, "cache")
}

func TestEdDSABuildVerify(t *testing.T) {
	root, key, r, m, local, cacheRoot := fixtureWithAlgorithm(t, "ed25519")
	dir := filepath.Join(root, "deb")
	if err := Build(dir, cacheRoot, r, m, local); err != nil {
		t.Fatal(err)
	}
	if err := Verify(dir, key, r, m); err != nil {
		t.Fatal(err)
	}
}

func TestCrossVersionGPGv(t *testing.T) {
	if os.Getenv("GHREPO_APT_CROSS_GPGV") != "1" {
		t.Skip("set GHREPO_APT_CROSS_GPGV=1 to run the Docker check")
	}
	root, _, r, m, local, cacheRoot := fixtureWithAlgorithm(t, "ed25519")
	dir := filepath.Join(root, "deb")
	if err := Build(dir, cacheRoot, r, m, local); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("docker", "run", "--rm", "--mount", "type=bind,src="+root+",dst=/work", "ubuntu:24.04", "gpgv", "--keyring", "/work/KEY.gpg", "--output", "/work/verified", "/work/deb/InRelease")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("container gpgv: %v: %s", err, output)
	}
	verified, err := os.ReadFile(filepath.Join(root, "verified"))
	if err != nil {
		t.Fatal(err)
	}
	release, err := os.ReadFile(filepath.Join(dir, "Release"))
	if err != nil {
		t.Fatal(err)
	}
	if !sameSignedRelease(verified, release) {
		t.Fatalf("cross-version GPGv changed signed content: verified=%q release=%q", verified, release)
	}
}

func TestSameSignedRelease(t *testing.T) {
	release := []byte("Origin: example\nSHA256:\n hash 1 Packages\n")
	for _, cleared := range [][]byte{release, append(bytes.Clone(release), '\n')} {
		if !sameSignedRelease(cleared, release) {
			t.Fatalf("rejected signed content %q", cleared)
		}
	}
	for _, cleared := range [][]byte{append(bytes.Clone(release), '\n', '\n'), []byte("Origin: other\nSHA256:\n hash 1 Packages\n\n")} {
		if sameSignedRelease(cleared, release) {
			t.Fatalf("accepted changed signed content %q", cleared)
		}
	}
}

func TestBuildVerifyAndDeterminism(t *testing.T) {
	root, key, r, m, local, cacheRoot := fixture(t)
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	for _, dir := range []string{a, b} {
		if err := buildAt(dir, cacheRoot, r, m, local, time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
		if err := Verify(dir, key, r, m); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"Packages", "Packages.gz", "Release"} {
		x, _ := os.ReadFile(filepath.Join(a, name))
		y, _ := os.ReadFile(filepath.Join(b, name))
		if !bytes.Equal(x, y) {
			t.Fatalf("%s is not deterministic", name)
		}
	}
	if _, err := os.Stat(filepath.Join(a, "Release.gpg")); !os.IsNotExist(err) {
		t.Fatal("unexpected Release.gpg")
	}
}

func TestVerifyRejectsBrokenLinks(t *testing.T) {
	root, key, r, m, local, cacheRoot := fixture(t)
	dir := filepath.Join(root, "deb")
	if err := Build(dir, cacheRoot, r, m, local); err != nil {
		t.Fatal(err)
	}
	checkBad := func(name string, change func()) {
		t.Helper()
		change()
		if err := Verify(dir, key, r, m); err == nil {
			t.Fatalf("accepted %s", name)
		}
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		if err := Build(dir, cacheRoot, r, m, local); err != nil {
			t.Fatal(err)
		}
	}
	checkBad("InRelease", func() { mutate(t, filepath.Join(dir, "InRelease")) })
	checkBad("Release", func() { mutate(t, filepath.Join(dir, "Release")) })
	checkBad("Packages.gz", func() { mutate(t, filepath.Join(dir, "Packages.gz")) })
	checkBad("Packages", func() { mutate(t, filepath.Join(dir, "Packages")) })
	otherRoot, otherKey, _, _, _, _ := fixture(t)
	_ = otherRoot
	if err := Verify(dir, otherKey, r, m); err == nil {
		t.Fatal("accepted wrong key")
	}
	omitted := m
	omitted.Packages = []contract.Package{}
	if err := Verify(dir, key, r, omitted); err == nil {
		t.Fatal("accepted omitted package")
	}
	retained := m
	retained.Packages = append([]contract.Package(nil), m.Packages...)
	retained.Packages[0].State = "retained"
	retained.Packages[0].RetainedAt = "2026-10-31T00:00:00Z"
	if err := Verify(dir, key, r, retained); err == nil {
		t.Fatal("accepted retained package in index")
	}
	// An index with valid signatures and hashes must still match the manifest.
	wrong := m
	wrong.Packages = append([]contract.Package(nil), m.Packages...)
	wrong.Packages[0].Size++
	if err := Verify(dir, key, r, wrong); err == nil {
		t.Fatal("accepted wrong indexed size")
	}
}

func TestBuildRejectsChangedPackage(t *testing.T) {
	root, _, r, m, local, cacheRoot := fixture(t)
	name := filepath.Join(cacheRoot, "sha256", m.Packages[0].SHA256)
	mutate(t, name)
	out := filepath.Join(root, "deb")
	if err := Build(out, cacheRoot, r, m, local); err == nil {
		t.Fatal("accepted changed package bytes")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("failed build left partial output")
	}
}

func TestBuildExcludesRetainedPackages(t *testing.T) {
	root, key, r, m, local, cacheRoot := fixture(t)
	m.Packages[0].State = "retained"
	m.Packages[0].RetainedAt = "2026-10-31T00:00:00Z"
	dir := filepath.Join(root, "deb")
	if err := Build(dir, cacheRoot, r, m, local); err != nil {
		t.Fatal(err)
	}
	if err := Verify(dir, key, r, m); err != nil {
		t.Fatal(err)
	}
	plain, err := os.ReadFile(filepath.Join(dir, "Packages"))
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) != 0 {
		t.Fatal("retained package appears in new index")
	}
}

func TestSigningFailureKeepsPriorMetadata(t *testing.T) {
	root, key, r, m, local, cacheRoot := fixture(t)
	prior := filepath.Join(root, "prior")
	if err := Build(prior, cacheRoot, r, m, local); err != nil {
		t.Fatal(err)
	}
	local.GPG.KeyID = "MISSING-KEY"
	next := filepath.Join(root, "next")
	if err := Build(next, cacheRoot, r, m, local); err == nil {
		t.Fatal("accepted missing signing key")
	}
	if _, err := os.Stat(next); !os.IsNotExist(err) {
		t.Fatal("failed signing left partial metadata")
	}
	if err := Verify(prior, key, r, m); err != nil {
		t.Fatalf("prior metadata changed: %v", err)
	}
}

func mutate(t *testing.T, path string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		t.Fatal("empty fixture")
	}
	b[len(b)/2] ^= 1
	if err := os.WriteFile(path, b, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestIndexReaderRejectsMissingAndExtraEntries(t *testing.T) {
	_, _, _, m, _, _ := fixture(t)
	if err := readPackages(nil, m.Packages); err == nil || !strings.Contains(err.Error(), "termination") {
		t.Fatal(err)
	}
	if err := readPackages([]byte(""), nil); err != nil {
		t.Fatal(err)
	}
}

func TestIndexReaderChecksPackageLinks(t *testing.T) {
	root, _, r, m, local, cacheRoot := fixture(t)
	dir := filepath.Join(root, "deb")
	if err := Build(dir, cacheRoot, r, m, local); err != nil {
		t.Fatal(err)
	}
	compressed, err := os.ReadFile(filepath.Join(dir, "Packages.gz"))
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	var plain bytes.Buffer
	if _, err := plain.ReadFrom(zr); err != nil {
		t.Fatal(err)
	}
	if err := zr.Close(); err != nil {
		t.Fatal(err)
	}
	good := plain.String()
	if err := readPackages([]byte(good), m.Packages); err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct{ name, old, replacement string }{
		{"path", strings.TrimPrefix(m.Packages[0].Path, "deb/"), "pool/other.deb"},
		{"size", fmt.Sprintf("Size: %d", m.Packages[0].Size), "Size: 1"},
		{"hash", m.Packages[0].SHA256, strings.Repeat("0", 64)},
	} {
		altered := strings.Replace(good, change.old, change.replacement, 1)
		if err := readPackages([]byte(altered), m.Packages); err == nil {
			t.Fatalf("accepted changed %s", change.name)
		}
	}
}

func TestControlFieldsKeepDependenciesAndRejectIndexOverrides(t *testing.T) {
	fields := map[string]string{"Package": "hello", "Version": "1", "Architecture": "amd64", "Description": "hello", "Pre-Depends": "base-files", "X-Extra": "value"}
	var first, second bytes.Buffer
	if err := writeControl(&first, fields); err != nil {
		t.Fatal(err)
	}
	if err := writeControl(&second, fields); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) || !strings.Contains(first.String(), "Pre-Depends: base-files\n") || !strings.Contains(first.String(), "X-Extra: value\n") {
		t.Fatal("control fields were lost or reordered")
	}
	fields["Filename"] = "wrong.deb"
	if err := writeControl(&first, fields); err == nil {
		t.Fatal("accepted package supplied Filename")
	}
}

func TestAPTClientCompatibility(t *testing.T) {
	algorithms := []string{"rsa2048", "ed25519"}
	if selected := os.Getenv("GHREPO_TEST_KEY_ALGORITHM"); selected != "" {
		algorithms = []string{selected}
	}
	for _, algorithm := range algorithms {
		t.Run(algorithm, func(t *testing.T) { testAPTClientCompatibility(t, algorithm) })
	}
}

func testAPTClientCompatibility(t *testing.T, algorithm string) {
	if _, err := exec.LookPath("apt-get"); err != nil {
		if os.Getenv("GHREPO_APT_INSTALL") == "1" {
			t.Fatal("apt-get is required for the container compatibility test")
		}
		t.Skip("apt-get is unavailable")
	}
	root, key, r, m, local, cacheRoot := fixtureWithAlgorithm(t, algorithm)
	debDir := filepath.Join(root, "deb")
	if err := Build(debDir, cacheRoot, r, m, local); err != nil {
		t.Fatal(err)
	}
	pkgPath := filepath.Join(root, filepath.FromSlash(m.Packages[0].Path))
	if err := os.MkdirAll(filepath.Dir(pkgPath), 0755); err != nil {
		t.Fatal(err)
	}
	pkg, err := os.ReadFile(filepath.Join(cacheRoot, "sha256", m.Packages[0].SHA256))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pkgPath, pkg, 0644); err != nil {
		t.Fatal(err)
	}
	field := exec.Command("dpkg-deb", "--field", pkgPath, "Package")
	if output, err := field.CombinedOutput(); err != nil || strings.TrimSpace(string(output)) != m.Packages[0].Identity.Name {
		t.Fatalf("dpkg-deb could not read package: %v: %s", err, output)
	}
	server := httptest.NewServer(http.FileServer(http.Dir(root)))
	defer server.Close()
	state := filepath.Join(root, "apt-state")
	archives := filepath.Join(root, "archives")
	for _, dir := range []string{filepath.Join(state, "partial"), filepath.Join(archives, "partial")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	sources := filepath.Join(root, "sources.list")
	if err := os.WriteFile(sources, []byte(fmt.Sprintf("deb [signed-by=%s] %s/deb/ ./\n", key, server.URL)), 0644); err != nil {
		t.Fatal(err)
	}
	opts := []string{"-o", "APT::Architecture=amd64", "-o", "Dir::Etc::sourcelist=" + sources, "-o", "Dir::Etc::sourceparts=-", "-o", "Dir::State::lists=" + state, "-o", "Dir::Cache::archives=" + archives, "-o", "APT::Get::List-Cleanup=0"}
	for _, action := range [][]string{{"update"}, {"-s", "install", m.Packages[0].Identity.Name}} {
		args := append(append([]string{}, opts...), action...)
		cmd := exec.Command("apt-get", args...)
		if output, err := cmd.CombinedOutput(); err != nil {
			if strings.Contains(string(output), "Unable to determine a suitable packaging system type") {
				if os.Getenv("GHREPO_APT_INSTALL") == "1" {
					t.Fatalf("APT has no usable packaging backend: %s", output)
				}
				t.Skip("APT has no usable packaging backend")
			}
			t.Fatalf("apt-get %v: %v: %s", action, err, output)
		} else {
			t.Logf("apt-get %v: %s", action, output)
		}
	}
	download := exec.Command("apt-get", append(append([]string{}, opts...), "download", m.Packages[0].Identity.Name)...)
	download.Dir = root
	if output, err := download.CombinedOutput(); err != nil {
		t.Fatalf("apt-get download: %v: %s", err, output)
	}
	downloaded, err := filepath.Glob(filepath.Join(root, "hello_*.deb"))
	if err != nil || len(downloaded) != 1 {
		t.Fatalf("APT download missing: %v, %v", downloaded, err)
	}
	downloadedBytes, err := os.ReadFile(downloaded[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(downloadedBytes, pkg) {
		t.Fatal("APT downloaded bytes differ from the manifest package")
	}
	if os.Getenv("GHREPO_APT_INSTALL") == "1" {
		if os.Geteuid() != 0 {
			t.Fatal("GHREPO_APT_INSTALL requires an isolated root test environment")
		}
		install := exec.Command("apt-get", append(append([]string{}, opts...), "-y", "install", m.Packages[0].Identity.Name)...)
		if output, err := install.CombinedOutput(); err != nil {
			t.Fatalf("apt-get install: %v: %s", err, output)
		}
		query := exec.Command("dpkg-query", "-W", "-f=${Status} ${Version}", m.Packages[0].Identity.Name)
		output, err := query.Output()
		if err != nil || string(output) != "install ok installed "+m.Packages[0].Identity.Version {
			t.Fatalf("APT did not install the fixture: %v: %s", err, output)
		}
	}
	// APT's package URL must match the path in the served manifest.
	resp, err := http.Get(server.URL + "/deb/" + strings.TrimPrefix(m.Packages[0].Path, "deb/"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("package HTTP status: %d", resp.StatusCode)
	}
}
