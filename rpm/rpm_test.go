package rpm

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asciimoth/ghrepo/contract"
	"github.com/asciimoth/ghrepo/inspect"
)

func fixture(t *testing.T) (string, string, string, contract.Repository, contract.Manifest, contract.Local) {
	return fixtureWithAlgorithm(t, "rsa2048")
}

func fixtureWithAlgorithm(t *testing.T, algorithm string) (string, string, string, contract.Repository, contract.Manifest, contract.Local) {
	t.Helper()
	for _, tool := range []string{"gpg", "gpgv"} {
		if _, err := exec.LookPath(tool); err != nil {
			if os.Getenv("GHREPO_RPM_INSTALL") == "1" {
				t.Fatal(tool + " is required for the container compatibility test")
			}
			t.Skip(tool + " unavailable")
		}
	}
	root := t.TempDir()
	home := filepath.Join(root, "gnupg")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	key := "RPM Fixture <rpm@example.org>"
	cmd := exec.Command("gpg", "--batch", "--pinentry-mode", "loopback", "--passphrase", "", "--quick-generate-key", key, algorithm, "sign", "0")
	cmd.Env = append(os.Environ(), "GNUPGHOME="+home)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("key: %v: %s", err, b)
	}
	pub := filepath.Join(root, "KEY.gpg")
	cmd = exec.Command("gpg", "--batch", "--export", key)
	cmd.Env = append(os.Environ(), "GNUPGHOME="+home)
	b, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pub, b, 0644); err != nil {
		t.Fatal(err)
	}
	r := contract.Repository{Schema: 1, Repository: contract.Repo{URL: "https://github.com/example/repo", Branch: "main", Layout: "flat-v1"}, APT: contract.APT{Suite: "stable", Component: "main", Architectures: []string{"amd64"}}, RPM: contract.RPM{ID: "example", Name: "Example", Architectures: []string{"x86_64"}, PackageSignature: "metadata-only"}, Downloads: contract.Download{SourceHosts: []string{"example.com"}, MaxBytes: 1000000}, Site: contract.Site{BaseURL: "https://example.github.io/repo", MaxBytes: 1000000, RetentionDays: 30}}
	raw, err := os.ReadFile("../contract/testdata/packages/empty-0.1-1.x86_64.rpm")
	if err != nil {
		t.Fatal(err)
	}
	meta, err := inspect.Bytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	p := contract.Package{URL: "https://example.com/empty.rpm", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(raw)), Source: "upstream", State: "active", Format: "rpm", Identity: meta.Identity}
	p.Path, err = contract.ServedPath(p)
	if err != nil {
		t.Fatal(err)
	}
	m := contract.Manifest{Schema: 1, Packages: []contract.Package{p}}
	cacheRoot := filepath.Join(root, "cache")
	if err := os.MkdirAll(filepath.Join(cacheRoot, "sha256"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheRoot, "sha256", p.SHA256), raw, 0600); err != nil {
		t.Fatal(err)
	}
	site := filepath.Join(root, "site")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(site, p.Path)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(site, p.Path), raw, 0644); err != nil {
		t.Fatal(err)
	}
	return root, pub, cacheRoot, r, m, contract.Local{Schema: 1, GPG: contract.LocalGPG{KeyID: key, Home: home}}
}

func TestEdDSABuildVerify(t *testing.T) {
	root, pub, cacheRoot, r, m, local := fixtureWithAlgorithm(t, "ed25519")
	dir := filepath.Join(root, "rpm")
	if err := Build(dir, cacheRoot, r, m, local); err != nil {
		t.Fatal(err)
	}
	if err := Verify(dir, pub, filepath.Join(root, "site"), r, m); err != nil {
		t.Fatal(err)
	}
}

