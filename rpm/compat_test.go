package rpm

import (
	"crypto/sha256"
	"encoding/hex"
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

// Run this test in a disposable environment with rpm and DNF installed.
func TestCompatibility(t *testing.T) {
	for _, tool := range []string{"rpm"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " unavailable")
		}
	}
	dnfTool := "dnf"
	if _, err := exec.LookPath(dnfTool); err != nil {
		dnfTool = "dnf5"
		if _, err := exec.LookPath(dnfTool); err != nil {
			t.Skip("DNF unavailable")
		}
	}
	fixtureDir, err := filepath.Abs("../contract/testdata/packages")
	if err != nil {
		t.Fatal(err)
	}
	root, pub, cacheRoot, r, m, local := fixture(t)
	t.Chdir(root)
	site := filepath.Join(root, "site")
	for _, fixtureName := range []string{"payload-gzip.rpm", "payload-xz.rpm"} {
		raw, err := os.ReadFile(filepath.Join(fixtureDir, fixtureName))
		if err != nil {
			t.Fatal(err)
		}
		meta, err := inspect.Bytes(raw)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		p := contract.Package{URL: "https://example.com/" + fixtureName, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(raw)), Source: "upstream", State: "active", Format: "rpm", Identity: meta.Identity}
		p.Path, err = contract.ServedPath(p)
		if err != nil {
			t.Fatal(err)
		}
		m.Packages = append(m.Packages, p)
		if err := os.WriteFile(filepath.Join(cacheRoot, "sha256", p.SHA256), raw, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(site, p.Path)), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(site, p.Path), raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
	metadata := filepath.Join(root, "metadata")
	if err := Build(metadata, cacheRoot, r, m, local); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(metadata, "repodata"), filepath.Join(site, "rpm", "repodata")); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.FileServer(http.Dir(site)))
	defer server.Close()
	cmdExport := exec.Command("gpg", "--batch", "--armor", "--export", local.GPG.KeyID)
	cmdExport.Env = append(os.Environ(), "GNUPGHOME="+local.GPG.Home)
	pubText, err := cmdExport.Output()
	if err != nil {
		t.Fatal(err)
	}
	armored := filepath.Join(site, "KEY.asc")
	if err := os.WriteFile(armored, pubText, 0644); err != nil {
		t.Fatal(err)
	}
	runDNF := func(policy string) {
		t.Helper()
		install := filepath.Join(root, "install-"+policy)
		args := []string{"--assumeyes", "--disableplugin=versionlock", "--installroot=" + install, "--releasever=1", "--setopt=reposdir=" + filepath.Join(root, "empty-repos"), "--setopt=cachedir=" + filepath.Join(root, "cache-"+policy), "--repofrompath=fixture," + server.URL + "/rpm/", "--setopt=fixture.repo_gpgcheck=1", "--setopt=fixture.gpgcheck=" + policy, "--setopt=fixture.gpgkey=" + server.URL + "/KEY.asc", "makecache"}
		cmd := exec.Command(dnfTool, args...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("DNF makecache: %v: %s", err, output)
		}
		for _, p := range m.Packages {
			query := append(append([]string(nil), args...), p.Identity.Name)
			query[len(query)-2] = "repoquery"
			cmd = exec.Command(dnfTool, query...)
			output, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(output), p.Identity.Name) {
				t.Fatalf("DNF repoquery: %v: %s", err, output)
			}
			query[len(query)-2] = "download"
			cmd = exec.Command(dnfTool, query...)
			cmd.Dir = root
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("DNF download: %v: %s", err, output)
			}
		}
		if os.Getenv("GHREPO_RPM_INSTALL") == "1" {
			installArgs := append(append([]string(nil), args...), m.Packages[0].Identity.Name)
			installArgs[len(installArgs)-2] = "install"
			cmd = exec.Command(dnfTool, installArgs...)
			cmd.Dir = root
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("DNF install: %v: %s", err, output)
			}
		}
	}
	if err := os.Mkdir(filepath.Join(root, "empty-repos"), 0755); err != nil {
		t.Fatal(err)
	}
	runDNF("0")
	if os.Getenv("GHREPO_RPM_INSTALL") == "1" {
		args := []string{
			"--assumeyes", "--disableplugin=versionlock",
			"--installroot=" + filepath.Join(root, "reject-unsigned"), "--releasever=1",
			"--setopt=reposdir=" + filepath.Join(root, "empty-repos"),
			"--setopt=cachedir=" + filepath.Join(root, "reject-cache"),
			"--repofrompath=fixture," + server.URL + "/rpm/",
			"--setopt=fixture.repo_gpgcheck=1", "--setopt=fixture.gpgcheck=1",
			"--setopt=fixture.gpgkey=" + server.URL + "/KEY.asc",
			"install", m.Packages[0].Identity.Name,
		}
		cmd := exec.Command(dnfTool, args...)
		if output, err := cmd.CombinedOutput(); err == nil || !strings.Contains(strings.ToLower(string(output)), "not signed") {
			t.Fatalf("DNF unsigned package rejection: %v: %s", err, output)
		}
	}
	unsignedDB := filepath.Join(root, "unsigned-rpmdb")
	if err := os.Mkdir(unsignedDB, 0755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"--dbpath", unsignedDB, "--initdb"},
		{"--dbpath", unsignedDB, "--nosignature", "--nodeps", "--install", filepath.Join(cacheRoot, "sha256", m.Packages[0].SHA256)},
		{"--dbpath", unsignedDB, "--query", m.Packages[0].Identity.Name},
	} {
		cmd := exec.Command("rpm", args...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("RPM unsigned install: %v: %s", err, output)
		}
	}
	signed := filepath.Join(root, "signed.rpm")
	if err := SignCopy(filepath.Join(cacheRoot, "sha256", m.Packages[0].SHA256), signed, local, []string{pub}); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(root, "rpmdb")
	if err := os.Mkdir(db, 0755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--dbpath", db, "--initdb"}, {"--dbpath", db, "--import", armored}} {
		cmd := exec.Command("rpm", args...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("rpm setup: %v: %s", err, output)
		}
	}
	cmd := exec.Command("rpm", "--dbpath", db, "--checksig", signed)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("rpm checksig: %v: %s", err, output)
	}
	for _, args := range [][]string{
		{"--dbpath", db, "--nodeps", "--install", signed},
		{"--dbpath", db, "--query", m.Packages[0].Identity.Name},
	} {
		cmd := exec.Command("rpm", args...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("RPM signed install: %v: %s", err, output)
		}
	}
	meta, n, digest, err := inspect.File(signed, r.Downloads.MaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	p := m.Packages[0]
	p.Identity, p.Size, p.SHA256 = meta.Identity, n, digest
	p.Path, err = contract.ServedPath(p)
	if err != nil {
		t.Fatal(err)
	}
	m.Packages = []contract.Package{p}
	r.RPM.PackageSignature = "trusted-packages"
	r.RPM.TrustedKeyPaths = []string{"KEY.gpg"}
	if err := os.WriteFile(filepath.Join(cacheRoot, "sha256", digest), mustRead(t, signed), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(site, p.Path)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(site, p.Path), mustRead(t, signed), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(site, "rpm", "repodata")); err != nil {
		t.Fatal(err)
	}
	metadata2 := filepath.Join(root, "signed-metadata")
	if err := Build(metadata2, cacheRoot, r, m, local); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(metadata2, "repodata"), filepath.Join(site, "rpm", "repodata")); err != nil {
		t.Fatal(err)
	}
	runDNF("1")
}
