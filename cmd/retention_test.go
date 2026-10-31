package cmd

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/ghrepo/contract"
	"github.com/asciimoth/ghrepo/site"
)

func TestReplaceAndPrune(t *testing.T) {
	for _, fixture := range []string{"hello_1.2.0-1_amd64.deb", "empty-0.1-1.x86_64.rpm"} {
		t.Run(fixture, func(t *testing.T) {
			original := mustRead(t, filepath.Join("../contract/testdata/packages", fixture))
			changed := append(append([]byte(nil), original...), 0)
			if strings.HasSuffix(fixture, ".deb") {
				changed = mustRead(t, "../contract/testdata/packages/hello_changed.deb")
			}
			dir := t.TempDir()
			first, second := "https://example.com/old", "https://example.com/new"
			start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			now := start
			c := Runner{Dir: dir, Fetcher: fixtureFetcher{map[string][]byte{first: original, second: changed}}, Now: func() time.Time { return now }}
			ctx := context.Background()
			if err := c.Run(ctx, []string{"init", "--repo", "https://github.com/example/repo", "--site", "https://example.github.io/repo", "--source-hosts", "example.com"}); err != nil {
				t.Fatal(err)
			}
			if err := c.Run(ctx, []string{"add", first}); err != nil {
				t.Fatal(err)
			}
			r, m, err := c.load()
			if err != nil {
				t.Fatal(err)
			}
			old := m.Packages[0]
			if err := c.Run(ctx, []string{"replace", old.Path, second}); err != nil {
				t.Fatal(err)
			}
			_, m, err = c.load()
			if err != nil {
				t.Fatal(err)
			}
			if len(m.Packages) != 2 || m.Packages[0].State != "retained" || m.Packages[1].State != "active" || m.Packages[0].Path == m.Packages[1].Path || m.Packages[0].Identity != m.Packages[1].Identity {
				t.Fatal(m)
			}
			if err := c.Run(ctx, []string{"replace", m.Packages[1].Path, second}); err == nil {
				t.Fatal("accepted identical replacement")
			}
			if err := c.Run(ctx, []string{"add", second}); err != nil {
				t.Fatalf("repeat add of active package failed: %v", err)
			}
			now = start.AddDate(0, 0, r.Site.RetentionDays)
			var out bytes.Buffer
			c.Out = &out
			if err := c.Run(ctx, []string{"prune"}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), old.Path) {
				t.Fatal(out.String())
			}
			_, m, err = c.load()
			if err != nil || len(m.Packages) != 2 {
				t.Fatalf("preview changed manifest: %v %v", m, err)
			}
			if err := c.Run(ctx, []string{"prune", "--apply"}); err != nil {
				t.Fatal(err)
			}
			_, m, err = c.load()
			if err != nil || len(m.Packages) != 1 || m.Packages[0].Path == old.Path {
				t.Fatalf("prune did not remove old path: %v %v", m, err)
			}
		})
	}
}