func TestEdDSASignCopy(t *testing.T) {
	root, pub, cacheRoot, _, m, local := fixtureWithAlgorithm(t, "ed25519")
	source := filepath.Join(cacheRoot, "sha256", m.Packages[0].SHA256)
	signed := filepath.Join(root, "signed-ed25519.rpm")
	if err := SignCopy(source, signed, local, []string{pub}); err != nil {
		t.Fatal(err)
	}
	meta, _, _, err := inspect.File(signed, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.SignatureTags) != 2 || meta.SignatureTags[0] != "DSA" || meta.SignatureTags[1] != "GPG" {
		t.Fatalf("wrong EdDSA signature tags: %v", meta.SignatureTags)
	}
	if err := VerifyPackageSignature(signed, []string{pub}, "trusted-packages"); err != nil {
		t.Fatal(err)
	}
}
func TestBuildVerify(t *testing.T) {
	root, pub, cacheRoot, r, m, local := fixture(t)
	dir := filepath.Join(root, "rpm")
	if err := Build(dir, cacheRoot, r, m, local); err != nil {
		t.Fatal(err)
	}
	if err := Verify(dir, pub, filepath.Join(root, "site"), r, m); err != nil {
		t.Fatal(err)
	}
	otherRoot, otherPub, _, _, _, _ := fixture(t)
	_ = otherRoot
	if err := Verify(dir, otherPub, filepath.Join(root, "site"), r, m); err == nil {
		t.Fatal("accepted wrong repository key")
	}
	entries, err := os.ReadDir(filepath.Join(dir, "repodata"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 5 {
		t.Fatalf("got %d metadata files", len(entries))
	}
	checkBad := func(label string, change func()) {
		t.Helper()
		change()
		if err := Verify(dir, pub, filepath.Join(root, "site"), r, m); err == nil {
			t.Fatalf("accepted %s", label)
		}
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		if err := Build(dir, cacheRoot, r, m, local); err != nil {
			t.Fatal(err)
		}
	}
	checkBad("signature", func() { mutate(t, filepath.Join(dir, "repodata", "repomd.xml.asc")) })
	checkBad("repomd", func() { mutate(t, filepath.Join(dir, "repodata", "repomd.xml")) })
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".gz") {
			name := entry.Name()
			checkBad(name, func() { mutate(t, filepath.Join(dir, "repodata", name)) })
		}
	}
	checkBad("package", func() { mutate(t, filepath.Join(root, "site", m.Packages[0].Path)) })
	retained := m
	retained.Packages = append([]contract.Package(nil), m.Packages...)
	retained.Packages[0].State = "retained"
	retained.Packages[0].RetainedAt = "2026-10-31T00:00:00Z"
	if err := Verify(dir, pub, "", r, retained); err == nil {
		t.Fatal("accepted retained package")
	}
}
func TestBuildRejectsChangedCache(t *testing.T) {
	root, _, cacheRoot, r, m, local := fixture(t)
	mutate(t, filepath.Join(cacheRoot, "sha256", m.Packages[0].SHA256))
	out := filepath.Join(root, "rpm")
	if err := Build(out, cacheRoot, r, m, local); err == nil {
		t.Fatal("accepted changed package")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("failed build left output")
	}
}
func TestTrustedPolicyRejectsUnsigned(t *testing.T) {
	root, _, cacheRoot, r, m, local := fixture(t)
	t.Chdir(root)
	r.RPM.PackageSignature = "trusted-packages"
	r.RPM.TrustedKeyPaths = []string{"KEY.gpg"}
	if err := m.Validate(r); err != nil {
		t.Fatal(err)
	}
	if err := Build(filepath.Join(root, "rpm"), cacheRoot, r, m, local); err == nil {
		t.Fatal("accepted unsigned package")
	}
}
func mutate(t *testing.T, name string) {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)/2] ^= 1
	if err := os.WriteFile(name, b, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestSignCopy(t *testing.T) {
	root, pub, cacheRoot, r, m, local := fixture(t)
	t.Chdir(root)
	trusted := []string{"KEY.gpg"}
	source := filepath.Join(cacheRoot, "sha256", m.Packages[0].SHA256)
	signed := filepath.Join(root, "signed.rpm")
	if err := SignCopy(source, signed, local, trusted); err != nil {
		t.Fatal(err)
	}
	if err := SignCopy(source, signed, local, trusted); err == nil {
		t.Fatal("overwrote an existing signed copy")
	}
	if err := VerifyPackageSignature(signed, trusted, "trusted-packages"); err != nil {
		t.Fatal(err)
	}
	meta, n, digest, err := inspect.File(signed, r.Downloads.MaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	p := m.Packages[0]
	p.Size = n
	p.SHA256 = digest
	p.Identity = meta.Identity
	p.Path, err = contract.ServedPath(p)
	if err != nil {
		t.Fatal(err)
	}
	if p.Path == m.Packages[0].Path {
		t.Fatal("signed copy path did not change")
	}
	r.RPM.PackageSignature = "trusted-packages"
	r.RPM.TrustedKeyPaths = trusted
	m.Packages = []contract.Package{p}
	if err := os.WriteFile(filepath.Join(cacheRoot, "sha256", digest), mustRead(t, signed), 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "rpm")
	if err := Build(out, cacheRoot, r, m, local); err != nil {
		t.Fatal(err)
	}
	site := filepath.Join(root, "site")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(site, p.Path)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(site, p.Path), mustRead(t, signed), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(site, "KEY.gpg"), mustRead(t, pub), 0644); err != nil {
		t.Fatal(err)
	}
	if err := Verify(out, pub, site, r, m); err != nil {
		t.Fatal(err)
	}
}

func TestSignCopyFailureLeavesNoDestination(t *testing.T) {
	root, _, cacheRoot, _, m, local := fixture(t)
	source := filepath.Join(cacheRoot, "sha256", m.Packages[0].SHA256)
	destination := filepath.Join(root, "signed.rpm")
	if err := SignCopy(source, destination, local, []string{filepath.Join(root, "missing.gpg")}); err == nil {
		t.Fatal("accepted a missing trusted key")
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatal("failed signing left a destination")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".ghrepo-signed-") {
			t.Fatal("failed signing left a temporary file")
		}
	}
}
func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPayloadFilesInMetadata(t *testing.T) {
	root, pub, cacheRoot, r, m, local := fixture(t)
	raw, err := os.ReadFile("../contract/testdata/packages/payload-gzip.rpm")
	if err != nil {
		t.Fatal(err)
	}
	meta, err := inspect.Bytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	p := contract.Package{URL: "https://example.com/payload.rpm", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(raw)), Source: "upstream", State: "active", Format: "rpm", Identity: meta.Identity}
	p.Path, err = contract.ServedPath(p)
	if err != nil {
		t.Fatal(err)
	}
	m.Packages = append(m.Packages, p)
	if err := os.WriteFile(filepath.Join(cacheRoot, "sha256", p.SHA256), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, "site", p.Path)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "site", p.Path), raw, 0644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "rpm")
	if err := Build(dir, cacheRoot, r, m, local); err != nil {
		t.Fatal(err)
	}
	if err := Verify(dir, pub, filepath.Join(root, "site"), r, m); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "repodata"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), "-filelists.xml.gz") {
			b := mustRead(t, filepath.Join(dir, "repodata", e.Name()))
			zr, err := gzip.NewReader(bytes.NewReader(b))
			if err != nil {
				t.Fatal(err)
			}
			plain, err := io.ReadAll(zr)
			if err != nil {
				t.Fatal(err)
			}
			if err := zr.Close(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(plain), "<file") {
				t.Fatal("RPM payload files missing from filelists")
			}
			return
		}
	}
	t.Fatal("filelists metadata missing")
}

