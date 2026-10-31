package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testRepository() Repository {
	return Repository{
		Schema:     1,
		Repository: Repo{URL: "https://github.com/example/repo", Branch: "main", Layout: "flat-v1"},
		APT:        APT{Suite: "stable", Component: "main", Architectures: []string{"amd64"}},
		RPM:        RPM{ID: "test", Name: "Test packages", Architectures: []string{"x86_64"}, PackageSignature: "metadata-only"},
		Downloads:  Download{SourceHosts: []string{"example.com"}, RedirectHosts: []string{"cdn.example.com"}, MaxBytes: 1000},
		Site:       Site{BaseURL: "https://example.com/repo", MaxBytes: 100000, RetentionDays: 30},
	}
}

func testPackage() Package {
	p := Package{URL: "https://example.com/hello.deb", SHA256: strings.Repeat("a", 64), Size: 10, Source: "upstream", State: "active", Format: "deb", Identity: Identity{Name: "hello", Version: "1.0-1", Architecture: "amd64"}}
	p.Path, _ = ServedPath(p)
	return p
}

func TestSchemaRoundTrip(t *testing.T) {
	r := testRepository()
	b, err := MarshalRepository(r)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := ParseRepository(b)
	if err != nil || r2.Repository.URL != r.Repository.URL {
		t.Fatalf("repository round trip: %v", err)
	}
	p := testPackage()
	m := Manifest{Schema: 1, Packages: []Package{p}}
	b, err = MarshalManifest(m, r)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := ParseManifest(b, r)
	if err != nil || len(m2.Packages) != 1 || m2.Packages[0].Path != p.Path {
		t.Fatalf("manifest round trip: %v", err)
	}
	l := Local{Schema: 1, GitHub: LocalGitHub{Account: "example"}}
	b, err = MarshalLocal(l)
	if err != nil {
		t.Fatal(err)
	}
	l2, err := ParseLocal(b)
	if err != nil || l2.GitHub.Account != l.GitHub.Account {
		t.Fatalf("local round trip: %v", err)
	}
}

func TestManifestWritingIsStable(t *testing.T) {
	r := testRepository()
	a := testPackage()
	b := a
	b.Identity.Name = "zebra"
	b.Path, _ = ServedPath(b)
	first, err := MarshalManifest(Manifest{Schema: 1, Packages: []Package{b, a}}, r)
	if err != nil {
		t.Fatal(err)
	}
	second, err := MarshalManifest(Manifest{Schema: 1, Packages: []Package{a, b}}, r)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("package input order changed output")
	}
	if strings.Index(string(first), a.Path) > strings.Index(string(first), b.Path) {
		t.Fatal("output is not sorted by path")
	}
}

func TestEmptyManifestRoundTrip(t *testing.T) {
	r := testRepository()
	b, err := MarshalManifest(Manifest{Schema: 1, Packages: []Package{}}, r)
	if err != nil {
		t.Fatal(err)
	}
	m, err := ParseManifest(b, r)
	if err != nil || len(m.Packages) != 0 {
		t.Fatalf("empty manifest failed round trip: %v", err)
	}
}

func TestStrictYAML(t *testing.T) {
	r := testRepository()
	b, _ := MarshalRepository(r)
	for _, data := range [][]byte{
		append(append([]byte(nil), b...), []byte("schema: 1\n")...),
		append(append([]byte(nil), b...), []byte("unknown: true\n")...),
		append(append([]byte(nil), b...), []byte("---\nschema: 1\n")...),
		[]byte("schema: &a 1\nrepository: *a\n"),
	} {
		if _, err := ParseRepository(data); err == nil {
			t.Fatalf("accepted invalid YAML: %q", data)
		}
	}
	p := testPackage()
	m := Manifest{Schema: 1, Packages: []Package{p}}
	manifestYAML, _ := MarshalManifest(m, r)
	for _, data := range [][]byte{
		append(append([]byte(nil), manifestYAML...), []byte("packages: []\n")...),
		append(append([]byte(nil), manifestYAML...), []byte("extra: 1\n")...),
	} {
		if _, err := ParseManifest(data, r); err == nil {
			t.Fatal("accepted invalid manifest YAML")
		}
	}
}