func TestPruneBoundaryAndRollbackReference(t *testing.T) {
	data := mustRead(t, "../contract/testdata/packages/hello_1.2.0-1_amd64.deb")
	dir := t.TempDir()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start
	c := Runner{Dir: dir, Fetcher: fixtureFetcher{map[string][]byte{"https://example.com/a": data}}, Now: func() time.Time { return now }}
	ctx := context.Background()
	if err := c.Run(ctx, []string{"init", "--repo", "https://github.com/example/repo", "--site", "https://example.github.io/repo", "--source-hosts", "example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Run(ctx, []string{"add", "https://example.com/a"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Run(ctx, []string{"remove", "deb/hello/amd64/1.2.0-1"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	c.Out = &out
	now = start.AddDate(0, 0, 30).Add(-time.Second)
	if err := c.Run(ctx, []string{"prune"}); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatal("pruned before boundary")
	}
	command(t, dir, "git", "init")
	command(t, dir, "git", "add", "ghrepo.yaml", "packages.yaml")
	command(t, dir, "git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "rollback")
	command(t, dir, "git", "tag", "rollback")
	now = now.Add(time.Second)
	if err := c.Run(ctx, []string{"prune"}); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatal("pruned rollback reference")
	}
	command(t, dir, "git", "tag", "-d", "rollback")
	if err := c.Run(ctx, []string{"prune"}); err != nil {
		t.Fatal(err)
	}
	if out.Len() == 0 {
		t.Fatal("current branch tip blocked pruning")
	}
}

func TestReplacementRollbackPaths(t *testing.T) {
	for _, pair := range [][2]string{{"hello_1.2.0-1_amd64.deb", "hello_changed.deb"}, {"empty-0.1-1.x86_64.rpm", ""}} {
		t.Run(pair[0], func(t *testing.T) {
			original := mustRead(t, filepath.Join("../contract/testdata/packages", pair[0]))
			changed := append(append([]byte(nil), original...), 0)
			if pair[1] != "" {
				changed = mustRead(t, filepath.Join("../contract/testdata/packages", pair[1]))
			}
			dir := t.TempDir()
			first, second := "https://example.com/a", "https://example.com/b"
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			c := Runner{Dir: dir, Fetcher: fixtureFetcher{map[string][]byte{first: original, second: changed}}, Now: func() time.Time { return now }}
			ctx := context.Background()
			if err := c.Run(ctx, []string{"init", "--repo", "https://github.com/example/repo", "--site", "https://example.github.io/repo", "--source-hosts", "example.com"}); err != nil {
				t.Fatal(err)
			}
			if err := c.Run(ctx, []string{"add", first}); err != nil {
				t.Fatal(err)
			}
			_, m, err := c.load()
			if err != nil {
				t.Fatal(err)
			}
			oldPath := m.Packages[0].Path
			command(t, dir, "git", "init")
			command(t, dir, "git", "add", "ghrepo.yaml", "packages.yaml")
			command(t, dir, "git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "original")
			command(t, dir, "git", "tag", "rollback")
			if err := c.Run(ctx, []string{"replace", oldPath, second}); err != nil {
				t.Fatal(err)
			}
			now = now.AddDate(0, 0, 30)
			var out bytes.Buffer
			c.Out = &out
			if err := c.Run(ctx, []string{"prune", "--apply"}); err != nil {
				t.Fatal(err)
			}
			_, m, err = c.load()
			if err != nil || len(m.Packages) != 2 || out.Len() != 0 {
				t.Fatalf("rollback path removed: %v %v", m, err)
			}
		})
	}
}

func TestRPMMetadataRetentionAge(t *testing.T) {
	c, dir := prepareFlow(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start
	c.Now = func() time.Time { return now }
	if err := c.Run(context.Background(), []string{"rebuild"}); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "rpm", "repodata")
	next := filepath.Join(t.TempDir(), "next")
	if err := os.Mkdir(next, 0755); err != nil {
		t.Fatal(err)
	}
	content := []byte("old metadata")
	sum := sha256.Sum256(content)
	name := fmt.Sprintf("%x-primary.xml.gz", sum)
	if err := os.WriteFile(filepath.Join(old, name), content, 0644); err != nil {
		t.Fatal(err)
	}
	record := map[string]string{name: start.Format(time.RFC3339)}
	if err := writeRPMRetention(old, record); err != nil {
		t.Fatal(err)
	}
	now = start.AddDate(0, 0, 30).Add(-time.Second)
	if err := c.retainRPM(old, next, 30); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(next, name)); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(next); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(next, 0755); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if err := c.retainRPM(old, next, 30); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(next, name)); !os.IsNotExist(err) {
		t.Fatal("expired metadata retained")
	}
	command(t, dir, "git", "init")
	command(t, dir, "git", "add", "ghrepo.yaml", "packages.yaml", "rpm/repodata")
	command(t, dir, "git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "metadata")
	if err := os.Remove(filepath.Join(old, name)); err != nil {
		t.Fatal(err)
	}
	paths, err := c.allowedGitPaths()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range paths {
		if p == "rpm/repodata/"+name {
			found = true
		}
	}
	if !found {
		t.Fatal("expired metadata deletion is not staged")
	}
}

func TestSiteAfterPrune(t *testing.T) {
	c, dir := prepareFlow(t)
	ctx := context.Background()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start
	c.Now = func() time.Time { return now }
	_, m, err := c.load()
	if err != nil {
		t.Fatal(err)
	}
	var oldPath string
	for _, p := range m.Packages {
		if p.Format == "deb" {
			oldPath = p.Path
		}
	}
	if oldPath == "" {
		t.Fatal("missing DEB fixture")
	}
	if err := c.Run(ctx, []string{"remove", oldPath}); err != nil {
		t.Fatal(err)
	}
	if err := c.Run(ctx, []string{"rebuild"}); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "build", "site")
	if err := site.Build(ctx, dir, output, c.Fetcher); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(output, filepath.FromSlash(oldPath))); err != nil {
		t.Fatal(err)
	}
	now = start.AddDate(0, 0, 30)
	if err := c.Run(ctx, []string{"prune", "--apply"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Run(ctx, []string{"rebuild"}); err != nil {
		t.Fatal(err)
	}
	if err := site.Build(ctx, dir, output, c.Fetcher); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(output, filepath.FromSlash(oldPath))); !os.IsNotExist(err) {
		t.Fatal("pruned package remains on site")
	}
}