func TestRPMFilePathCompatibility(t *testing.T) {
	for _, name := range []string{"/usr/share/notes..txt", "/usr/share/...", "/usr/share/.hidden", "/usr/share/regular"} {
		if !safeRPMFilePath(name) {
			t.Errorf("rejected valid path %q", name)
		}
	}
	for _, name := range []string{"relative", "/../escape", "/usr/../escape", "/usr/share/..", "/usr/\x00file"} {
		if safeRPMFilePath(name) {
			t.Errorf("accepted unsafe path %q", name)
		}
	}
}

func TestTranslatedRPMBuildVerify(t *testing.T) {
	root, pub, cacheRoot, r, m, local := fixture(t)
	raw := mustRead(t, "../contract/testdata/packages/compat.rpm")
	meta, err := inspect.Bytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Fields["Summary"] != "Compatibility summary" || meta.Fields["Description"] != "Compatibility description." {
		t.Fatalf("lost default text: %+v", meta.Fields)
	}
	r.RPM.Architectures = []string{"noarch"}
	sum := sha256.Sum256(raw)
	p := &m.Packages[0]
	p.Identity, p.Size, p.SHA256 = meta.Identity, int64(len(raw)), hex.EncodeToString(sum[:])
	p.Path, err = contract.ServedPath(*p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheRoot, "sha256", p.SHA256), raw, 0600); err != nil {
		t.Fatal(err)
	}
	site := filepath.Join(root, "site")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(site, p.Path)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(site, p.Path), raw, 0644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "rpm")
	if err := Build(out, cacheRoot, r, m, local); err != nil {
		t.Fatal(err)
	}
	if err := Verify(out, pub, site, r, m); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"primary", "filelists"} {
		files, err := filepath.Glob(filepath.Join(out, "repodata", "*-"+kind+".xml.gz"))
		if err != nil || len(files) != 1 {
			t.Fatalf("%s files = %v, %v", kind, files, err)
		}
		gz, err := gzip.NewReader(bytes.NewReader(mustRead(t, files[0])))
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(gz)
		if err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		if kind == "primary" {
			if !bytes.Contains(data, []byte("Compatibility summary")) || !bytes.Contains(data, []byte("Compatibility description.")) {
				t.Fatalf("missing translated text in index: %s", data)
			}
		} else if !bytes.Contains(data, []byte("/usr/share/ghrepo-compat/notes..txt")) {
			t.Fatalf("missing valid file path in index: %s", data)
		}
	}
}