func TestManifestRejectCases(t *testing.T) {
	r := testRepository()
	p := testPackage()
	cases := map[string]func(*Package){
		"unsafe path":  func(p *Package) { p.Path = "deb/../bad" },
		"wrong path":   func(p *Package) { p.Path = "deb/pool/other.deb" },
		"bad hash":     func(p *Package) { p.SHA256 = "bad" },
		"zero size":    func(p *Package) { p.Size = 0 },
		"large size":   func(p *Package) { p.Size = 1001 },
		"state":        func(p *Package) { p.State = "deleted" },
		"source":       func(p *Package) { p.Source = "unknown" },
		"http":         func(p *Package) { p.URL = "http://example.com/file" },
		"host":         func(p *Package) { p.URL = "https://elsewhere.com/file" },
		"token":        func(p *Package) { p.URL = "https://example.com/file?access_token=private" },
		"query":        func(p *Package) { p.URL = "https://example.com/file?download=1" },
		"fragment":     func(p *Package) { p.URL = "https://example.com/file#" },
		"userinfo":     func(p *Package) { p.URL = "https://user:private@example.com/file" },
		"architecture": func(p *Package) { p.Identity.Architecture = "arm64" },
		"retention":    func(p *Package) { p.State = "retained" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			q := p
			change(&q)
			if err := (Manifest{Schema: 1, Packages: []Package{q}}).Validate(r); err == nil {
				t.Fatal("accepted invalid package")
			}
		})
	}
	if err := (Manifest{Schema: 1, Packages: []Package{p, p}}).Validate(r); err == nil {
		t.Fatal("accepted duplicate path and identity")
	}
	q := p
	q.SHA256 = strings.Repeat("b", 64)
	q.Path, _ = ServedPath(q)
	if err := (Manifest{Schema: 1, Packages: []Package{p, q}}).Validate(r); err == nil {
		t.Fatal("accepted duplicate active identity")
	}
	q.State, q.RetainedAt = "retained", "2026-10-31T00:00:00Z"
	if err := (Manifest{Schema: 1, Packages: []Package{p, q}}).Validate(r); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalRPMEpoch(t *testing.T) {
	p := testPackage()
	p.Format = "rpm"
	p.Identity = Identity{Name: "hello", Epoch: "0", Version: "1.0", Release: "1", Architecture: "x86_64"}
	p.Path, _ = ServedPath(p)
	if err := (Manifest{Schema: 1, Packages: []Package{p}}).Validate(testRepository()); err != nil {
		t.Fatal(err)
	}
	p.Identity.Epoch = "00"
	if _, err := ServedPath(p); err == nil {
		t.Fatal("accepted noncanonical RPM epoch")
	}
}

func TestConfigRejectCases(t *testing.T) {
	r := testRepository()
	cases := []func(*Repository){
		func(r *Repository) { r.Schema = 2 },
		func(r *Repository) { r.Repository.URL = "http://github.com/example" },
		func(r *Repository) { r.Downloads.SourceHosts = []string{"127.0.0.1"} },
		func(r *Repository) { r.Downloads.RedirectHosts = []string{"*.example.com"} },
		func(r *Repository) { r.Site.RetentionDays = 29 },
		func(r *Repository) { r.Site.MaxBytes = 1073741825 },
		func(r *Repository) { r.RPM.PackageSignature = "trusted-packages" },
		func(r *Repository) { r.Storage.Backend = "github-releases" },
	}
	for n, change := range cases {
		q := r
		change(&q)
		if q.Validate() == nil {
			t.Fatalf("accepted invalid config case %d", n)
		}
	}
}