func TestRPMRollbackProtectsIndexedMetadataOnly(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "rpm", "repodata")
	if err := os.MkdirAll(data, 0755); err != nil {
		t.Fatal(err)
	}
	indexed := strings.Repeat("a", 64) + "-primary.xml.gz"
	stale := strings.Repeat("b", 64) + "-primary.xml.gz"
	xml := []byte(`<repomd><data type="primary"><location href="repodata/` + indexed + `"/></data></repomd>`)
	for name, content := range map[string][]byte{"repomd.xml": xml, indexed: []byte("indexed"), stale: []byte("stale")} {
		if err := os.WriteFile(filepath.Join(data, name), content, 0644); err != nil {
			t.Fatal(err)
		}
	}
	command(t, dir, "git", "init")
	command(t, dir, "git", "add", "rpm/repodata")
	command(t, dir, "git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "metadata")
	command(t, dir, "git", "tag", "rollback")
	c := Runner{Dir: dir}
	protected, err := c.protectedRPMMetadata()
	if err != nil {
		t.Fatal(err)
	}
	if !protected[indexed] || protected[stale] {
		t.Fatalf("unexpected rollback protection: %v", protected)
	}
}

func TestExpiredRPMMetadataLeavesSite(t *testing.T) {
	c, dir := prepareFlow(t)
	ctx := context.Background()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start
	c.Now = func() time.Time { return now }
	if err := c.Run(ctx, []string{"rebuild"}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	z := gzip.NewWriter(&buf)
	if _, err := z.Write([]byte("<metadata/>")); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	name := fmt.Sprintf("%x-primary.xml.gz", sum)
	repodata := filepath.Join(dir, "rpm", "repodata")
	if err := os.WriteFile(filepath.Join(repodata, name), buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeRPMRetention(repodata, map[string]string{name: start.Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "build", "site")
	now = start.AddDate(0, 0, 30).Add(-time.Second)
	if err := c.Run(ctx, []string{"rebuild"}); err != nil {
		t.Fatal(err)
	}
	if err := site.Build(ctx, dir, output, c.Fetcher); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(output, "rpm", "repodata", name)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if err := c.Run(ctx, []string{"rebuild"}); err != nil {
		t.Fatal(err)
	}
	if err := site.Build(ctx, dir, output, c.Fetcher); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(output, "rpm", "repodata", name)); !os.IsNotExist(err) {
		t.Fatal("expired RPM metadata remains on site")
	}
}

func TestReplacementRollbackBuildsOldBytes(t *testing.T) {
	c, dir := prepareFlow(t)
	ctx := context.Background()
	r, original, err := c.load()
	if err != nil {
		t.Fatal(err)
	}
	deb := mustRead(t, "../contract/testdata/packages/hello_changed.deb")
	rpm := append(mustRead(t, "../contract/testdata/packages/empty-0.1-1.x86_64.rpm"), 0)
	fetcher := c.Fetcher.(fixtureFetcher)
	fetcher.data["https://example.com/new.deb"] = deb
	fetcher.data["https://example.com/new.rpm"] = rpm
	c.Fetcher = fetcher
	for _, p := range original.Packages {
		url := "https://example.com/new.deb"
		if p.Format == "rpm" {
			url = "https://example.com/new.rpm"
		}
		if err := c.Run(ctx, []string{"replace", p.Path, url}); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Run(ctx, []string{"rebuild"}); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "build", "site")
	if err := site.Build(ctx, dir, output, c.Fetcher); err != nil {
		t.Fatal(err)
	}
	_, replaced, err := c.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(replaced.Packages) != 4 {
		t.Fatal(replaced)
	}
	for _, p := range replaced.Packages {
		if _, err := os.Stat(filepath.Join(output, filepath.FromSlash(p.Path))); err != nil {
			t.Fatal(err)
		}
	}
	if err := contract.WriteManifest(filepath.Join(dir, "packages.yaml"), original, r); err != nil {
		t.Fatal(err)
	}
	if err := c.Run(ctx, []string{"rebuild"}); err != nil {
		t.Fatal(err)
	}
	if err := site.Build(ctx, dir, output, c.Fetcher); err != nil {
		t.Fatal(err)
	}
	for _, p := range original.Packages {
		if _, err := os.Stat(filepath.Join(output, filepath.FromSlash(p.Path))); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range replaced.Packages {
		if p.State == "active" {
			if _, err := os.Stat(filepath.Join(output, filepath.FromSlash(p.Path))); !os.IsNotExist(err) {
				t.Fatal("new package remained after rollback")
			}
		}
	}
}
