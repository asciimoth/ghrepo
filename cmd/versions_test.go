package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/ghrepo/contract"
)

func TestComparePackageVersions(t *testing.T) {
	tests := []struct {
		format, a, b string
		want         int
	}{
		{"deb", "1.9", "1.10", -1}, {"deb", "1.0~rc1", "1.0", -1},
		{"deb", "1.0", "1.0-0", 0}, {"deb", "1.0-2", "1.0-10", -1},
		{"deb", "2:1.0", "1:99.0", 1}, {"deb", "1.001", "1.1", 0},
		{"deb", "1.0a", "1.0+", -1}, {"deb", "1.0~~", "1.0~", -1},
		{"deb", "1.0-1+b1", "1.0-1+b2", -1},
		{"rpm", "1.9", "1.10", -1}, {"rpm", "1.0~rc1", "1.0", -1},
		{"rpm", "1.0^git1", "1.0", 1}, {"rpm", "1.0^git1", "1.0.1", -1},
		{"rpm", "1.0^git1", "1.0^git2", -1}, {"rpm", "1.0", "1+0", 0},
		{"rpm", "1.001", "1.1", 0}, {"rpm", "1.a", "1.1", -1},
		{"rpm", "1.0+", "1.0", 0},
	}
	for _, tt := range tests {
		t.Run(tt.format+"/"+tt.a+"/"+tt.b, func(t *testing.T) {
			a := contract.Package{Format: tt.format, Identity: contract.Identity{Version: tt.a}}
			b := contract.Package{Format: tt.format, Identity: contract.Identity{Version: tt.b}}
			if got := comparePackageVersions(a, b); got != tt.want {
				t.Fatalf("got %d, want %d", got, tt.want)
			}
			if got := comparePackageVersions(b, a); got != -tt.want {
				t.Fatalf("reverse got %d", got)
			}
		})
	}
	a := contract.Package{Format: "rpm", Identity: contract.Identity{Epoch: "2", Version: "1", Release: "1"}}
	b := contract.Package{Format: "rpm", Identity: contract.Identity{Epoch: "1", Version: "99", Release: "99"}}
	if comparePackageVersions(a, b) <= 0 {
		t.Fatal("RPM epoch ignored")
	}
	b.Identity = a.Identity
	b.Identity.Release = "10"
	if comparePackageVersions(a, b) >= 0 {
		t.Fatal("RPM release ignored")
	}
}

func TestRetainOlderVersions(t *testing.T) {
	makePackage := func(format, name, arch, version string) contract.Package {
		return contract.Package{Format: format, State: "active", Identity: contract.Identity{Name: name, Architecture: arch, Version: version}}
	}
	m := contract.Manifest{Packages: []contract.Package{
		makePackage("deb", "foo", "amd64", "1.10"),
		makePackage("deb", "foo", "amd64", "1.9"),
		makePackage("deb", "foo", "arm64", "1.0"),
		makePackage("deb", "bar", "amd64", "1.0"),
		makePackage("rpm", "foo", "amd64", "1.0"),
		makePackage("deb", "foo", "amd64", "1.010"),
		makePackage("deb", "foo", "amd64", "99"),
	}}
	m.Packages[6].State = "retained"
	m.Packages[6].RetainedAt = "2025-01-01T00:00:00Z"
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := retainOlderVersions(&m, now); len(got) != 1 {
		t.Fatalf("retained %d packages", len(got))
	}
	for i, p := range m.Packages {
		if i == 1 {
			if p.State != "retained" || p.RetainedAt != now.Format(time.RFC3339) {
				t.Fatal(p)
			}
		} else if i == 6 {
			if p.State != "retained" || p.RetainedAt != "2025-01-01T00:00:00Z" {
				t.Fatal(p)
			}
		} else if p.State != "active" {
			t.Fatal(p)
		}
	}
	if got := retainOlderVersions(&m, now.Add(time.Hour)); len(got) != 0 {
		t.Fatal("cleanup is not idempotent")
	}
}

func TestRebuildRetiresAndPrunesVersions(t *testing.T) {
	c, dir := prepareFlow(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c.Now = func() time.Time { return now }
	if err := c.Run(ctx, []string{"rebuild"}); err != nil {
		t.Fatal(err)
	}
	fetch := c.Fetcher.(fixtureFetcher)
	fetch.data["https://example.com/new.deb"] = mustRead(t, "../contract/testdata/packages/hello_2.0.0-1_amd64.deb")
	if err := c.Run(ctx, []string{"add", "https://example.com/new.deb"}); err != nil {
		t.Fatal(err)
	}
	before := mustRead(t, filepath.Join(dir, "packages.yaml"))
	var out bytes.Buffer
	c.Out = &out
	if err := c.Run(ctx, []string{"rebuild", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, mustRead(t, filepath.Join(dir, "packages.yaml"))) {
		t.Fatal("dry run changed manifest")
	}
	if !strings.Contains(out.String(), "retained older version") {
		t.Fatal(out.String())
	}
	if err := c.Run(ctx, []string{"rebuild"}); err != nil {
		t.Fatal(err)
	}
	r, m, err := c.load()
	if err != nil {
		t.Fatal(err)
	}
	old := m.Packages[0]
	if old.State != "retained" || old.RetainedAt != now.Format(time.RFC3339) {
		t.Fatal(old)
	}
	index := string(mustRead(t, filepath.Join(dir, "deb", "Packages")))
	if strings.Contains(index, "Version: 1.2.0-1\n") || !strings.Contains(index, "Version: 2.0.0-1\n") {
		t.Fatal(index)
	}
	if err := c.Run(ctx, []string{"check"}); err != nil {
		t.Fatal(err)
	}
	now = now.AddDate(0, 0, r.Site.RetentionDays)
	if err := c.Run(ctx, []string{"rebuild"}); err != nil {
		t.Fatal(err)
	}
	_, m, err = c.load()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range m.Packages {
		if p.Path == old.Path {
			t.Fatal("expired version remains")
		}
	}
	if err := c.Run(ctx, []string{"check"}); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverManifestTransaction(t *testing.T) {
	dir := t.TempDir()
	c := Runner{Dir: dir}
	tx := c.transaction()
	if err := os.Mkdir(tx, 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"pending.json":      `{"had_manifest":true}`,
		"old-packages.yaml": "original manifest",
	} {
		if err := os.WriteFile(filepath.Join(tx, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "packages.yaml"), []byte("new manifest"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.recoverTransaction(); err != nil {
		t.Fatal(err)
	}
	if string(mustRead(t, filepath.Join(dir, "packages.yaml"))) != "original manifest" {
		t.Fatal("manifest not restored")
	}
}