func TestEnvPrecedenceAndRedaction(t *testing.T) {
	secret := "very-secret-password"
	values, err := ParseEnv([]byte("GH_TOKEN='" + secret + "'\nGHREPO_GITHUB_ACCOUNT=from-file\n"))
	if err != nil {
		t.Fatal(err)
	}
	l := Local{Schema: 1, GitHub: LocalGitHub{Token: "local", Account: "local"}}
	effective := EffectiveLocal(l, values, map[string]string{"GH_TOKEN": "process"})
	if effective.GitHub.Token != "process" || effective.GitHub.Account != "from-file" {
		t.Fatal("wrong precedence")
	}
	literal, err := ParseEnv([]byte("GH_TOKEN=$(false)\n"))
	if err != nil || literal["GH_TOKEN"] != "$(false)" {
		t.Fatal(".env value was interpreted")
	}
	for _, input := range []string{"GH_TOKEN=x\nGH_TOKEN=y\n", "export GH_TOKEN=x\n", "GH_TOKEN=\"unterminated\n"} {
		if _, err := ParseEnv([]byte(input)); err == nil {
			t.Fatalf("accepted bad .env: %q", input)
		}
	}
	p := testPackage()
	p.URL = "https://user:" + secret + "@example.com/file"
	if err := (Manifest{Schema: 1, Packages: []Package{p}}).Validate(testRepository()); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("credential leaked or accepted: %v", err)
	}
	if _, err := ParseLocal([]byte("schema: 1\ngithub:\n  token: " + secret + "\n  unknown: x\n")); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("YAML credential leaked or accepted: %v", err)
	}
	dir := t.TempDir()
	name := filepath.Join(dir, ".ghrepo.local.yaml")
	if err := os.WriteFile(name, []byte("schema: 1\ngithub:\n  token: "+secret+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadLocal(name); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("unsafe token file: %v", err)
	}
}

func TestLoadEffectiveLocal(t *testing.T) {
	dir := t.TempDir()
	envName := filepath.Join(dir, "selected.env")
	if err := os.WriteFile(envName, []byte("GH_TOKEN=from-file\nGHREPO_GITHUB_ACCOUNT=file-account\n"), 0600); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(dir, ".ghrepo.local.yaml")
	l := Local{Schema: 1, GitHub: LocalGitHub{Token: "local", Account: "local-account", EnvFile: envName}}
	if err := WriteLocal(name, l); err != nil {
		t.Fatal(err)
	}
	effective, err := LoadEffectiveLocal(name, map[string]string{"GH_TOKEN": "process"})
	if err != nil || effective.GitHub.Token != "process" || effective.GitHub.Account != "file-account" {
		t.Fatalf("wrong loaded precedence: %v", err)
	}
	effective, err = LoadEffectiveLocal(name, nil)
	if err != nil || effective.GitHub.Token != "from-file" {
		t.Fatalf("selected .env was not loaded: %v", err)
	}
}

func TestAtomicWriteKeepsOldOnFailure(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(name, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomic(filepath.Join(dir, "missing", "manifest.yaml"), []byte("new"), 0600); err == nil {
		t.Fatal("expected write error")
	}
	b, err := os.ReadFile(name)
	if err != nil || string(b) != "old" {
		t.Fatalf("old file changed: %v", err)
	}
	if err := WriteAtomic(name, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(name)
	if string(b) != "new" {
		t.Fatal("atomic write failed")
	}
	invalid := Manifest{Schema: 1, Packages: []Package{testPackage()}}
	invalid.Packages[0].Size = 0
	if err := WriteManifest(name, invalid, testRepository()); err == nil {
		t.Fatal("accepted invalid manifest snapshot")
	}
	b, _ = os.ReadFile(name)
	if string(b) != "new" {
		t.Fatal("invalid snapshot replaced prior file")
	}
}
